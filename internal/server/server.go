// Package server builds the proxy from its configuration and runs it.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goastian/midopress/internal/admin"
	"github.com/goastian/midopress/internal/auth"
	"github.com/goastian/midopress/internal/codec"
	"github.com/goastian/midopress/internal/config"
	"github.com/goastian/midopress/internal/flows"
	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/magic"
	"github.com/goastian/midopress/internal/metrics"
	"github.com/goastian/midopress/internal/mitm"
	"github.com/goastian/midopress/internal/pipeline"
	"github.com/goastian/midopress/internal/proxy"
	"github.com/goastian/midopress/internal/rules"
	"github.com/goastian/midopress/internal/stats"
	"github.com/goastian/midopress/internal/sysmem"
	"github.com/goastian/midopress/internal/textmin"
	"github.com/goastian/midopress/internal/upstream"
	"github.com/goastian/midopress/internal/xcache"
)

// Option customizes a Server, mostly for tests.
type Option func(*Server)

// WithUpstream lets fn adjust the options of the outbound client.
func WithUpstream(fn func(*upstream.Options)) Option {
	return func(s *Server) { s.upstreamHook = fn }
}

// WithVersion sets the version shown in the build info metric.
func WithVersion(v string) Option { return func(s *Server) { s.version = v } }

// WithConfigLoader gives the server a way to read the configuration again.
// Reload uses it to apply what can change while running: rules, profiles, the
// default profile, minification, TTLs, the host patterns that are never
// intercepted, and the log level.
func WithConfigLoader(fn func() (*config.Config, error)) Option {
	return func(s *Server) { s.loader = fn }
}

// WithLogLevel lets Reload change the level of the logger.
func WithLogLevel(l *slog.LevelVar) Option { return func(s *Server) { s.level = l } }

// Server is a configured proxy and its listeners.
type Server struct {
	cfg *config.Config
	log *slog.Logger

	upstreamHook func(*upstream.Options)
	version      string
	loader       func() (*config.Config, error)
	level        *slog.LevelVar

	cache    *xcache.Cache
	registry *metrics.Registry
	policy   *mitm.Policy

	base      *config.Config // as it was read, before what the interface saved
	current   atomic.Pointer[config.Config]
	started   time.Time
	logs      *admin.LogHub
	collector *stats.Collector
	flows     *flows.Store
	ca        *mitm.CA
	admin     *admin.Server
	adminSrv  *http.Server
	adminLn   net.Listener
	adminTok  string
	tokenNew  bool
	cancel    context.CancelFunc
	cfgMu     sync.Mutex

	auth    *auth.Authenticator
	pipe    *pipeline.Pipeline
	proxy   *proxy.Proxy
	certs   *certReloader
	tlsConf *tls.Config

	main    *http.Server
	mitmSrv *http.Server

	ln   net.Listener
	self atomic.Pointer[selfAddrs]
}

