package server_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// echoServer accepts connections and upper-cases what it receives. When the
// client stops sending it adds a trailer line, which proves that the reverse
// direction still works after a half-close.
func echoServer(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				data, _ := io.ReadAll(c)
				_, _ = c.Write(append(bytes.ToUpper(data), []byte("|after-eof")...))
			}()
		}
	}()
	return ln
}

func connect(t *testing.T, conn net.Conn, target string) *bufio.Reader {
	t.Helper()
	_, err := io.WriteString(conn, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	require.NoError(t, err)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "CONNECT must succeed")
	return br
}

func TestConnectTunnel(t *testing.T) {
	for name, opt := range map[string]harnessOption{
		"without interception":                 withoutMITM(),
		"non TLS traffic with interception on": func(*harness) {},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, opt)
			ln := echoServer(t)
			h.routes.Store("echo.test:7000", ln.Addr().String())

			conn := h.rawConn()
			br := connect(t, conn, "echo.test:7000")
			_, err := io.WriteString(conn, "hello through the tunnel")
			require.NoError(t, err)
			require.NoError(t, conn.(*net.TCPConn).CloseWrite())

			got, err := io.ReadAll(br)
			require.NoError(t, err)
			assert.Equal(t, "HELLO THROUGH THE TUNNEL|after-eof", string(got), "half-close must keep the other direction open")
			assert.GreaterOrEqual(t, h.srv.Proxy().Stats().Tunnels, uint64(1))
		})
	}

	t.Run("unreachable target is reported before the tunnel opens", func(t *testing.T) {
		h := newHarness(t, withoutMITM())
		h.deadHosts.Store("down.test", true)
		conn := h.rawConn()
		_, _ = io.WriteString(conn, "CONNECT down.test:443 HTTP/1.1\r\nHost: down.test:443\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	})

	t.Run("a large transfer survives the tunnel", func(t *testing.T) {
		h := newHarness(t, withoutMITM())
		ln := echoServer(t)
		h.routes.Store("echo.test:7000", ln.Addr().String())
		conn := h.rawConn()
		br := connect(t, conn, "echo.test:7000")

		payload := bytes.Repeat([]byte("abcdefghij"), 300_000)
		go func() {
			_, _ = conn.Write(payload)
			_ = conn.(*net.TCPConn).CloseWrite()
		}()
		got, err := io.ReadAll(br)
		require.NoError(t, err)
		assert.Equal(t, len(payload)+len("|after-eof"), len(got))
		assert.True(t, bytes.HasPrefix(got, bytes.ToUpper(payload[:1000])))
	})
}

func TestMITM(t *testing.T) {
	f := fix()

	t.Run("compresses HTTPS and presents a certificate from our CA", func(t *testing.T) {
		h := newHarness(t)
		resp, body := h.get(h.client(), h.secureURL("/html"), "Accept-Encoding", "gzip, br")
		assert.Equal(t, "br", resp.Header.Get("Content-Encoding"))
		assert.Equal(t, f.html, decoded(t, resp, body))

		require.NotNil(t, resp.TLS)
		leaf := resp.TLS.PeerCertificates[0]
		assert.Equal(t, "MidoPress test CA", leaf.Issuer.CommonName)
		assert.Equal(t, []string{"example.com"}, leaf.DNSNames)
		assert.Equal(t, 2, resp.ProtoMajor, "HTTP/2 is negotiated with clients that support it")
	})

	t.Run("serves HTTP/1.1 clients too", func(t *testing.T) {
		h := newHarness(t)
		c := h.client(func(tr *http.Transport) {
			tr.ForceAttemptHTTP2 = false
			tr.TLSClientConfig.NextProtos = []string{"http/1.1"}
		})
		resp, body := h.get(c, h.secureURL("/html"), "Accept-Encoding", "gzip")
		assert.Equal(t, 1, resp.ProtoMajor)
		assert.Equal(t, f.html, decoded(t, resp, body))
	})

	t.Run("images and request bodies", func(t *testing.T) {
		h := newHarness(t)
		c := h.client()
		resp, body := h.get(c, h.secureURL("/image/jpeg"))
		assert.Equal(t, "image/jpeg", resp.Header.Get("Content-Type"))
		assert.Less(t, len(body), len(f.jpeg))

		payload := strings.Repeat("secure request body ", 4000)
		resp, body = h.do(c, http.MethodPost, h.secureURL("/echo"), strings.NewReader(payload), "Content-Type", "application/x-test")
		assert.Equal(t, "POST", resp.Header.Get("X-Seen-Method"))
		assert.Equal(t, payload, string(body))
	})

	t.Run("the connection to the origin is reused", func(t *testing.T) {
		h := newHarness(t)
		c := h.client()
		for range 30 {
			resp, _ := h.get(c, h.secureURL("/status/204"))
			require.Equal(t, http.StatusNoContent, resp.StatusCode)
		}
		assert.LessOrEqual(t, h.dials.Load(), int64(2), "30 requests must not open 30 upstream connections")
	})

	t.Run("an origin with a bad certificate is not trusted on the client's behalf", func(t *testing.T) {
		h := newHarness(t)
		resp, _ := h.get(h.client(), "https://untrusted.test/html")
		assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	})

	t.Run("a different host gets its own certificate", func(t *testing.T) {
		h := newHarness(t)
		h.routes.Store("other.test:443", h.secure.Listener.Addr().String())
		resp, _ := h.get(h.client(), "https://other.test/status/204")
		// the origin certificate does not cover other.test, so this is a 502,
		// but the client must have seen a certificate for the right name first
		require.NotNil(t, resp.TLS)
		assert.Equal(t, []string{"other.test"}, resp.TLS.PeerCertificates[0].DNSNames)
	})

	t.Run("the proxy over TLS", func(t *testing.T) {
		h := newHarness(t, withTLSProxy())

		// HTTP/2 to the proxy: a plain request arrives with the target in :authority
		resp, body := h.get(h.client(), h.plainURL("/html"), "Accept-Encoding", "gzip")
		assert.Equal(t, 2, resp.ProtoMajor)
		assert.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))
		assert.Equal(t, f.html, decoded(t, resp, body))

		// Go's client speaks HTTP/1.1 for CONNECT even when ALPN says h2, so
		// pin it to HTTP/1.1 here. The h2 CONNECT path has its own test.
		c := h.client(func(tr *http.Transport) {
			tr.ForceAttemptHTTP2 = false
			tr.TLSClientConfig.NextProtos = []string{"http/1.1"}
		})
		resp, body = h.get(c, h.secureURL("/html"), "Accept-Encoding", "br")
		assert.Equal(t, "br", resp.Header.Get("Content-Encoding"))
		assert.Equal(t, f.html, decoded(t, resp, body))
	})
}

