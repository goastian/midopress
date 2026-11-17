package server_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/goastian/midopress/internal/mitm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"
)

func TestE1CompatibilityMatrix(t *testing.T) {
	var rows []map[string]any
	defer func() {
		e1WriteReport(t, "compatibility", map[string]any{"schema": 1, "generated": time.Now().UTC(), "cases": rows,
			"scope": "isolated local origins, ephemeral test CA; browser dynamic SRI is recorded separately by e1-compatibility.mjs"})
	}()
	gate := make(chan struct{})
	defer func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	}()
	payload := bytes.Repeat([]byte("e1-range-0123456789"), 1000)
	mux := http.NewServeMux()
	mux.HandleFunc("/proto", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Origin-Protocol", r.Proto)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, "validated TLS and protocol")
	})
	mux.Handle("/ws", websocket.Handler(func(ws *websocket.Conn) {
		var message string
		if websocket.Message.Receive(ws, &message) == nil {
			_ = websocket.Message.Send(ws, message)
		}
	}))
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "data: second\n\n")
	})
	mux.HandleFunc("/range", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("ETag", `"e1-range"`)
		http.ServeContent(w, r, "clip.mp4", time.Unix(1700000000, 0), bytes.NewReader(payload))
	})
	ca, err := mitm.GenerateCA("E1 fixture origin", time.Hour)
	require.NoError(t, err)
	tlsOrigin := httptest.NewUnstartedServer(mux)
	tlsOrigin.EnableHTTP2 = true
	tlsOrigin.TLS = mitm.NewIssuer(ca, mitm.IssuerOptions{}).TLSConfig(nil)
	tlsOrigin.TLS.NextProtos = []string{"h2", "http/1.1"}
	tlsOrigin.StartTLS()
	t.Cleanup(tlsOrigin.Close)
	plainOrigin := httptest.NewServer(mux)
	t.Cleanup(plainOrigin.Close)
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	h := newHarness(t, withOrigin(tlsOrigin, pool), withConfig(func(c *configT) { c.MITM.DefaultPassthrough = false }))
	h.routes.Store("example.com:80", plainOrigin.Listener.Addr().String())
	t.Run("verified TLS and HTTP2 on both hops", func(t *testing.T) {
		resp, body := h.get(h.client(), h.secureURL("/proto"))
		assert.Equal(t, "validated TLS and protocol", string(body))
		require.NotNil(t, resp.TLS)
		assert.True(t, len(resp.TLS.VerifiedChains) > 0)
		assert.Equal(t, "HTTP/2.0", resp.Header.Get("X-Origin-Protocol"))
		assert.Equal(t, "HTTP/2.0", resp.Proto)
		rows = append(rows, map[string]any{"case": "TLS_HTTP2", "status": resp.StatusCode, "client_protocol": resp.Proto, "origin_protocol": resp.Header.Get("X-Origin-Protocol"), "tls_version": resp.TLS.Version, "verified_chain": len(resp.TLS.VerifiedChains) > 0, "fallback": "none; certificate chains verified against fixture roots"})
	})
	for _, secure := range []bool{false, true} {
		t.Run(fmt.Sprintf("real WebSocket framed echo TLS=%v", secure), func(t *testing.T) {
			conn := h.rawConn()
			require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))
			target := "http://example.com/ws"
			if secure {
				connect(t, conn, "example.com:443")
				tc := tls.Client(conn, &tls.Config{ServerName: "example.com", RootCAs: h.trustAll(), NextProtos: []string{"http/1.1"}})
				require.NoError(t, tc.Handshake())
				conn = tc
				target = "/ws"
			}
			e1WebSocketExchange(t, conn, target)
			rows = append(rows, map[string]any{"case": "WebSocket", "tls": secure, "status": 101, "framed_echo": true, "fallback": "HTTP/1.1 Upgrade; bidirectional relay; no recompression"})
		})
	}
	t.Run("SSE flushes before origin completes", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.secureURL("/events"), nil)
		require.NoError(t, err)
		req.Header.Set("Accept-Encoding", "gzip, br")
		resp, err := h.client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		first := make([]byte, len("data: first\n\n"))
		_, err = io.ReadFull(resp.Body, first)
		require.NoError(t, err)
		assert.Equal(t, "data: first\n\n", string(first))
		close(gate)
		rest, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, "data: second\n\n", string(rest))
		assert.Empty(t, resp.Header.Get("Content-Encoding"))
		rows = append(rows, map[string]any{"case": "SSE", "status": resp.StatusCode, "first_event_before_release": true, "fallback": "stream passthrough; no-store; no buffering or entity transformation"})
	})
	t.Run("range206 and IfRange full fallback", func(t *testing.T) {
		resp, body := h.get(h.client(), h.secureURL("/range"), "Range", "bytes=100-199", "If-Range", `"e1-range"`, "Accept-Encoding", "gzip, br")
		assert.Equal(t, 206, resp.StatusCode)
		assert.Equal(t, payload[100:200], body)
		assert.Equal(t, fmt.Sprintf("bytes 100-199/%d", len(payload)), resp.Header.Get("Content-Range"))
		assert.Empty(t, resp.Header.Get("Content-Encoding"))
		rows = append(rows, map[string]any{"case": "Range", "status": resp.StatusCode, "content_range": resp.Header.Get("Content-Range"), "bytes": len(body), "fallback": "exact206 passthrough; no transform"})
		resp, body = h.get(h.client(), h.secureURL("/range"), "Range", "bytes=100-199", "If-Range", `"different"`, "Accept-Encoding", "gzip, br")
		assert.Equal(t, 200, resp.StatusCode)
		assert.Equal(t, payload, body)
		assert.Empty(t, resp.Header.Get("Content-Encoding"))
		rows = append(rows, map[string]any{"case": "IfRange_miss", "status": resp.StatusCode, "bytes": len(body), "fallback": "origin returned full200; preserved because request carries Range"})
	})
	t.Run("untrusted origin TLS is refused", func(t *testing.T) {
		bad := newHarness(t, withOrigin(tlsOrigin, x509.NewCertPool()), withConfig(func(c *configT) { c.MITM.DefaultPassthrough = false }))
		resp, body := bad.get(bad.client(), bad.secureURL("/proto"))
		assert.Equal(t, 502, resp.StatusCode)
		assert.NotContains(t, string(body), "validated TLS and protocol")
		rows = append(rows, map[string]any{"case": "TLS_untrusted_origin", "status": resp.StatusCode, "fallback": "fail closed; no insecure origin retry or source-body exposure"})
	})
}

func e1WebSocketExchange(t *testing.T, conn net.Conn, target string) {
	t.Helper()
	_, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: example.com\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nOrigin: https://example.com\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", target)
	require.NoError(t, err)
	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, nil)
	require.NoError(t, err)
	require.Equal(t, 101, resp.StatusCode)
	assert.Equal(t, "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=", resp.Header.Get("Sec-WebSocket-Accept"))
	message := []byte("e1 framed echo")
	mask := []byte{1, 2, 3, 4}
	frame := append([]byte{0x81, 0x80 | byte(len(message))}, mask...)
	for i, b := range message {
		frame = append(frame, b^mask[i%4])
	}
	_, err = conn.Write(frame)
	require.NoError(t, err)
	header := make([]byte, 2)
	_, err = io.ReadFull(r, header)
	require.NoError(t, err)
	assert.Equal(t, byte(0x81), header[0])
	require.Zero(t, header[1]&0x80, "server frames are unmasked")
	got := make([]byte, int(header[1]&0x7f))
	_, err = io.ReadFull(r, got)
	require.NoError(t, err)
	assert.True(t, strings.EqualFold(string(message), string(got)))
	assert.Equal(t, message, got)
}
