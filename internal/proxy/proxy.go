// Package proxy implements the HTTP proxy: request forwarding, CONNECT
// tunnels, HTTPS interception and WebSocket upgrades.
package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/goastian/midopress/internal/auth"
	"github.com/goastian/midopress/internal/httpx"
	"github.com/goastian/midopress/internal/mitm"
	"github.com/goastian/midopress/internal/pipeline"
	"github.com/goastian/midopress/internal/upstream"
)

// ErrLoop is returned when a request would be sent to the proxy itself.
var ErrLoop = errors.New("proxy: request would loop back to the proxy")

// Options configures a Proxy.
type Options struct {
	Auth     *auth.Authenticator
	Upstream *upstream.Client
	Pipeline *pipeline.Pipeline

	// Observer is told about every response and every request in flight, for the
	// statistics and the live list of the administration interface. Nil for none.
	Observer Observer

	// Magic answers the requests for MagicHost itself, which never leave the
	// proxy: the page that explains how to install the certificate.
	Magic http.Handler

	// Discovery answers /.well-known/midopress.json, which tells a client what to
	// configure from the one address it was given. It needs no credentials.
	Discovery http.Handler
	MagicHost func(host string) bool

	// Metrics serves /metrics on the proxy port, to the clients the proxy
	// accepts. Nil leaves the path unanswered.
	Metrics http.Handler

	// Issuer and Policy enable HTTPS interception. Without them every CONNECT
	// request becomes a plain tunnel.
	Issuer    *mitm.Issuer
	Policy    *mitm.Policy
	CACertPEM []byte

	// IsLocal reports whether a host:port names the proxy itself.
	IsLocal func(hostport string) bool

	Logger           *slog.Logger
	LogURLs          bool
	HandshakeTimeout time.Duration
}

// Observation describes a response that went through the proxy.
type Observation struct {
	Time      time.Time
	Method    string
	Scheme    string
	Host      string // without the port
	Path      string // with the query
	Status    int
	Client    pipeline.Client
	UserAgent string
	// ReqHeader and RespHeader are the live headers: copy what you keep.
	ReqHeader  http.Header
	RespHeader http.Header
	Outcome    pipeline.Outcome
	Duration   time.Duration
}

// Observer watches the proxy. Both methods are called on the hot path and must
// be quick.
type Observer interface {
	// Begin says a request of this client started; done says it ended.
	Begin(c pipeline.Client) (done func())
	Observe(o *Observation)
}

// Proxy is the HTTP handler. Use Handler for the listener clients connect to
// and MITMListener with MITMHandler for the connections it decrypts.
type Proxy struct {
	opts    Options
	log     *slog.Logger
	ml      *connListener
	tunnels tunnelSet
	tlsConf *tls.Config
	closing atomic.Bool

	requests     atomic.Uint64
	inflight     atomic.Int64
	bytesIn      atomic.Uint64
	bytesOut     atomic.Uint64
	upstreamErrs atomic.Uint64
	aborted      atomic.Uint64
	handshakes   atomic.Uint64
	handshakeErr atomic.Uint64
	autoMarked   atomic.Uint64
	denied       atomic.Uint64
}

// New returns a Proxy.
func New(opts Options) *Proxy {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.HandshakeTimeout <= 0 {
		opts.HandshakeTimeout = 10 * time.Second
	}
	if opts.IsLocal == nil {
		opts.IsLocal = func(string) bool { return false }
	}
	p := &Proxy{opts: opts, log: opts.Logger, ml: newConnListener()}
	if opts.Issuer != nil {
		p.tlsConf = opts.Issuer.TLSConfig(func(h *tls.ClientHelloInfo) string {
			if hc, ok := h.Conn.(*hostConn); ok {
				return hc.host
			}
			return ""
		})
	}
	return p
}

// Handler returns the handler for connections made by clients.
func (p *Proxy) Handler() http.Handler { return http.HandlerFunc(p.serve) }

// MITMHandler returns the handler for requests found inside intercepted TLS
// connections.
func (p *Proxy) MITMHandler() http.Handler { return http.HandlerFunc(p.serveMITM) }

