package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/goastian/midopress/internal/admin"
	"github.com/goastian/midopress/internal/auth"
	"github.com/goastian/midopress/internal/config"
	"github.com/goastian/midopress/internal/flows"
	"github.com/goastian/midopress/internal/magic"
	"github.com/goastian/midopress/internal/pipeline"
	"github.com/goastian/midopress/internal/proxy"
	"github.com/goastian/midopress/internal/stats"
)

// Logger returns the logger to use after New: what it writes is also kept for
// the log page of the administration interface.
func (s *Server) Logger() *slog.Logger { return s.log }

// AdminAddr is where the administration server listens, nil when it is off or
// not listening yet.
func (s *Server) AdminAddr() net.Addr {
	if s.adminLn == nil {
		return nil
	}
	return s.adminLn.Addr()
}

// AdminToken is the token of the administration interface, and whether it was
// made by this start.
func (s *Server) AdminToken() (token string, created bool) { return s.adminTok, s.tokenNew }

// Stats gives the statistics, for the end of the run.
func (s *Server) Stats() *stats.Collector { return s.collector }

func clientKey(c pipeline.Client) string {
	if c.Device != "" {
		return c.Device
	}
	if c.Addr.IsValid() {
		return c.Addr.String()
	}
	return ""
}

// observer tells the statistics and the live list about what the proxy does.
type observer struct{ s *Server }

func (o *observer) Begin(c pipeline.Client) func() {
	return o.s.collector.Begin(clientKey(c))
}

func (o *observer) Observe(ob *proxy.Observation) {
	s := o.s
	out := &ob.Outcome
	key := clientKey(ob.Client)
	ev := stats.Event{
		Time: ob.Time, Kind: out.Kind, Codec: out.Codec, Profile: out.Profile, Reason: out.Reason.String(),
		Transformed: out.Reason == pipeline.Transformed, BytesIn: out.BytesIn, BytesOut: out.BytesOut,
		Duration: ob.Duration, ClientKey: key, Device: ob.Client.Device, Addr: ob.Client.Addr.String(),
		UserAgent: ob.UserAgent, CacheHit: out.CacheHit,
	}
	if s.collector.Domains() {
		ev.Host = ob.Host
	}
	s.collector.Record(ev)

	if !s.flows.Enabled() {
		return
	}
	f := flows.Flow{
		Time: ob.Time, Method: ob.Method, Scheme: ob.Scheme, Host: ob.Host, Path: ob.Path, Status: ob.Status,
		Client: flows.ClientRef{Device: ob.Client.Device, Addr: ob.Client.Addr.String()},
		Kind:   out.Kind, ContentType: out.ContentType, OutContentType: out.OutType,
		BytesIn: out.BytesIn, BytesOut: out.BytesOut, Decision: "passthrough", Reason: out.Reason.String(),
		Codec: out.Codec, Profile: out.Profile, DurationMS: float64(ob.Duration.Microseconds()) / 1000,
		CacheHit: out.CacheHit, Metric: out.Metric, Measure: out.Measure,
		RequestHeaders: flows.SafeHeaders(ob.ReqHeader), ResponseHeader: flows.SafeHeaders(ob.RespHeader),
	}
	if out.Reason == pipeline.Transformed {
		f.Decision = "transformed"
	} else {
		f.Codec = ""
	}
	var orig, res *flows.Body
	if out.Original != nil && out.Result != nil {
		orig = &flows.Body{Data: out.Original, ContentType: out.ContentType}
		res = &flows.Body{Data: out.Result, ContentType: out.OutType}
	}
	s.flows.Add(f, orig, res)
}

// me answers what the magic host says to a client about itself.
func (s *Server) me(r *http.Request) magic.Me {
	cl := pipeline.ClientFrom(r.Context())
	m := magic.Me{Device: cl.Device, Addr: cl.Addr.String()}
	profiles, def := s.pipe.Profiles()
	_ = profiles
	m.Profile = def
	if cl.Profile != "" {
		m.Profile = cl.Profile
	}
	if c, ok := s.collector.Client(clientKey(cl)); ok {
		m.Requests, m.BytesIn, m.BytesOut = int64(c.Requests), int64(c.BytesIn), int64(c.BytesOut)
		m.Saved = c.Saved
	}
	return m
}

