package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/goastian/midopress/internal/codec"
	"github.com/goastian/midopress/internal/httpx"
	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/xcache"
)

var (
	errBusy    = errors.New("pipeline: no free transcoding slot")
	errTimeout = errors.New("pipeline: transcoding deadline exceeded")
)

func (p *Pipeline) imageEnabled(kind img.Format) bool {
	ic := &p.cfg.Image
	switch kind {
	case img.JPEG:
		return ic.JPEG
	case img.PNG:
		return ic.PNG
	case img.GIF:
		return ic.GIF
	}
	return false
}

// expiry is when a result computed for a response with these headers should
// leave the cache.
func (p *Pipeline) expiry(h http.Header) time.Time {
	now := time.Now()
	return now.Add(ttlFor(h, now, p.live.Load().ttl))
}

// quality returns the fixed quality for the request, 0 to search for it. The
// header is read per request, so concurrent requests can not affect each other.
func (p *Pipeline) quality(h http.Header, fl *flow) int {
	if fl.opts.quality > 0 {
		return fl.opts.quality
	}
	v := h.Get("X-MidoPress-Quality")
	if v == "" {
		v = h.Get("X-Compy-Quality")
	}
	if q, err := strconv.Atoi(v); err == nil && q >= 1 && q <= 100 {
		return q
	}
	return p.cfg.Image.JPEGQuality
}

// hintsFrom reads how the client shows images: the client hints of the browser
// or, from the extension, X-MidoPress-Viewport as "WIDTHxDPR". The width is
// rounded up to 50 pixels so that similar screens share cached results.
func hintsFrom(h http.Header) img.Hints {
	var hints img.Hints
	width := func(v string) int {
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || n < 1 || n > 20000 {
			return 0
		}
		return int(n)
	}
	dpr := func(v string) float64 {
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || n < 0.5 || n > 8 {
			return 0
		}
		return n
	}
	if v := h.Get("X-MidoPress-Viewport"); v != "" {
		w, d, _ := strings.Cut(strings.NewReplacer(",", "x", ";", "x").Replace(v), "x")
		hints.ViewportWidth, hints.DPR = width(w), dpr(d)
	}
	if hints.ViewportWidth == 0 {
		hints.ViewportWidth = width(h.Get("Sec-CH-Viewport-Width"))
	}
	if hints.ViewportWidth == 0 {
		hints.ViewportWidth = width(h.Get("Viewport-Width"))
	}
	if hints.DPR == 0 {
		hints.DPR = dpr(h.Get("Sec-CH-DPR"))
	}
	if hints.DPR == 0 {
		hints.DPR = dpr(h.Get("DPR"))
	}
	if hints.ViewportWidth > 0 {
		hints.ViewportWidth = (hints.ViewportWidth + 49) / 50 * 50
		hints.DPR = float64(int(hints.DPR*4+0.5)) / 4
	}
	return hints
}

func imageKey(plain []byte, prof *img.Profile, caps img.Caps, hints img.Hints, fixed int) string {
	h := sha256.New()
	h.Write(plain)
	_, _ = fmt.Fprintf(h, "|%s|%v%v%v|%d|%.2f|%d", prof.Name, caps.WebP, caps.AVIF, caps.JXL, hints.ViewportWidth, hints.DPR, fixed)
	return hex.EncodeToString(h.Sum(nil))
}

// imageDetail is what handleImage tells its caller about the response.
type imageDetail struct {
	entry *xcache.Entry
	hit   bool
	plain []byte
}

func (p *Pipeline) handleImage(w http.ResponseWriter, req *http.Request, resp *http.Response, mt string, fl *flow) Outcome {
	var d imageDetail
	out := p.imageFlow(w, req, resp, mt, fl, &d)
	out.Profile = fl.prof.Name
	if out.Reason == Transformed && d.entry != nil {
		out.OutType = d.entry.ContentType
		out.Codec = strings.TrimPrefix(d.entry.ContentType, "image/")
		out.CacheHit = d.hit
		out.Metric, out.Measure = d.entry.Metric, d.entry.Measure
		if p.capture.Load() {
			out.Original, out.Result = d.plain, d.entry.Data
		}
	}
	return out
}

