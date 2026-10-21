package server_test

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"image"
	"image/color"
	"image/color/palette"
	stdgif "image/gif"
	stdjpeg "image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"math/big"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/codec"
	"github.com/goastian/midopress/internal/config"
	"github.com/goastian/midopress/internal/mitm"
	"github.com/goastian/midopress/internal/server"
	"github.com/goastian/midopress/internal/upstream"
)

// fixtures are built once and shared by every test.
type fixtures struct {
	html, json, noise, ranged []byte
	jpeg, png, gifStatic      []byte
	gifAnimated               []byte
	gzippedHTML               []byte
	bomb                      []byte
}

var (
	fixOnce sync.Once
	fixData fixtures
)

func fix() *fixtures {
	fixOnce.Do(func() {
		fixData.html = []byte("<!DOCTYPE html>\n<html>\n  <body>\n" +
			strings.Repeat("    <p>Call me Ishmael. Some years ago, never mind how long precisely.</p>\n", 300) +
			"  </body>\n</html>\n")
		fixData.json = []byte(strings.Repeat(`{"key":"value","list":[1,2,3,4,5]},`, 200))
		rng := mrand.New(mrand.NewPCG(5, 6))
		fixData.noise = make([]byte, 6000)
		for i := range fixData.noise {
			fixData.noise[i] = byte(rng.IntN(256))
		}
		fixData.ranged = bytes.Repeat([]byte("0123456789"), 1000)

		img := image.NewRGBA(image.Rect(0, 0, 320, 240))
		for y := range 240 {
			for x := range 320 {
				n := uint8(rng.IntN(48))
				img.SetRGBA(x, y, color.RGBA{R: uint8(x*255/320) ^ n, G: uint8(y*255/240) ^ n, B: n * 3, A: 255})
			}
		}
		var b bytes.Buffer
		_ = stdjpeg.Encode(&b, img, &stdjpeg.Options{Quality: 95})
		fixData.jpeg = append([]byte(nil), b.Bytes()...)
		b.Reset()
		_ = png.Encode(&b, img)
		fixData.png = append([]byte(nil), b.Bytes()...)

		mk := func(frames int) []byte {
			g := &stdgif.GIF{}
			for f := range frames {
				p := image.NewPaletted(image.Rect(0, 0, 96, 96), palette.Plan9)
				for i := range p.Pix {
					p.Pix[i] = uint8(i/96 + f*30)
				}
				g.Image = append(g.Image, p)
				g.Delay = append(g.Delay, 5)
			}
			var gb bytes.Buffer
			_ = stdgif.EncodeAll(&gb, g)
			return gb.Bytes()
		}
		fixData.gifStatic, fixData.gifAnimated = mk(1), mk(4)

		var gz bytes.Buffer
		zw := gzip.NewWriter(&gz)
		_, _ = zw.Write(fixData.html)
		_ = zw.Close()
		fixData.gzippedHTML = gz.Bytes()

		gz.Reset()
		zw = gzip.NewWriter(&gz)
		zero := make([]byte, 1<<20)
		for range 50 {
			_, _ = zw.Write(zero)
		}
		_ = zw.Close()
		fixData.bomb = append([]byte(nil), gz.Bytes()...)
	})
	return &fixData
}

type harness struct {
	t   testing.TB
	cfg *config.Config
	srv *server.Server

	plain, secure *httptest.Server
	originPool    *x509.CertPool
	ca            *mitm.CA
	caPool        *x509.CertPool
	proxyCertPool *x509.CertPool
	proxyURL      *url.URL
	proxyAddr     string

	dials     atomic.Int64
	release   chan struct{}
	deadHosts sync.Map
	routes    sync.Map // "host:port" -> address of a backend

	realDial bool
	resolver upstream.Resolver
	logger   *slog.Logger

	cancel context.CancelFunc
	done   chan error
}

type harnessOption func(*harness)

func withConfig(fn func(*config.Config)) harnessOption {
	return func(h *harness) { fn(h.cfg) }
}

// withoutMITM leaves the CA out of the configuration.
func withoutMITM() harnessOption {
	return func(h *harness) { h.cfg.MITM.CACert, h.cfg.MITM.CAKey = "", "" }
}