// New builds a Server. It opens no sockets; call Listen or Run.
func New(cfg *config.Config, log *slog.Logger, opts ...Option) (*Server, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Server{cfg: cfg, started: time.Now()}
	for _, o := range opts {
		o(s)
	}
	// what the web interface saved wins over the file, and is read again on every
	// reload, so keep the configuration as it was before
	base := *cfg
	s.base = &base
	ov, err := config.LoadOverrides(cfg.Web.UIPath())
	if err != nil {
		return nil, fmt.Errorf("settings of the interface: %w", err)
	}
	cfg.ApplyOverrides(ov)
	s.current.Store(cfg)

	s.logs = admin.NewLogHub(log.Handler(), 500)
	log = slog.New(s.logs)
	s.log = log
	s.collector = stats.New(stats.Options{})
	s.flows = flows.NewStore(cfg.Web.FlowBuffer, int64(cfg.Web.CaptureBytes))

	a, err := auth.New(auth.Options{
		Mode:          auth.Mode(cfg.Auth.Mode),
		User:          cfg.Auth.User,
		Pass:          cfg.Auth.Pass,
		DevicesFile:   cfg.Auth.DevicesFile,
		BlockGlobal:   cfg.Auth.BlockGlobal,
		MaxFailures:   cfg.Auth.MaxFailures,
		FailureWindow: cfg.Auth.FailureWindow,
	})
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	s.auth = a

	var (
		issuer *mitm.Issuer
		policy *mitm.Policy
		caPEM  []byte
	)
	if cfg.MITM.Enabled() {
		ca, err := mitm.LoadCA(cfg.MITM.CACert, cfg.MITM.CAKey)
		if err != nil {
			return nil, fmt.Errorf("mitm: %w", err)
		}
		issuer = mitm.NewIssuer(ca, mitm.IssuerOptions{LeafTTL: cfg.MITM.LeafTTL, CacheSize: cfg.MITM.CacheSize})
		policy, err = mitm.NewPolicy(mitm.PolicyOptions{
			Patterns:    cfg.MITM.Passthrough,
			UseDefaults: cfg.MITM.DefaultPassthrough,
			AutoTTL:     cfg.MITM.AutoPassthroughTTL,
		})
		if err != nil {
			return nil, fmt.Errorf("mitm: %w", err)
		}
		caPEM = ca.CertPEM()
		s.ca = ca
	}

	upOpts := upstream.Options{
		DialTimeout:           cfg.Upstream.DialTimeout,
		TLSHandshakeTimeout:   cfg.Upstream.TLSHandshakeTimeout,
		ResponseHeaderTimeout: cfg.Upstream.ResponseHeaderTimeout,
		IdleConnTimeout:       cfg.Upstream.IdleConnTimeout,
		MaxIdleConnsPerHost:   cfg.Upstream.MaxIdleConnsPerHost,
		DNSCacheTTL:           cfg.Upstream.DNSCacheTTL,
		DenyPrivate:           cfg.Upstream.DenyPrivate,
		Deny: func(ap netip.AddrPort) error {
			if self := s.self.Load(); self != nil {
				return self.deny(ap)
			}
			return nil
		},
	}
	if s.upstreamHook != nil {
		s.upstreamHook(&upOpts)
	}

	s.policy = policy
	s.cache, err = xcache.Open(xcache.Options{
		MemoryBytes: int64(cfg.Cache.Memory),
		Dir:         cfg.Cache.Dir,
		DiskBytes:   int64(cfg.Cache.MaxSize),
	})
	if err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}
	pcfg, err := pipelineConfig(cfg)
	if err != nil {
		return nil, err
	}
	pcfg.Cache = s.cache
	if pcfg.Backend, err = img.NewBackend(cfg.Pipeline.Image.Backend); err != nil {
		return nil, err
	}
	s.pipe = pipeline.New(pcfg)
	var metricsHandler http.Handler
	if cfg.Metrics.Prometheus {
		s.registry = metrics.NewRegistry()
		s.registry.MustRegisterGoCollector()
		s.registry.MustRegisterProcessCollector()
		s.registry.NewBuildInfo(s.version)
		s.pipe.Instrument(s.registry)
		if cfg.Metrics.Addr == "" {
			metricsHandler = s.registry.Handler()
		}
	}
	var magicHandler http.Handler
	var isMagic func(string) bool
	if s.ca != nil && cfg.Web.MagicHost != "" {
		magicHandler, err = magic.New(magic.Options{
			Host: cfg.Web.MagicHost, CertPEM: caPEM, ProxyAddr: cfg.Listen.Addr, Version: s.version, Me: s.me,
		})
		if err != nil {
			return nil, fmt.Errorf("magic host: %w", err)
		}
		host := cfg.Web.MagicHost
		isMagic = func(h string) bool { return magic.IsMagic(h, host) }
	}
	s.proxy = proxy.New(proxy.Options{
		Observer:  &observer{s: s},
		Magic:     magicHandler,
		Discovery: http.HandlerFunc(s.discovery),
		MagicHost: isMagic,
		Metrics:   metricsHandler,
		Auth:      a,
		Upstream:  upstream.New(upOpts),
		Pipeline:  s.pipe,
		Issuer:    issuer,
		Policy:    policy,
		CACertPEM: caPEM,
		IsLocal: func(hostport string) bool {
			self := s.self.Load()
			return self != nil && self.isLocal(hostport)
		},
		Logger:           log,
		LogURLs:          cfg.Log.URLs,
		HandshakeTimeout: cfg.Server.TLSHandshakeTimeout,
	})
	if s.registry != nil {
		s.proxy.Instrument(s.registry)
	}

	if cfg.Listen.TLS.Cert != "" {
		s.certs, err = newCertReloader(cfg.Listen.TLS.Cert, cfg.Listen.TLS.Key)
		if err != nil {
			return nil, fmt.Errorf("listen.tls: %w", err)
		}
		s.tlsConf = &tls.Config{
			GetCertificate: s.certs.get,
			MinVersion:     tls.VersionTLS12,
			NextProtos:     []string{"h2", "http/1.1"},
		}
	}

	httpLog := slog.NewLogLogger(log.Handler(), slog.LevelDebug)
	newHTTP := func(h http.Handler) *http.Server {
		return &http.Server{
			Handler:           h,
			ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
			IdleTimeout:       cfg.Server.IdleTimeout,
			ErrorLog:          httpLog,
		}
	}
	s.main = newHTTP(s.proxy.Handler())
	s.mitmSrv = newHTTP(s.proxy.MITMHandler())
	s.mitmSrv.ConnContext = s.proxy.ConnContext

	if err := s.newAdmin(); err != nil {
		return nil, err
	}
	return s, nil
}