func (p *Pipeline) imageFlow(w http.ResponseWriter, req *http.Request, resp *http.Response, mt string, fl *flow, d *imageDetail) Outcome {
	ic := &p.cfg.Image
	declared := img.JPEG
	switch mt {
	case "image/png":
		declared = img.PNG
	case "image/gif":
		declared = img.GIF
	}
	if !p.imageEnabled(declared) {
		return p.passthrough(w, resp, PassDisabled)
	}
	if len(resp.Header["Content-Encoding"]) > 1 {
		return p.passthrough(w, resp, PassEncoding)
	}
	srcEnc, ok := codec.ParseEncoding(resp.Header.Get("Content-Encoding"))
	if !ok {
		return p.passthrough(w, resp, PassEncoding)
	}
	if resp.ContentLength > ic.MaxBytes {
		return p.passthrough(w, resp, PassTooLarge)
	}
	if resp.ContentLength >= 0 && resp.ContentLength < ic.MinBytes {
		return p.passthrough(w, resp, PassTooSmall)
	}

	expected := resp.ContentLength
	if expected < 0 {
		expected = ic.MaxBytes
	}
	weight := expected * 2
	if !p.mem.TryAcquire(weight) {
		return p.passthrough(w, resp, PassBusy)
	}
	defer p.mem.Release(weight)

	var raw bytes.Buffer
	if resp.ContentLength > 0 {
		raw.Grow(int(resp.ContentLength))
	}
	in := &errReader{r: resp.Body}
	if _, err := raw.ReadFrom(io.LimitReader(in, ic.MaxBytes+1)); err != nil {
		return Outcome{Reason: PassError, BytesIn: in.n, Err: err}
	}
	if int64(raw.Len()) > ic.MaxBytes {
		return p.passthroughBody(w, resp, prefixed(raw.Bytes(), resp.Body), PassTooLarge)
	}
	wire := raw.Bytes()

	plain := wire
	if srcEnc != codec.None {
		var decoded bytes.Buffer
		if err := p.text.Decode(srcEnc, wire, &decoded, ic.MaxBytes); err != nil {
			return p.writeBuffered(w, resp, wire, in.n, PassError)
		}
		plain = decoded.Bytes()
	}

	kind := img.Sniff(plain)
	if kind == img.Unknown || !p.imageEnabled(kind) {
		return p.writeBuffered(w, resp, wire, in.n, PassType)
	}
	width, height, err := codec.ImageSize(codecKind(kind), plain)
	if err != nil {
		return p.writeBuffered(w, resp, wire, in.n, PassError)
	}
	if int64(width)*int64(height) > ic.MaxPixels {
		return p.writeBuffered(w, resp, wire, in.n, PassTooLarge)
	}
	// decoding keeps the picture and a few copies of it
	pixelWeight := int64(width) * int64(height) * 12
	if !p.mem.TryAcquire(pixelWeight) {
		return p.writeBuffered(w, resp, wire, in.n, PassBusy)
	}
	defer p.mem.Release(pixelWeight)

	prof := fl.prof
	caps := img.CapsFromAccept(req.Header.Get("Accept"))
	var hints img.Hints
	if prof.ResizeToViewport {
		hints = hintsFrom(req.Header)
	}
	fixed := p.quality(req.Header, fl)
	key := imageKey(plain, prof, caps, hints, fixed)
	input := img.Input{Raw: plain, Format: kind, Caps: caps, Profile: prof, Hints: hints, Fixed: fixed, Budget: ic.Timeout}

	entry, ok := p.cache.Get(key)
	d.hit, d.plain = ok, plain
	if !ok {
		res, err := p.recode(req.Context(), key, input)
		switch {
		case errors.Is(err, errBusy):
			return p.writeBuffered(w, resp, wire, in.n, PassBusy)
		case errors.Is(err, img.ErrBudget):
			p.bg.submit(key, withoutBudget(input), p.expiry(resp.Header))
			return p.writeBuffered(w, resp, wire, in.n, PassBudget)
		case errors.Is(err, errTimeout), errors.Is(err, context.DeadlineExceeded):
			p.bg.submit(key, withoutBudget(input), p.expiry(resp.Header))
			return p.writeBuffered(w, resp, wire, in.n, PassTimeout)
		case errors.Is(err, img.ErrUnsupported):
			p.cache.Add(key, &xcache.Entry{Keep: true, Expires: p.expiry(resp.Header)})
			return p.writeBuffered(w, resp, wire, in.n, PassType)
		case err != nil:
			return p.writeBuffered(w, resp, wire, in.n, PassError)
		}
		entry = entryFor(res, len(plain), p.cfg.MinGain, p.expiry(resp.Header))
		if !res.Limited || !entry.Keep {
			// "the original is better" is not worth remembering when the time ran out
			p.cache.Add(key, entry)
		}
		if res.Limited {
			p.bg.submit(key, withoutBudget(input), p.expiry(resp.Header))
		}
	}
	if entry.Keep || !p.worthIt(len(entry.Data), len(wire)) {
		return p.writeBuffered(w, resp, wire, in.n, PassNotSmaller)
	}
	d.entry = entry
	return p.writeRecoded(w, resp, srcEnc, entry, prof, hints, in.n)
}