func newHarness(t testing.TB, opts ...harnessOption) *harness {
	t.Helper()
	h := &harness{t: t, cfg: config.Default(), release: make(chan struct{})}
	h.cfg.Pipeline.Image.Timeout = 30 * time.Second
	h.cfg.Pipeline.Image.Warmup = false
	h.cfg.Pipeline.Text.Minify = config.Minify{HTML: "off", CSS: "off", JS: "off", SVG: "off", JSON: "off"}
	h.cfg.Listen.Addr = "127.0.0.1:0"
	h.cfg.Server.ShutdownGrace = 3 * time.Second
	h.cfg.Server.TLSHandshakeTimeout = 3 * time.Second

	dir := t.TempDir()
	ca, err := mitm.GenerateCA("MidoPress test CA", 24*time.Hour)
	require.NoError(t, err)
	h.ca = ca
	h.cfg.MITM.CACert, h.cfg.MITM.CAKey = filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	require.NoError(t, ca.WriteFiles(h.cfg.MITM.CACert, h.cfg.MITM.CAKey))
	h.caPool = x509.NewCertPool()
	h.caPool.AddCert(ca.Cert)

	mux := h.originMux()
	h.plain = httptest.NewServer(mux)
	h.secure = httptest.NewTLSServer(mux)
	t.Cleanup(h.plain.Close)
	t.Cleanup(h.secure.Close)
	h.originPool = x509.NewCertPool()
	h.originPool.AddCert(h.secure.Certificate())

	for _, o := range opts {
		o(h)
	}
	h.start(dir)
	return h
}

func (h *harness) dial(ctx context.Context, _, addr string) (net.Conn, error) {
	host, port, _ := net.SplitHostPort(addr)
	if _, dead := h.deadHosts.Load(host); dead {
		return nil, fmt.Errorf("connection to %s refused", host)
	}
	if r, ok := h.routes.Load(addr); ok {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", r.(string))
	}
	h.dials.Add(1)
	target := h.plain.Listener.Addr().String()
	if port == "443" {
		target = h.secure.Listener.Addr().String()
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", target)
}

func (h *harness) start(dir string) {
	t := h.t
	if h.cfg.Listen.TLS.Cert != "" || h.cfg.Listen.TLS.Key == "tls" {
		h.cfg.Listen.TLS.Cert, h.cfg.Listen.TLS.Key = h.makeProxyCert(dir)
	}
	log := h.logger
	if log == nil && os.Getenv("MIDOPRESS_TEST_LOG") != "" {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	srv, err := server.New(h.cfg, log, server.WithUpstream(func(o *upstream.Options) {
		o.RootCAs = h.originPool
		if !h.realDial {
			o.Dial = h.dial
		}
		if h.resolver != nil {
			o.Resolver = h.resolver
		}
	}))
	require.NoError(t, err)
	require.NoError(t, srv.Listen())
	h.srv = srv
	h.proxyAddr = srv.Addr().String()
	scheme := "http"
	if h.cfg.Listen.TLS.Cert != "" {
		scheme = "https"
	}
	h.proxyURL = &url.URL{Scheme: scheme, Host: h.proxyAddr}

	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan error, 1)
	go func() { h.done <- srv.Run(ctx) }()
	t.Cleanup(func() { h.stop() })
}

func (h *harness) stop() {
	if h.cancel == nil {
		return
	}
	h.cancel()
	select {
	case err := <-h.done:
		require.NoError(h.t, err)
	case <-time.After(10 * time.Second):
		h.t.Error("server did not stop")
	}
	h.cancel = nil
}

// makeProxyCert writes a self-signed certificate for 127.0.0.1, used by the
// proxy when clients speak TLS to it.
func (h *harness) makeProxyCert(dir string) (certPath, keyPath string) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(h.t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "proxy.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(h.t, err)
	certPath, keyPath = filepath.Join(dir, "proxy.crt"), filepath.Join(dir, "proxy.key")
	require.NoError(h.t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	kb, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(h.t, err)
	require.NoError(h.t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb}), 0o600))
	cert, err := x509.ParseCertificate(der)
	require.NoError(h.t, err)
	h.proxyCertPool = x509.NewCertPool()
	h.proxyCertPool.AddCert(cert)
	return certPath, keyPath
}

// withRealDial makes the proxy resolve and connect for real, with the given
// resolver, instead of sending everything to the fixture servers.
func withRealDial(r upstream.Resolver) harnessOption {
	return func(h *harness) { h.realDial, h.resolver = true, r }
}

// withOrigin replaces the HTTPS fixture server, for tests that bring their own.
func withOrigin(srv *httptest.Server, pool *x509.CertPool) harnessOption {
	return func(h *harness) {
		h.secure.Close()
		h.secure, h.originPool = srv, pool
	}
}