// newAdmin makes the administration server, when it is on.
func (s *Server) newAdmin() error {
	cfg := s.current.Load()
	if !cfg.Web.Enabled {
		return nil
	}
	token, created, err := admin.LoadOrCreateToken(cfg.Web.TokenPath())
	if err != nil {
		return fmt.Errorf("admin token: %w", err)
	}
	s.adminTok, s.tokenNew = token, created

	d := admin.Deps{
		Version: s.version, Started: s.started, Token: token, Pipeline: s.pipe, Proxy: s.proxy,
		Collector: s.collector, Flows: s.flows, Logs: s.logs, Level: s.level, Registry: s.registry,
		Backend: s.pipe.Engine().Backend(), Auth: s.auth, CA: s.ca,
		Config:    func() *config.Config { return s.current.Load() },
		Overrides: func() (*config.Overrides, error) { return config.LoadOverrides(s.current.Load().Web.UIPath()) },
		Save:      s.saveOverrides,
		Reload:    s.Reload,
		Shutdown:  func() { s.stop() },
		Pprof:     cfg.Web.Pprof,
	}
	if cfg.Auth.Mode == "token" && cfg.Auth.DevicesFile != "" {
		file := cfg.Auth.DevicesFile
		d.OpenDevices = func() (*auth.Store, error) { return auth.OpenStore(file, true) }
	}
	if d.Level == nil {
		d.Level = new(slog.LevelVar)
	}
	a, err := admin.New(d)
	if err != nil {
		return err
	}
	a.SetInitialSettings(cfg.Web.Flows, cfg.Web.Domains)
	s.admin = a
	s.adminSrv = &http.Server{
		Handler:           a.Handler(),
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
	}
	return nil
}

func (s *Server) stop() {
	if s.cancel != nil {
		s.cancel()
	}
}

// listenAdmin opens the socket of the administration server.
func (s *Server) listenAdmin() error {
	if s.adminSrv == nil || s.adminLn != nil {
		return nil
	}
	ln, err := net.Listen("tcp", s.current.Load().Web.Addr)
	if err != nil {
		return fmt.Errorf("web.addr: %w", err)
	}
	s.adminLn = ln
	return nil
}

// saveOverrides checks what the interface wants to save as a whole, writes it
// and puts it in force. If anything is wrong nothing is written.
func (s *Server) saveOverrides(o *config.Overrides) error {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	var cand *config.Config
	if s.loader != nil {
		c, err := s.loader()
		if err != nil {
			return err
		}
		cand = c
	} else {
		c := *s.base
		cand = &c
	}
	cand.ApplyOverrides(o)
	if err := Check(cand); err != nil {
		return err
	}
	if _, err := pipelineConfig(cand); err != nil {
		return err
	}
	path := cand.Web.UIPath()
	prev, _ := config.LoadOverrides(path)
	if err := config.SaveOverrides(path, o); err != nil {
		return err
	}
	if err := s.apply(cand); err != nil {
		_ = config.SaveOverrides(path, prev) // put back what was there
		return err
	}
	return nil
}

// discovery is the document a client reads from the one address it was given,
// the way a DNS service hands out an endpoint: it says where the proxy is, how
// to authenticate, what the certificate is, and which files configure a browser.
func (s *Server) discovery(w http.ResponseWriter, r *http.Request) {
	cfg := s.current.Load()
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host, port = r.Host, ""
	}
	if port == "" {
		_, port, _ = net.SplitHostPort(cfg.Listen.Addr)
	}
	tls := cfg.Listen.TLS.Cert != ""
	scheme := "http"
	if tls {
		scheme = "https"
	}
	base := scheme + "://" + net.JoinHostPort(host, port)
	profiles, def := s.pipe.Profiles()
	names := make([]string, 0, len(profiles))
	for n := range profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	doc := map[string]any{
		"service": "midopress", "version": s.version,
		"proxy":    map[string]any{"host": host, "port": port, "tls": tls, "auth": cfg.Auth.Mode},
		"pac":      map[string]any{"http": base + "/proxy.pac", "full": nil},
		"https":    map[string]any{"intercept": false},
		"profiles": names, "default_profile": def,
	}
	if s.ca != nil {
		sum := sha256.Sum256(s.ca.Cert.Raw)
		doc["pac"].(map[string]any)["full"] = base + "/proxy-full.pac"
		doc["https"] = map[string]any{
			"intercept": true,
			"ca": map[string]any{
				"url": base + "/cacert", "sha256": strings.ToUpper(hex.EncodeToString(sum[:])),
				"not_after": s.ca.Cert.NotAfter.UTC().Format(time.RFC3339),
			},
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(doc)
}