// MemoryBudget is the budget of the pipeline for a configured size: the size
// itself, or, when it is 0, a quarter of the memory of the machine or container
// within 256 MiB and 8 GiB. A picture takes 80 to 190 bytes per pixel while it is
// recoded, so the budget decides how many big ones are recoded at once.
func MemoryBudget(configured config.ByteSize) int64 {
	if configured > 0 {
		return int64(configured)
	}
	return sysmem.Budget(sysmem.Total(), 256<<20, 8<<30)
}

func pipelineConfig(cfg *config.Config) (pipeline.Config, error) {
	p := cfg.Pipeline
	profiles, err := img.BuildProfiles(cfg.Profiles)
	if err != nil {
		return pipeline.Config{}, fmt.Errorf("profiles: %w", err)
	}
	names := make([]string, 0, len(profiles))
	for n := range profiles {
		names = append(names, n)
	}
	rs, err := rules.Compile(cfg.Rules, rules.WithProfiles(names...))
	if err != nil {
		return pipeline.Config{}, fmt.Errorf("rules: %w", err)
	}
	mode := func(m string) textmin.Mode { return textmin.Mode(m) }
	return pipeline.Config{
		MinGain:       p.MinGain,
		MaxConcurrent: p.MaxConcurrentTranscodes,
		TranscodeWait: p.TranscodeWait,
		MemoryBudget:  MemoryBudget(p.MemoryBudget),
		Profile:       p.Profile,
		NoTransform:   p.NoTransform,
		Profiles:      profiles,
		Rules:         rs,
		TTL:           pipeline.TTLConfig{Min: cfg.Cache.MinTTL, Max: cfg.Cache.MaxTTL, Default: cfg.Cache.DefaultTTL},
		Levels:        codec.Levels{Brotli: p.Text.Brotli, Gzip: p.Text.Gzip, Zstd: p.Text.Zstd},
		Text: pipeline.TextConfig{
			Minify: textmin.Config{
				HTML: mode(p.Text.Minify.HTML), CSS: mode(p.Text.Minify.CSS), JS: mode(p.Text.Minify.JS),
				SVG: mode(p.Text.Minify.SVG), JSON: mode(p.Text.Minify.JSON), SkipMinified: true,
			},
			Recompress: p.Text.Recompress,
			BufferMax:  int64(p.Text.BufferMax),
			MinBytes:   int64(p.Text.MinBytes),
			MaxDecoded: int64(p.Text.MaxDecoded),
		},
		Image: pipeline.ImageConfig{
			JPEG:              p.Image.JPEG,
			JPEGQuality:       p.Image.JPEGQuality,
			PNG:               p.Image.PNG,
			GIF:               p.Image.GIF,
			Modern:            p.Image.Modern,
			PageHints:         p.Image.PageHints,
			HintDPR:           p.Image.HintDPR,
			MinBytes:          int64(p.Image.MinBytes),
			MaxBytes:          int64(p.Image.MaxBytes),
			MaxPixels:         p.Image.MaxPixels,
			Timeout:           p.Image.Timeout,
			Metric:            p.Image.Metric,
			Background:        p.Image.Background,
			BackgroundTimeout: p.Image.BackgroundTimeout,
			Warmup:            p.Image.Warmup,
		},
	}, nil
}

// CacheLen is the number of entries the cache holds in memory.
func (s *Server) CacheLen() int { return s.cache.Len() }

// Listen opens the listening socket. Run calls it when it has not been called.
func (s *Server) Listen() error {
	if s.ln != nil {
		return nil
	}
	if err := s.listenAdmin(); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", s.cfg.Listen.Addr)
	if err != nil {
		return err
	}
	s.self.Store(newSelfAddrs(ln.Addr()))
	if s.tlsConf != nil {
		ln = newTLSListener(ln, s.tlsConf, s.cfg.Server.TLSHandshakeTimeout)
	}
	s.ln = ln
	return nil
}

