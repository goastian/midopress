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
	"sync/atomic"

	"github.com/goastian/midopress/internal/auth"
	"github.com/goastian/midopress/internal/codec"
	"github.com/goastian/midopress/internal/config"
	"github.com/goastian/midopress/internal/mitm"
	"github.com/goastian/midopress/internal/pipeline"
	"github.com/goastian/midopress/internal/proxy"
	"github.com/goastian/midopress/internal/textmin"
	"github.com/goastian/midopress/internal/upstream"
)

// Option customizes a Server, mostly for tests.
type Option func(*Server)

// WithUpstream lets fn adjust the options of the outbound client.
func WithUpstream(fn func(*upstream.Options)) Option {
	return func(s *Server) { s.upstreamHook = fn }
}

// Server is a configured proxy and its listeners.
type Server struct {
	cfg *config.Config
	log *slog.Logger

	upstreamHook func(*upstream.Options)

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
	s := &Server{cfg: cfg, log: log}
	for _, o := range opts {
		o(s)
	}

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

	s.pipe = pipeline.New(pipelineConfig(cfg))
	s.proxy = proxy.New(proxy.Options{
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
	return s, nil
}

func pipelineConfig(cfg *config.Config) pipeline.Config {
	p := cfg.Pipeline
	mode := func(m string) textmin.Mode { return textmin.Mode(m) }
	return pipeline.Config{
		MinGain:       p.MinGain,
		MaxConcurrent: p.MaxConcurrentTranscodes,
		TranscodeWait: p.TranscodeWait,
		MemoryBudget:  int64(p.MemoryBudget),
		Profile:       p.Profile,
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
			MinBytes:          int64(p.Image.MinBytes),
			MaxBytes:          int64(p.Image.MaxBytes),
			MaxPixels:         p.Image.MaxPixels,
			Timeout:           p.Image.Timeout,
			CacheSize:         int64(p.Image.CacheSize),
			Background:        p.Image.Background,
			BackgroundTimeout: p.Image.BackgroundTimeout,
			Warmup:            p.Image.Warmup,
		},
	}
}

// Listen opens the listening socket. Run calls it when it has not been called.
func (s *Server) Listen() error {
	if s.ln != nil {
		return nil
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
	return errors.Join(errs...)
}

// Run serves until ctx is done, then shuts down gracefully: it stops
// accepting connections, lets requests and tunnels finish for up to
// server.shutdown_grace, and closes whatever is left.
func (s *Server) Run(ctx context.Context) error {
	if err := s.Listen(); err != nil {
		return err
	}
	defer s.auth.Close()
	defer s.pipe.Close()

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