// A client that does not trust our CA rejects the certificate. That is what an
// application with a pinned certificate does, so the host must be tunnelled
// untouched from then on.
func TestAutoPassthroughForPinnedClients(t *testing.T) {
	h := newHarness(t)
	pinned := &tls.Config{RootCAs: h.originPool}
	c := func() *http.Client {
		return h.client(func(tr *http.Transport) { tr.TLSClientConfig = pinned.Clone() })
	}

	_, err := c().Get(h.secureURL("/html"))
	require.Error(t, err, "the first attempt fails: the client does not trust the CA")

	assert.Eventually(t, func() bool { return h.srv.Proxy().Stats().AutoPassthrough == 1 }, 3*time.Second, 10*time.Millisecond)

	resp, err := c().Get(h.secureURL("/html"))
	require.NoError(t, err, "the second attempt goes through untouched")
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, fix().html, body)
	assert.Empty(t, resp.Header.Get("Content-Encoding"), "nothing was recompressed")
	assert.NotEqual(t, "MidoPress test CA", resp.TLS.PeerCertificates[0].Issuer.CommonName,
		"the client talks to the real origin certificate")
	assert.Equal(t, uint64(1), h.srv.Proxy().Stats().AutoPassthrough, "marked once, not on every attempt")
}

func TestConfiguredPassthrough(t *testing.T) {
	h := newHarness(t, withConfig(func(c *configT) { c.MITM.Passthrough = []string{"~d ^example\\.com$"} }))
	c := h.client(func(tr *http.Transport) { tr.TLSClientConfig = &tls.Config{RootCAs: h.originPool} })
	resp, body := h.get(c, h.secureURL("/html"), "Accept-Encoding", "gzip, br")
	assert.Equal(t, fix().html, body)
	assert.Empty(t, resp.Header.Get("Content-Encoding"))
	assert.NotEqual(t, "MidoPress test CA", resp.TLS.PeerCertificates[0].Issuer.CommonName)
}