// Addr returns the address the server listens on, once Listen has run.
func (s *Server) Addr() net.Addr {
	if s.ln == nil {
		return nil
	}
	if tl, ok := s.ln.(*tlsListener); ok {
		return tl.Addr()
	}
	return s.ln.Addr()
}

// Proxy returns the proxy, for statistics.
func (s *Server) Proxy() *proxy.Proxy { return s.proxy }

// Reload re-reads the TLS certificate and the devices file.
func (s *Server) Reload() error {
	var errs []error
	if s.certs != nil {
		if err := s.certs.reload(); err != nil {
			errs = append(errs, fmt.Errorf("certificate: %w", err))
		}
	}
	if err := s.auth.Reload(); err != nil {
		errs = append(errs, fmt.Errorf("devices: %w", err))
	}
	if err := s.reloadConfig(); err != nil {
		errs = append(errs, fmt.Errorf("configuration: %w", err))
	}
	return errors.Join(errs...)
}

// freshConfig reads the configuration as it is on disk, and puts what the
// interface saved over it, or over the configuration the server started with
// when it has no way to read the file again.
func (s *Server) freshConfig() (*config.Config, error) {
	var cfg *config.Config
	if s.loader != nil {
		c, err := s.loader()
		if err != nil {
			return nil, err
		}
		cfg = c
	} else {
		c := *s.base
		cfg = &c
	}
	ov, err := config.LoadOverrides(cfg.Web.UIPath())
	if err != nil {
		return nil, err
	}
	cfg.ApplyOverrides(ov)
	return cfg, nil
}

// reloadConfig reads the configuration again and applies the settings that can
// change while running. It is all or nothing: a configuration that does not
// validate changes nothing.
func (s *Server) reloadConfig() error {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	cfg, err := s.freshConfig()
	if err != nil {
		return err
	}
	return s.apply(cfg)
}

// apply puts a configuration in force. The caller holds cfgMu.
func (s *Server) apply(cfg *config.Config) error {
	if err := Check(cfg); err != nil {
		return err
	}
	pcfg, err := pipelineConfig(cfg)
	if err != nil {
		return err
	}
	if s.policy != nil {
		if err := s.policy.SetPatterns(cfg.MITM.Passthrough); err != nil {
			return fmt.Errorf("mitm.passthrough: %w", err)
		}
	}
	if err := s.pipe.Reload(pcfg); err != nil {
		return err
	}
	if s.level != nil {
		var lv slog.Level
		if err := lv.UnmarshalText([]byte(cfg.Log.Level)); err == nil {
			s.level.Set(lv)
		}
	}
	s.current.Store(cfg)
	return nil
}

// Run serves until ctx is done, then shuts down gracefully: it stops
// accepting connections, lets requests and tunnels finish for up to
// server.shutdown_grace, and closes whatever is left.
func (s *Server) Run(ctx context.Context) error {
	if err := s.Listen(); err != nil {
		return err
	}
	ctx, s.cancel = context.WithCancel(ctx)
	defer s.cancel()
	if s.adminSrv != nil {
		go func() { _ = s.adminSrv.Serve(s.adminLn) }()
		defer func() { _ = s.adminSrv.Close() }()
		defer s.admin.Close()
	}
	defer s.auth.Close()
	defer s.cache.Close()
	defer s.pipe.Close()

	var metricsSrv *http.Server
	if s.registry != nil && s.cfg.Metrics.Addr != "" {
		ln, err := net.Listen("tcp", s.cfg.Metrics.Addr)
		if err != nil {
			return fmt.Errorf("metrics: %w", err)
		}
		mux := http.NewServeMux()
		mux.Handle("GET /metrics", s.registry.Handler())
		metricsSrv = &http.Server{Handler: mux, ReadHeaderTimeout: s.cfg.Server.ReadHeaderTimeout}
		go func() { _ = metricsSrv.Serve(ln) }()
		defer func() { _ = metricsSrv.Close() }()
	}

	errc := make(chan error, 2)
	go func() { errc <- s.main.Serve(s.ln) }()
	go func() { errc <- s.mitmSrv.Serve(s.proxy.MITMListener()) }()

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			_ = s.main.Close()
			_ = s.mitmSrv.Close()
			return err
		}
	}

	grace, cancel := context.WithTimeout(context.Background(), s.cfg.Server.ShutdownGrace)
	defer cancel()
	s.proxy.Stop()
	var errs []error
	for _, srv := range []*http.Server{s.main, s.mitmSrv} {
		if err := srv.Shutdown(grace); err != nil {
			_ = srv.Close()
			errs = append(errs, err)
		}
	}
	s.proxy.Wait(grace)
	return errors.Join(errs...)
}