func withoutBudget(in img.Input) img.Input {
	in.Budget = 0
	return in
}

func codecKind(f img.Format) codec.ImageKind {
	switch f {
	case img.JPEG:
		return codec.KindJPEG
	case img.PNG:
		return codec.KindPNG
	case img.GIF:
		return codec.KindGIF
	}
	return codec.KindUnknown
}

func (p *Pipeline) writeRecoded(w http.ResponseWriter, resp *http.Response, srcEnc codec.Encoding, e *xcache.Entry, prof *img.Profile, hints img.Hints, bytesIn int64) Outcome {
	h := w.Header()
	httpx.CopyHeader(h, resp.Header)
	httpx.WeakenETag(h)
	httpx.DropValidators(h)
	httpx.AddVary(h, "Accept")
	httpx.AddVary(h, "Save-Data")
	if prof.ResizeToViewport {
		httpx.AddVary(h, "Sec-CH-Viewport-Width")
		httpx.AddVary(h, "Sec-CH-DPR")
	}
	if srcEnc != codec.None {
		httpx.AddVary(h, "Accept-Encoding")
		h.Del("Content-Encoding")
	}
	h.Set("Content-Type", e.ContentType)
	h.Set("Content-Length", strconv.Itoa(len(e.Data)))
	w.WriteHeader(resp.StatusCode)
	n, _ := w.Write(e.Data)
	return Outcome{Reason: Transformed, BytesIn: bytesIn, BytesOut: int64(n), Written: true}
}

// recode runs the engine for one image. Requests for the same image that
// arrive together share one run.
func (p *Pipeline) recode(ctx context.Context, key string, in img.Input) (*img.Result, error) {
	ch := p.flight.DoChan(key, func() (any, error) {
		return p.runCPU(in)
	})
	select {
	case r := <-ch:
		if r.Err != nil {
			return nil, r.Err
		}
		return r.Val.(*img.Result), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// runCPU runs the engine on a CPU slot. The engine checks its deadline between
// encodes, and an encode itself can not be interrupted, so the slot is held
// until it returns, which bounds the CPU used.
func (p *Pipeline) runCPU(in img.Input) (res *img.Result, err error) {
	if !p.acquireCPU() {
		return nil, errBusy
	}
	defer p.releaseCPU()
	defer func() {
		if r := recover(); r != nil {
			res, err = nil, fmt.Errorf("pipeline: codec panic: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.Image.Timeout)
	defer cancel()
	res, err = p.engine.Optimize(ctx, in)
	if err == nil && ctx.Err() != nil {
		if res == nil {
			return nil, errTimeout
		}
		// the time ran out in the middle of the search: what was found is the
		// best of what fitted, and a caller with more time can do better
		cp := *res
		cp.Limited = true
		res = &cp
	}
	return res, err
}