func TestDefaultPassthroughList(t *testing.T) {
	h := newHarness(t)
	h.routes.Store("secure-bank.example:443", h.secure.Listener.Addr().String())
	c := h.client(func(tr *http.Transport) {
		tr.TLSClientConfig = &tls.Config{RootCAs: h.originPool, ServerName: "example.com"}
	})
	resp, err := c.Get("https://secure-bank.example/status/204")
	require.NoError(t, err, "banking sites are never intercepted by default")
	_ = resp.Body.Close()
	assert.NotEqual(t, "MidoPress test CA", resp.TLS.PeerCertificates[0].Issuer.CommonName)
}

func TestWebSocket(t *testing.T) {
	handshake := "GET %s HTTP/1.1\r\nHost: example.com\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n"

	exchange := func(t *testing.T, conn net.Conn, target string) {
		t.Helper()
		_, err := io.WriteString(conn, strings.Replace(handshake, "%s", target, 1))
		require.NoError(t, err)
		br := bufio.NewReader(conn)
		resp, err := http.ReadResponse(br, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
		assert.Equal(t, "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=", resp.Header.Get("Sec-WebSocket-Accept"))
		assert.Equal(t, "websocket", strings.ToLower(resp.Header.Get("Upgrade")))

		for _, msg := range []string{"first", "second message", strings.Repeat("big ", 10000)} {
			_, err = io.WriteString(conn, msg)
			require.NoError(t, err)
			got := make([]byte, len(msg))
			_, err = io.ReadFull(br, got)
			require.NoError(t, err)
			assert.Equal(t, strings.ToUpper(msg), string(got))
		}
	}

	t.Run("through the plain proxy", func(t *testing.T) {
		h := newHarness(t)
		// a proxied request carries the absolute URI
		exchange(t, h.rawConn(), "http://example.com/ws")
	})

	t.Run("through the interception", func(t *testing.T) {
		h := newHarness(t)
		conn := h.rawConn()
		connect(t, conn, "example.com:443")
		tc := tls.Client(conn, &tls.Config{ServerName: "example.com", RootCAs: h.trustAll(), NextProtos: []string{"http/1.1"}})
		require.NoError(t, tc.Handshake())
		exchange(t, tc, "/ws")
	})

	t.Run("through a plain tunnel", func(t *testing.T) {
		h := newHarness(t, withoutMITM())
		h.routes.Store("example.com:80", h.plain.Listener.Addr().String())
		conn := h.rawConn()
		connect(t, conn, "example.com:80")
		exchange(t, conn, "/ws")
	})

	t.Run("an origin that refuses the upgrade answers normally", func(t *testing.T) {
		h := newHarness(t)
		conn := h.rawConn()
		_, err := io.WriteString(conn, "GET http://example.com/ws HTTP/1.1\r\nHost: example.com\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		require.NoError(t, err)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}

// h2Stream is one HTTP/2 stream used as a connection.
type h2Stream struct {
	r  io.ReadCloser
	w  *io.PipeWriter
	cc *http.ClientConn
}

func (s *h2Stream) Read(b []byte) (int, error)  { return s.r.Read(b) }
func (s *h2Stream) Write(b []byte) (int, error) { return s.w.Write(b) }
func (s *h2Stream) Close() error {
	_ = s.w.Close()
	_ = s.r.Close()
	return s.cc.Close()
}
func (s *h2Stream) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (s *h2Stream) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (s *h2Stream) SetDeadline(time.Time) error      { return nil }
func (s *h2Stream) SetReadDeadline(time.Time) error  { return nil }
func (s *h2Stream) SetWriteDeadline(time.Time) error { return nil }

// h2Connect opens a CONNECT tunnel over an HTTP/2 connection to the proxy,
// which is what browsers do with an HTTPS proxy.
func h2Connect(t *testing.T, h *harness, target string) *h2Stream {
	t.Helper()
	var protos http.Protocols
	protos.SetHTTP2(true)
	tr := &http.Transport{Protocols: &protos, TLSClientConfig: &tls.Config{RootCAs: h.trustAll()}}
	cc, err := tr.NewClientConn(context.Background(), "https", h.proxyAddr)
	require.NoError(t, err)

	pr, pw := io.Pipe()
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Host: target}, Host: target, Header: http.Header{}, Body: pr, ContentLength: -1}
	resp, err := cc.RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, 2, resp.ProtoMajor, "the tunnel must run over HTTP/2")
	s := &h2Stream{r: resp.Body, w: pw, cc: cc}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestConnectOverHTTP2(t *testing.T) {
	t.Run("plain tunnel", func(t *testing.T) {
		h := newHarness(t, withTLSProxy(), withoutMITM())
		ln := echoServer(t)
		h.routes.Store("echo.test:7000", ln.Addr().String())
		s := h2Connect(t, h, "echo.test:7000")
		_, err := io.WriteString(s, "over h2")
		require.NoError(t, err)
		require.NoError(t, s.w.Close())
		got, err := io.ReadAll(s.r)
		require.NoError(t, err)
		assert.Equal(t, "OVER H2|after-eof", string(got))
	})

	t.Run("interception", func(t *testing.T) {
		h := newHarness(t, withTLSProxy())
		s := h2Connect(t, h, "example.com:443")
		tc := tls.Client(s, &tls.Config{ServerName: "example.com", RootCAs: h.trustAll(), NextProtos: []string{"http/1.1"}})
		require.NoError(t, tc.Handshake())
		_, err := io.WriteString(tc, "GET /html HTTP/1.1\r\nHost: example.com\r\nAccept-Encoding: gzip\r\n\r\n")
		require.NoError(t, err)
		resp, err := http.ReadResponse(bufio.NewReader(tc), nil)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))
		assert.Equal(t, fix().html, decoded(t, resp, body))
	})
}

func TestLoopsAndPrivateTargets(t *testing.T) {
	t.Run("a name that points to the proxy is a loop, not a hang", func(t *testing.T) {
		h := newHarness(t, withRealDial(fakeResolver{"alias.test": "127.0.0.1"}))
		port := h.proxyAddr[strings.LastIndex(h.proxyAddr, ":")+1:]
		start := time.Now()
		resp, _ := h.get(h.client(), "http://alias.test:"+port+"/")
		assert.Equal(t, http.StatusLoopDetected, resp.StatusCode)
		assert.Less(t, time.Since(start), 2*time.Second)

	})

	t.Run("the same goes for a tunnel", func(t *testing.T) {
		h := newHarness(t, withoutMITM(), withRealDial(fakeResolver{"alias.test": "127.0.0.1"}))
		port := h.proxyAddr[strings.LastIndex(h.proxyAddr, ":")+1:]
		conn := h.rawConn()
		_, _ = io.WriteString(conn, "CONNECT alias.test:"+port+" HTTP/1.1\r\nHost: alias.test:"+port+"\r\n\r\n")
		cr, err := http.ReadResponse(bufio.NewReader(conn), nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusLoopDetected, cr.StatusCode)
	})

	t.Run("private addresses can be refused", func(t *testing.T) {
		h := newHarness(t, withRealDial(nil), withConfig(func(c *configT) { c.Upstream.DenyPrivate = true }))
		resp, _ := h.get(h.client(), "http://"+h.plain.Listener.Addr().String()+"/html")
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})
}

type fakeResolver map[string]string

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if ip, ok := f[host]; ok {
		return []netip.Addr{netip.MustParseAddr(ip)}, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}