// ConnContext is for the http.Server of intercepted connections: it gives each
// request the client that opened the tunnel it came through.
func (p *Proxy) ConnContext(ctx context.Context, c net.Conn) context.Context {
	if tc, ok := c.(*tls.Conn); ok {
		if hc, ok := tc.NetConn().(*hostConn); ok {
			cl := pipeline.Client{Device: hc.device, Profile: p.opts.Auth.DeviceProfile(hc.device)}
			if ap, err := netip.ParseAddrPort(c.RemoteAddr().String()); err == nil {
				cl.Addr = ap.Addr()
			}
			return pipeline.WithClient(ctx, cl)
		}
	}
	return ctx
}

// MITMListener returns the listener that yields the decrypted connections.
func (p *Proxy) MITMListener() net.Listener { return p.ml }

// Stop makes the proxy refuse new tunnels and stop handing connections to the
// interception server. It is the first step of a shutdown.
func (p *Proxy) Stop() {
	p.closing.Store(true)
	_ = p.ml.Close()
}

// Wait waits for the open tunnels to finish, closing them when ctx ends, and
// then drops the idle connections to origin servers. Call it after the HTTP
// servers have been shut down, so that requests that were in flight have
// returned their connections.
func (p *Proxy) Wait(ctx context.Context) {
	p.tunnels.shutdown(ctx)
	p.opts.Upstream.CloseIdleConnections()
}

// Stats is a snapshot of the proxy counters.
type Stats struct {
	Requests         uint64
	InFlight         int64
	BytesIn          uint64
	BytesOut         uint64
	UpstreamErrors   uint64
	Aborted          uint64
	Denied           uint64
	Tunnels          uint64
	TunnelsActive    int64
	TunnelBytesUp    uint64
	TunnelBytesDown  uint64
	TLSHandshakes    uint64
	TLSHandshakeErrs uint64
	AutoPassthrough  uint64
	Reasons          [pipeline.NumReasons]uint64
}

// Stats returns the current counters.
func (p *Proxy) Stats() Stats {
	return Stats{
		Requests:         p.requests.Load(),
		InFlight:         p.inflight.Load(),
		BytesIn:          p.bytesIn.Load(),
		BytesOut:         p.bytesOut.Load(),
		UpstreamErrors:   p.upstreamErrs.Load(),
		Aborted:          p.aborted.Load(),
		Denied:           p.denied.Load(),
		Tunnels:          p.tunnels.total.Load(),
		TunnelsActive:    p.tunnels.active.Load(),
		TunnelBytesUp:    p.tunnels.up.Load(),
		TunnelBytesDown:  p.tunnels.down.Load(),
		TLSHandshakes:    p.handshakes.Load(),
		TLSHandshakeErrs: p.handshakeErr.Load(),
		AutoPassthrough:  p.autoMarked.Load(),
		Reasons:          p.opts.Pipeline.Stats(),
	}
}