func withLogger(l *slog.Logger) harnessOption {
	return func(h *harness) { h.logger = l }
}

func withTLSProxy() harnessOption {
	return func(h *harness) { h.cfg.Listen.TLS.Key = "tls" }
}

// client returns an HTTP client that goes through the proxy, trusts the test CA
// (and the proxy certificate), keeps compressed bodies as they are and does not
// follow redirects.
func (h *harness) client(mods ...func(*http.Transport)) *http.Client {
	tr := &http.Transport{
		Proxy:              http.ProxyURL(h.proxyURL),
		DisableCompression: true,
		TLSClientConfig:    &tls.Config{RootCAs: h.trustAll()},
		ForceAttemptHTTP2:  true,
	}
	for _, m := range mods {
		m(tr)
	}
	h.t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       2 * time.Minute,
	}
}

// trustAll builds a pool with the MITM CA and the proxy certificate.
func (h *harness) trustAll() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(h.ca.Cert)
	if h.proxyCertPool != nil {
		for _, c := range h.proxyCerts() {
			pool.AddCert(c)
		}
	}
	return pool
}

func (h *harness) proxyCerts() []*x509.Certificate {
	data, err := os.ReadFile(h.cfg.Listen.TLS.Cert)
	require.NoError(h.t, err)
	block, _ := pem.Decode(data)
	c, err := x509.ParseCertificate(block.Bytes)
	require.NoError(h.t, err)
	return []*x509.Certificate{c}
}

func (h *harness) get(c *http.Client, rawURL string, headers ...string) (*http.Response, []byte) {
	h.t.Helper()
	return h.do(c, http.MethodGet, rawURL, nil, headers...)
}

func (h *harness) do(c *http.Client, method, rawURL string, body io.Reader, headers ...string) (*http.Response, []byte) {
	h.t.Helper()
	req, err := http.NewRequest(method, rawURL, body)
	require.NoError(h.t, err)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := c.Do(req)
	require.NoError(h.t, err)
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	require.NoError(h.t, err)
	return resp, data
}

// decoded returns the body after undoing its Content-Encoding.
func decoded(t testing.TB, resp *http.Response, body []byte) []byte {
	t.Helper()
	enc, ok := codec.ParseEncoding(resp.Header.Get("Content-Encoding"))
	require.True(t, ok, "unexpected Content-Encoding %q", resp.Header.Get("Content-Encoding"))
	rd, err := codec.NewText(codec.Levels{Brotli: 5, Gzip: 6, Zstd: 3}).NewReader(enc, bytes.NewReader(body))
	require.NoError(t, err)
	out, err := io.ReadAll(rd)
	require.NoError(t, err)
	require.NoError(t, rd.Close())
	return out
}

func (h *harness) plainURL(path string) string  { return "http://example.com" + path }
func (h *harness) secureURL(path string) string { return "https://example.com" + path }

