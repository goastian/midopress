package imageapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/upstream"
	"github.com/goastian/midopress/internal/xcache"
)

type Transform func(context.Context, Job) (*Output, error)

type Options struct {
	Key          []byte
	PublicURL    string
	MaxBytes     int64
	MaxPixels    int
	MaxWidth     int
	MaxDPR       float64
	Concurrency  int
	Timeout      time.Duration
	SignatureTTL time.Duration
	CacheTTL     time.Duration
	Backend      string
	Profiles     func() map[string]*img.Profile
	Transform    Transform
}

type Service struct {
	o            Options
	transport    http.RoundTripper
	cache        *xcache.Cache
	slots        chan struct{}
	now          func() time.Time
	credentialMu sync.Mutex
	credentials  map[string]credentialWindow
}

func New(o Options) (*Service, error) {
	if len(o.Key) < 32 || o.MaxBytes < 1024 || o.MaxBytes > 64<<20 || o.MaxPixels < 1 ||
		o.MaxWidth < 64 || o.MaxDPR < 1 || o.Concurrency < 1 || o.Timeout <= 0 ||
		o.SignatureTTL < time.Second || o.CacheTTL <= 0 || o.Profiles == nil || o.Transform == nil {
		return nil, errors.New("invalid image API options")
	}
	s := &Service{o: o, slots: make(chan struct{}, o.Concurrency), cache: xcache.New(64 << 20), now: time.Now}
	s.transport = upstream.New(upstream.Options{
		DenyPrivate: true, DialTimeout: min(o.Timeout, 10*time.Second),
		ResponseHeaderTimeout: o.Timeout, DNSCacheTTL: -1,
		Deny: func(ap netip.AddrPort) error {
			if !publicAddress(ap.Addr()) || (ap.Port() != 80 && ap.Port() != 443) {
				return errDestination
			}
			return nil
		},
	})
	return s, nil
}

func (s *Service) Close() {
	if c, ok := s.transport.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

func (s *Service) ClearCache() { s.cache.Clear() }

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Expose-Headers", "X-MidoPress-Original-Bytes, X-MidoPress-Result-Bytes, X-MidoPress-Cache, X-MidoPress-Decision, X-MidoPress-Metric, X-MidoPress-Measure, X-MidoPress-Backend, Server-Timing")
	if r.URL.Path == "/img/sign" {
		s.serveSigner(w, r)
		return
	}
	if r.URL.Path != "/img" {
		s.fail(w, http.StatusNotFound, "not_found")
		return
	}
	if r.Method != http.MethodGet {
		h.Set("Allow", "GET")
		s.fail(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	spec, err := s.authenticate(r.URL.RawQuery)
	if err != nil {
		s.fail(w, http.StatusForbidden, "invalid_signature")
		return
	}
	if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Proxy-Authorization") != "" {
		s.fail(w, http.StatusConflict, "session_resource")
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		h.Set("Retry-After", "1")
		s.fail(w, http.StatusTooManyRequests, "busy")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.o.Timeout)
	defer cancel()
	start := time.Now()
	caps := img.CapsFromAccept(r.Header.Get("Accept"))
	accept := "image/png,image/jpeg,image/gif"
	if caps.WebP || spec.Format == "webp" {
		accept += ",image/webp"
	}
	if caps.AVIF || spec.Format == "avif" {
		accept += ",image/avif"
	}
	if caps.JXL || spec.Format == "jxl" {
		accept += ",image/jxl"
	}
	raw, err := s.fetch(ctx, spec.URL, accept)
	if err != nil {
		switch {
		case ctx.Err() != nil:
			s.fail(w, http.StatusGatewayTimeout, "timeout")
		case errors.Is(err, errSession):
			s.fail(w, http.StatusConflict, "session_resource")
		case errors.Is(err, errDestination), errors.Is(err, upstream.ErrBlockedAddress):
			s.fail(w, http.StatusForbidden, "destination_refused")
		case errors.Is(err, errSize):
			s.fail(w, http.StatusRequestEntityTooLarge, "byte_limit")
		default:
			s.fail(w, http.StatusBadGateway, "origin_failed")
		}
		return
	}
	profiles := s.o.Profiles()
	prof, ok := profiles[spec.Profile]
	if !ok {
		s.fail(w, http.StatusBadRequest, "invalid_profile")
		return
	}
	job := Job{Raw: raw, Request: spec, Profile: *prof, Caps: caps, Backend: s.o.Backend, MaxPixels: s.o.MaxPixels}
	key := variantKey(job)
	var out Output
	hit := false
	if entry, ok := s.cache.Get(key); ok && json.Unmarshal(entry.Data, &out) == nil {
		hit = true
	} else {
		result, err := s.o.Transform(ctx, job)
		if err != nil || result == nil {
			if ctx.Err() != nil {
				s.fail(w, http.StatusGatewayTimeout, "timeout")
			} else {
				s.fail(w, http.StatusUnprocessableEntity, "unsupported_or_pixel_limit")
			}
			return
		}
		out = *result
		if len(out.Data) == 0 || len(out.Data) > len(raw) || out.Format == img.Unknown || img.Sniff(out.Data) != out.Format ||
			math.IsNaN(out.Measure) || math.IsInf(out.Measure, 0) {
			s.fail(w, http.StatusUnprocessableEntity, "invalid_result")
			return
		}
		if ctx.Err() != nil {
			s.fail(w, http.StatusGatewayTimeout, "timeout")
			return
		}
		data, err := json.Marshal(out)
		if err == nil {
			s.cache.Add(key, &xcache.Entry{Data: data, Expires: s.now().Add(s.o.CacheTTL)})
		}
	}
	h.Set("Content-Type", out.Format.MIME())
	h.Set("Content-Length", strconv.Itoa(len(out.Data)))
	h.Set("X-MidoPress-Original-Bytes", strconv.Itoa(len(raw)))
	h.Set("X-MidoPress-Result-Bytes", strconv.Itoa(len(out.Data)))
	h.Set("X-MidoPress-Cache", "miss")
	if hit {
		h.Set("X-MidoPress-Cache", "hit")
	}
	h.Set("X-MidoPress-Decision", out.Decision)
	h.Set("X-MidoPress-Backend", out.Backend)
	if out.Metric != "" {
		h.Set("X-MidoPress-Metric", string(out.Metric))
		h.Set("X-MidoPress-Measure", strconv.FormatFloat(out.Measure, 'g', 6, 64))
	}
	h.Set("Server-Timing", fmt.Sprintf("image;dur=%.1f", float64(time.Since(start).Microseconds())/1000))
	_, _ = w.Write(out.Data)
}

func variantKey(j Job) string {
	h := sha256.New()
	_, _ = h.Write(j.Raw)
	_ = json.NewEncoder(h).Encode(struct {
		Version string
		Request Request
		Profile img.Profile
		Caps    img.Caps
		Backend string
	}{Version, j.Request, j.Profile, j.Caps, j.Backend})
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Service) fail(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "fallback": "original"})
}