func (p *Proxy) serve(w http.ResponseWriter, r *http.Request) {
	// A client of an HTTP/1.1 proxy always sends the absolute URI, so a request
	// in origin form is someone visiting the proxy itself: a person with a
	// browser or a health check, who may reach it under any name or port
	// (a NAT, a published container port). Only HTTP/2 uses the origin form to
	// ask a proxy for another host, with the target in :authority.
	directVisit := !r.URL.IsAbs() && r.Method != http.MethodConnect &&
		(r.ProtoMajor == 1 || r.Host == "" || p.opts.IsLocal(r.Host))

	if directVisit && r.Method == http.MethodGet && r.URL.Path == "/health" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
		return
	}

	if directVisit && r.Method == http.MethodGet && r.URL.Path == "/.well-known/midopress.json" && p.opts.Discovery != nil {
		p.opts.Discovery.ServeHTTP(w, r)
		return
	}

	if directVisit && r.Method == http.MethodGet && (r.URL.Path == "/proxy.pac" || r.URL.Path == "/proxy-full.pac") {
		p.servePAC(w, r)
		return
	}

	d := p.opts.Auth.Check(r.RemoteAddr, r.Header.Get("Proxy-Authorization"))
	if !d.Allowed {
		p.denied.Add(1)
		switch d.Status {
		case http.StatusProxyAuthRequired:
			w.Header().Set("Proxy-Authenticate", d.Challenge)
		case http.StatusTooManyRequests:
			w.Header().Set("Retry-After", strconv.Itoa(int(d.RetryAfter.Seconds())))
		}
		w.WriteHeader(d.Status)
		return
	}

	cl := pipeline.Client{Device: d.Device, Profile: p.opts.Auth.DeviceProfile(d.Device)}
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		cl.Addr = ap.Addr()
	}
	r = r.WithContext(pipeline.WithClient(r.Context(), cl))

	switch {
	case r.Method == http.MethodConnect:
		p.handleConnect(w, r)
	case r.URL.IsAbs():
		if p.opts.IsLocal(r.URL.Host) {
			p.handleLocal(w, r)
			return
		}
		p.forward(w, r)
	case directVisit:
		p.handleLocal(w, r)
	default:
		// HTTP/2 request in origin form for another host.
		r.URL.Scheme = "http"
		r.URL.Host = r.Host
		p.forward(w, r)
	}
}

// serveMITM handles a request that arrived through an intercepted tunnel. The
// client was authenticated when it opened the tunnel.
func (p *Proxy) serveMITM(w http.ResponseWriter, r *http.Request) {
	if r.Host == "" {
		http.Error(w, "missing Host header", http.StatusBadRequest)
		return
	}
	r.URL.Scheme = "https"
	r.URL.Host = r.Host
	p.forward(w, r)
}

// forward sends the request upstream and writes the response back.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request) {
	if r.URL.Scheme != "http" && r.URL.Scheme != "https" {
		http.Error(w, "unsupported scheme", http.StatusBadRequest)
		return
	}
	if p.opts.Magic != nil && p.opts.MagicHost != nil && p.opts.MagicHost(r.URL.Hostname()) {
		p.opts.Magic.ServeHTTP(w, r)
		return
	}
	p.requests.Add(1)
	p.inflight.Add(1)
	defer p.inflight.Add(-1)
	cl := pipeline.ClientFrom(r.Context())
	if p.opts.Observer != nil {
		defer p.opts.Observer.Begin(cl)()
	}

	if httpx.IsUpgrade(r.Header) {
		p.upgrade(w, r)
		return
	}
	httpx.RemoveHopByHop(r.Header)
	r.RequestURI = ""

	resp, err := p.opts.Upstream.RoundTrip(r)
	if err != nil {
		p.upstreamError(w, r, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	httpx.RemoveHopByHop(resp.Header)

	start := time.Now()
	out := p.opts.Pipeline.Handle(w, r, resp)
	if p.opts.Observer != nil {
		p.opts.Observer.Observe(&Observation{
			Time: start, Method: r.Method, Scheme: r.URL.Scheme, Host: r.URL.Hostname(), Path: r.URL.RequestURI(),
			Status: resp.StatusCode, Client: cl, UserAgent: r.Header.Get("User-Agent"),
			ReqHeader: r.Header, RespHeader: resp.Header, Outcome: out, Duration: time.Since(start),
		})
	}
	p.bytesIn.Add(uint64(out.BytesIn))
	p.bytesOut.Add(uint64(out.BytesOut))
	if out.Err == nil {
		return
	}
	p.upstreamErrs.Add(1)
	if !out.Written {
		p.writeError(w, http.StatusBadGateway)
		return
	}
	// The response is already partly sent. Ending it normally would make the
	// client believe it is complete, so the connection is cut instead.
	p.aborted.Add(1)
	panic(http.ErrAbortHandler)
}

func (p *Proxy) upstreamError(w http.ResponseWriter, r *http.Request, err error) {
	p.upstreamErrs.Add(1)
	status := http.StatusBadGateway
	kind := "upstream"
	var ne net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return
	case errors.Is(err, ErrLoop):
		status, kind = http.StatusLoopDetected, "loop"
	case errors.Is(err, upstream.ErrBlockedAddress):
		status, kind = http.StatusForbidden, "blocked"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		status, kind = http.StatusGatewayTimeout, "timeout"
	}
	if p.log.Enabled(r.Context(), slog.LevelDebug) {
		attrs := []any{"kind", kind}
		if p.opts.LogURLs {
			attrs = append(attrs, "url", r.URL.String(), "err", err)
		}
		p.log.Debug("upstream error", attrs...)
	}
	p.writeError(w, status)
}