// originMux is the fixture server that stands behind the proxy.
func (h *harness) originMux() http.Handler {
	f := fix()
	mux := http.NewServeMux()
	serve := func(ct string, body []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Etag", `"v1"`)
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write(body)
		}
	}
	// chunked serves the body with no Content-Length, as dynamic pages do.
	chunked := func(ct string, body []byte, extra ...string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ct)
			for i := 0; i+1 < len(extra); i += 2 {
				w.Header().Set(extra[i], extra[i+1])
			}
			half := len(body) / 2
			_, _ = w.Write(body[:half])
			w.(http.Flusher).Flush()
			_, _ = w.Write(body[half:])
		}
	}
	mux.HandleFunc("/html-chunked", chunked("text/html", f.html))
	mux.HandleFunc("/noise-chunked", chunked("text/plain", f.noise))
	mux.HandleFunc("/pregzipped-chunked", chunked("text/html", f.gzippedHTML, "Content-Encoding", "gzip"))
	mux.HandleFunc("/bomb-chunked", chunked("text/html", f.bomb, "Content-Encoding", "gzip"))
	mux.HandleFunc("/html", serve("text/html; charset=utf-8", f.html))
	mux.HandleFunc("/json", serve("application/json", f.json))
	mux.HandleFunc("/noise", serve("text/plain", f.noise))
	mux.HandleFunc("/image/jpeg", serve("image/jpeg", f.jpeg))
	mux.HandleFunc("/image/png", serve("image/png", f.png))
	mux.HandleFunc("/image/gif", serve("image/gif", f.gifStatic))
	mux.HandleFunc("/image/animated", serve("image/gif", f.gifAnimated))
	mux.HandleFunc("/binary", serve("application/octet-stream", f.noise))
	mux.HandleFunc("/status/", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/status/"))
		w.WriteHeader(n)
	})
	mux.HandleFunc("/range", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		http.ServeContent(w, r, "range.txt", time.Unix(1700000000, 0), bytes.NewReader(f.ranged))
	})
	mux.HandleFunc("/notransform", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Cache-Control", "public, no-transform")
		_, _ = w.Write(f.html)
	})
	mux.HandleFunc("/pregzipped", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", strconv.Itoa(len(f.gzippedHTML)))
		_, _ = w.Write(f.gzippedHTML)
	})
	// /encoded?enc=gzip|br|zstd|deflate serves the page in that content coding
	mux.HandleFunc("/encoded", func(w http.ResponseWriter, r *http.Request) {
		enc, ok := codec.ParseEncoding(r.URL.Query().Get("enc"))
		if !ok {
			http.Error(w, "bad enc", http.StatusBadRequest)
			return
		}
		var body []byte
		if enc == codec.Deflate {
			var b bytes.Buffer
			zw := zlib.NewWriter(&b)
			_, _ = zw.Write(f.html)
			_ = zw.Close()
			body = b.Bytes()
		} else {
			var b bytes.Buffer
			_ = codec.NewText(codec.Levels{Brotli: 5, Gzip: 6, Zstd: 3}).Compress(enc, &b, f.html)
			body = b.Bytes()
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Encoding", enc.Token())
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/bomb", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", strconv.Itoa(len(f.bomb)))
		_, _ = w.Write(f.bomb)
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/html", http.StatusFound)
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", r.Header.Get("Content-Type"))
		w.Header().Set("X-Seen-Method", r.Method)
		// HTTP/1 servers can not read the request once they started to answer.
		body, _ := io.ReadAll(r.Body)
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/headers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(r.Header)
	})
	mux.HandleFunc("/hopbyhop", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Keep-Alive", "timeout=5")
		w.Header().Set("Connection", "X-Secret, keep-alive")
		w.Header().Set("X-Secret", "do not forward")
		w.Header().Set("Proxy-Authenticate", "Basic realm=origin")
		_, _ = w.Write([]byte("hop"))
	})
	mux.HandleFunc("/trailers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "X-Check")
		_, _ = w.Write([]byte("payload"))
		w.Header().Set("X-Check", "done")
	})
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: first\n\n")
		fl.Flush()
		select {
		case <-h.release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "data: second\n\n")
	})
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, strings.Repeat("first chunk of a long page. ", 20))
		fl.Flush()
		select {
		case <-h.release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "second chunk")
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
	})
	mux.HandleFunc("/truncated", func(w http.ResponseWriter, r *http.Request) {
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		_, _ = brw.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nContent-Length: 5000\r\n\r\n")
		_, _ = brw.Write(bytes.Repeat([]byte("x"), 100))
		_ = brw.Flush()
		_ = conn.Close()
	})
	mux.HandleFunc("/truncated-chunked", func(w http.ResponseWriter, r *http.Request) {
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		// a chunked body that is cut off without the final zero-length chunk
		_, _ = brw.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nTransfer-Encoding: chunked\r\n\r\n64\r\n")
		_, _ = brw.Write(bytes.Repeat([]byte("x"), 100))
		_, _ = brw.WriteString("\r\n")
		_ = brw.Flush()
		_ = conn.Close()
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Sec-WebSocket-Key")
		if key == "" || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "not a websocket request", http.StatusBadRequest)
			return
		}
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		_, _ = fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
			base64.StdEncoding.EncodeToString(sum[:]))
		_ = brw.Flush()
		// What follows is not real WebSocket framing; the proxy only has to
		// relay bytes in both directions, so a byte echo shows that.
		buf := make([]byte, 4096)
		for {
			n, err := brw.Read(buf)
			if n > 0 {
				_, _ = conn.Write(bytes.ToUpper(buf[:n]))
			}
			if err != nil {
				return
			}
		}
	})
	return mux
}

type configT = config.Config

// rawConn opens a plain TCP connection to the proxy, closed when the test ends.
func (h *harness) rawConn() net.Conn {
	h.t.Helper()
	conn, err := net.DialTimeout("tcp", h.proxyAddr, 5*time.Second)
	require.NoError(h.t, err)
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	h.t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func newGzipReader(t testing.TB, r io.Reader) *gzip.Reader {
	t.Helper()
	zr, err := gzip.NewReader(r)
	require.NoError(t, err)
	return zr
}