func (p *Proxy) writeError(w http.ResponseWriter, status int) {
	http.Error(w, fmt.Sprintf("%d %s\n", status, http.StatusText(status)), status)
}

func (p *Proxy) handleLocal(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	switch {
	case r.Method == http.MethodGet && (r.URL.Path == "" || r.URL.Path == "/"):
		h.Set("Content-Type", "text/html; charset=utf-8")
		s := p.Stats()
		ratio := "n/a"
		if s.BytesIn > 0 {
			ratio = fmt.Sprintf("%.1f%%", float64(s.BytesOut)/float64(s.BytesIn)*100)
		}
		cert := ""
		if len(p.opts.CACertPEM) > 0 {
			cert = `<li><a href="/cacert">CA certificate</a></li>`
		}
		_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><title>MidoPress</title></head>
<body>
<h1>MidoPress</h1>
<ul>
<li>bytes from origins: %d, sent to clients: %d (%s)</li>
<li>requests: %d, tunnels: %d</li>
%s
<li><a href="%s">Source code</a></li>
</ul>
</body>
</html>
`, s.BytesIn, s.BytesOut, html.EscapeString(ratio), s.Requests, s.Tunnels, cert, "https://github.com/goastian/midopress")
	case r.Method == http.MethodGet && r.URL.Path == "/metrics" && p.opts.Metrics != nil:
		p.opts.Metrics.ServeHTTP(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/cacert" && len(p.opts.CACertPEM) > 0:
		h.Set("Content-Type", "application/x-x509-ca-cert")
		h.Set("Content-Disposition", `attachment; filename="midopress-ca.crt"`)
		_, _ = w.Write(p.opts.CACertPEM)
	default:
		http.NotFound(w, r)
	}
}

var pacHost = regexp.MustCompile(`^[A-Za-z0-9.\-]+(:[0-9]{1,5})?$|^\[[0-9A-Fa-f:.]+\](:[0-9]{1,5})?$`)

// servePAC answers with a proxy auto-configuration file, so that a device is set
// up by pasting one address. It names the proxy by the host the client used to
// reach it. /proxy.pac sends only plain HTTP through the proxy, which is all it
// can shrink without a certificate; /proxy-full.pac sends HTTPS too, which only
// works on devices that trust the CA, so it exists only when there is one.
func (p *Proxy) servePAC(w http.ResponseWriter, r *http.Request) {
	full := r.URL.Path == "/proxy-full.pac"
	if full && len(p.opts.CACertPEM) == 0 {
		http.NotFound(w, r)
		return
	}
	if !pacHost.MatchString(r.Host) {
		http.Error(w, "bad host", http.StatusBadRequest)
		return
	}
	host := r.Host
	if !strings.Contains(strings.TrimPrefix(host, "["), ":") || strings.HasSuffix(host, "]") {
		host += ":80"
	}
	scheme := `url.substring(0, 5) == "http:"`
	if full {
		scheme = `url.substring(0, 5) == "http:" || url.substring(0, 6) == "https:"`
	}
	h := w.Header()
	h.Set("Content-Type", "application/x-ns-proxy-autoconfig")
	h.Set("Cache-Control", "no-cache")
	_, _ = fmt.Fprintf(w, `function FindProxyForURL(url, host) {
  if (isPlainHostName(host) || /\.local$/.test(host) || host == "localhost" ||
      /^(127\.|10\.|192\.168\.|169\.254\.|172\.(1[6-9]|2[0-9]|3[01])\.)/.test(host)) {
    return "DIRECT";
  }
  if (%s) {
    return "PROXY %s; DIRECT";
  }
  return "DIRECT";
}
`, scheme, host)
}
