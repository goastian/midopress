package server_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/auth"
)

func (h *harness) viaUser(user, pass string) *http.Client {
	u := *h.proxyURL
	u.User = url.UserPassword(user, pass)
	return h.client(func(tr *http.Transport) { tr.Proxy = http.ProxyURL(&u) })
}

func TestBasicAuthentication(t *testing.T) {
	h := newHarness(t, withConfig(func(c *configT) { c.Auth.Mode, c.Auth.User, c.Auth.Pass = "basic", "user", "pass" }))

	t.Run("no credentials", func(t *testing.T) {
		resp, _ := h.get(h.client(), h.plainURL("/status/204"))
		assert.Equal(t, http.StatusProxyAuthRequired, resp.StatusCode)
		assert.Equal(t, `Basic realm="MidoPress"`, resp.Header.Get("Proxy-Authenticate"))
	})

	t.Run("wrong credentials", func(t *testing.T) {
		resp, _ := h.get(h.viaUser("user", "wrong"), h.plainURL("/status/204"))
		assert.Equal(t, http.StatusProxyAuthRequired, resp.StatusCode)
	})

	t.Run("right credentials, and they do not reach the origin", func(t *testing.T) {
		c := h.viaUser("user", "pass")
		resp, _ := h.get(c, h.plainURL("/status/204"))
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)

		_, body := h.get(c, h.plainURL("/headers"))
		var seen http.Header
		require.NoError(t, json.Unmarshal(body, &seen))
		assert.Empty(t, seen.Get("Proxy-Authorization"), "credentials for the proxy must not leak to the origin")
	})

	t.Run("a tunnel needs them too", func(t *testing.T) {
		conn := h.rawConn()
		_, _ = io.WriteString(conn, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusProxyAuthRequired, resp.StatusCode)

		resp, body := h.get(h.viaUser("user", "pass"), h.secureURL("/html"), "Accept-Encoding", "gzip")
		assert.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))
		assert.Equal(t, fix().html, decoded(t, resp, body))
	})

	t.Run("requests inside an intercepted tunnel are not asked again", func(t *testing.T) {
		c := h.viaUser("user", "pass")
		for range 3 {
			resp, _ := h.get(c, h.secureURL("/status/204"))
			assert.Equal(t, http.StatusNoContent, resp.StatusCode)
		}
	})

	t.Run("health check stays open", func(t *testing.T) {
		resp, err := http.Get("http://" + h.proxyAddr + "/health")
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func TestDeviceTokens(t *testing.T) {
	devices := filepath.Join(t.TempDir(), "devices.json")
	store, err := auth.OpenStore(devices, true)
	require.NoError(t, err)
	phoneToken, err := store.Add("phone", "")
	require.NoError(t, err)

	h := newHarness(t, withConfig(func(c *configT) { c.Auth.Mode, c.Auth.DevicesFile = "token", devices }))
	status := func(c *http.Client) int {
		resp, _ := h.get(c, h.plainURL("/status/204"))
		return resp.StatusCode
	}

	assert.Equal(t, http.StatusNoContent, status(h.viaUser("phone", phoneToken)))
	assert.Equal(t, http.StatusProxyAuthRequired, status(h.viaUser("phone", "not-the-token")))
	assert.Equal(t, http.StatusProxyAuthRequired, status(h.viaUser("laptop", phoneToken)))

	t.Run("bearer tokens", func(t *testing.T) {
		resp, _ := h.get(h.client(), h.plainURL("/status/204"), "Proxy-Authorization", "Bearer "+phoneToken)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	})

	t.Run("a new device works after a reload, a revoked one stops", func(t *testing.T) {
		laptopToken, err := store.Add("laptop", "")
		require.NoError(t, err)
		require.NoError(t, store.Revoke("phone"))
		require.NoError(t, h.srv.Reload())

		assert.Equal(t, http.StatusNoContent, status(h.viaUser("laptop", laptopToken)))
		assert.Equal(t, http.StatusProxyAuthRequired, status(h.viaUser("phone", phoneToken)))
	})
}

func TestFailedLoginsAreRateLimited(t *testing.T) {
	h := newHarness(t, withConfig(func(c *configT) {
		c.Auth.Mode, c.Auth.User, c.Auth.Pass = "basic", "user", "pass"
		c.Auth.MaxFailures = 3
		c.Auth.FailureWindow = time.Minute
	}))
	for range 3 {
		resp, _ := h.get(h.viaUser("user", "wrong"), h.plainURL("/status/204"))
		require.Equal(t, http.StatusProxyAuthRequired, resp.StatusCode)
	}
	resp, _ := h.get(h.viaUser("user", "pass"), h.plainURL("/status/204"))
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode, "even the right password waits until the window passes")
	retry, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	require.NoError(t, err)
	assert.Positive(t, retry)
}

func TestSlowClientsAreCutOff(t *testing.T) {
	t.Run("a connection that never sends a request", func(t *testing.T) {
		h := newHarness(t, withConfig(func(c *configT) { c.Server.ReadHeaderTimeout = 300 * time.Millisecond }))
		conn := h.rawConn()
		_, _ = io.WriteString(conn, "GET http://example.com/ HTTP/1.1\r\nHost: exam")
		start := time.Now()
		_, _ = io.ReadAll(conn)
		assert.Less(t, time.Since(start), 3*time.Second)
	})

	t.Run("a TLS client that never finishes the handshake", func(t *testing.T) {
		h := newHarness(t, withTLSProxy(), withConfig(func(c *configT) { c.Server.TLSHandshakeTimeout = 300 * time.Millisecond }))
		conn, err := net.Dial("tcp", h.proxyAddr)
		require.NoError(t, err)
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = conn.Write([]byte{0x16, 0x03, 0x01})
		start := time.Now()
		_, err = io.ReadAll(conn)
		assert.NoError(t, err, "the server closes the connection cleanly")
		assert.Less(t, time.Since(start), 3*time.Second)
	})

	t.Run("a tunnel client that opens TLS and stalls", func(t *testing.T) {
		h := newHarness(t, withConfig(func(c *configT) { c.Server.TLSHandshakeTimeout = 300 * time.Millisecond }))
		conn := h.rawConn()
		br := connect(t, conn, "example.com:443")
		_, _ = conn.Write([]byte{0x16, 0x03, 0x01})
		start := time.Now()
		_, _ = io.ReadAll(br)
		assert.Less(t, time.Since(start), 3*time.Second)
	})
}

func TestGracefulShutdown(t *testing.T) {
	t.Run("a request in flight is allowed to finish", func(t *testing.T) {
		h := newHarness(t)
		c := h.client()
		req, _ := http.NewRequest(http.MethodGet, h.plainURL("/stream"), nil)
		req.Header.Set("Accept-Encoding", "gzip")
		resp, err := c.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		h.cancel()
		select {
		case err := <-h.done:
			t.Fatalf("the server stopped with a request still in flight: %v", err)
		case <-time.After(300 * time.Millisecond):
		}

		close(h.release)
		body, err := io.ReadAll(newGzipReader(t, resp.Body))
		require.NoError(t, err)
		assert.True(t, strings.HasSuffix(string(body), "second chunk"))

		select {
		case err := <-h.done:
			assert.NoError(t, err)
			h.cancel = nil
		case <-time.After(5 * time.Second):
			t.Fatal("the server did not stop once the request finished")
		}
	})

	t.Run("an open tunnel is cut at the end of the grace period", func(t *testing.T) {
		h := newHarness(t, withoutMITM(), withConfig(func(c *configT) { c.Server.ShutdownGrace = 400 * time.Millisecond }))
		ln := echoServer(t)
		h.routes.Store("echo.test:7000", ln.Addr().String())
		conn := h.rawConn()
		br := connect(t, conn, "echo.test:7000")

		start := time.Now()
		h.cancel()
		select {
		case err := <-h.done:
			assert.NoError(t, err)
			h.cancel = nil
		case <-time.After(5 * time.Second):
			t.Fatal("shutdown did not finish")
		}
		assert.Less(t, time.Since(start), 3*time.Second)

		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, err := io.ReadAll(br)
		assert.NotErrorIs(t, err, io.ErrNoProgress)
	})

	t.Run("no new connections once stopped", func(t *testing.T) {
		h := newHarness(t)
		addr := h.proxyAddr
		h.stop()
		_, err := net.DialTimeout("tcp", addr, time.Second)
		assert.Error(t, err)
	})
}

func TestConcurrentTraffic(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	f := fix()

	const workers, rounds = 40, 15
	var wg sync.WaitGroup
	errs := make(chan string, workers*rounds)
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range rounds {
				check := func(ok bool, format string, args ...any) {
					if !ok {
						errs <- fmt.Sprintf(format, args...)
					}
				}
				switch (w + i) % 6 {
				case 0:
					resp, body, err := fetch(c, h.plainURL("/html"), "Accept-Encoding", "gzip, br")
					check(err == nil && resp.Header.Get("Content-Encoding") == "br", "html: %v", err)
					if err == nil {
						check(bytes.Equal(decodeBody(t, resp, body), f.html), "html body differs")
					}
				case 1:
					resp, body, err := fetch(c, h.secureURL("/html"), "Accept-Encoding", "gzip")
					check(err == nil && resp.Header.Get("Content-Encoding") == "gzip", "secure html: %v", err)
					if err == nil {
						check(bytes.Equal(decodeBody(t, resp, body), f.html), "secure html body differs")
					}
				case 2:
					resp, body, err := fetch(c, h.plainURL("/image/jpeg"), "X-MidoPress-Quality", strconv.Itoa(10+(w*7+i)%80))
					check(err == nil && resp.StatusCode == 200 && len(body) > 0, "jpeg: %v", err)
				case 3:
					resp, body, err := fetch(c, h.plainURL("/range"), "Range", "bytes=10-19")
					check(err == nil && resp.StatusCode == 206 && string(body) == "0123456789", "range: %v %q", err, body)
				case 4:
					resp, body, err := fetch(c, h.plainURL("/html-chunked"), "Accept-Encoding", "gzip")
					check(err == nil && bytes.Equal(decodeBody(t, resp, body), f.html), "chunked: %v", err)
				default:
					resp, _, err := fetch(c, h.plainURL("/binary"))
					check(err == nil && resp.StatusCode == 200, "binary: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}

	s := h.srv.Proxy().Stats()
	assert.GreaterOrEqual(t, s.Requests, uint64(workers*rounds))
	assert.Zero(t, s.InFlight, "nothing is left in flight")
	assert.Zero(t, s.Aborted)
}

func fetch(c *http.Client, rawURL string, headers ...string) (*http.Response, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return resp, body, err
}

func decodeBody(t *testing.T, resp *http.Response, body []byte) []byte {
	if resp.Header.Get("Content-Encoding") == "" {
		return body
	}
	return decoded(t, resp, body)
}

// TestNoGoroutineLeaks drives every kind of connection, including clients that
// give up half way, and checks that nothing is left running afterwards. A
// leak of one goroutine per request would be fatal for a busy server.
func TestNoGoroutineLeaks(t *testing.T) {
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	func() {
		h := newHarness(t, withTLSProxy())
		ln := echoServer(t)
		h.routes.Store("echo.test:7000", ln.Addr().String())

		c := h.client(func(tr *http.Transport) {
			tr.ForceAttemptHTTP2 = false
			tr.TLSClientConfig.NextProtos = []string{"http/1.1"}
		})
		for range 20 {
			h.get(c, h.plainURL("/html"), "Accept-Encoding", "gzip, br")
			h.get(c, h.secureURL("/html"), "Accept-Encoding", "gzip")
			h.get(c, h.plainURL("/image/jpeg"))
			h.get(c, h.plainURL("/html-chunked"), "Accept-Encoding", "gzip")
		}

		// clients that give up in the middle of a response
		for range 10 {
			ctx, cancel := context.WithCancel(context.Background())
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.plainURL("/stream"), nil)
			req.Header.Set("Accept-Encoding", "gzip")
			resp, err := c.Do(req)
			require.NoError(t, err)
			buf := make([]byte, 10)
			_, _ = resp.Body.Read(buf)
			cancel()
			_ = resp.Body.Close()
		}

		// tunnels, left open and closed from the client side
		for range 10 {
			conn, err := net.Dial("tcp", h.proxyAddr)
			require.NoError(t, err)
			tc := tls.Client(conn, &tls.Config{RootCAs: h.trustAll(), ServerName: "127.0.0.1"})
			require.NoError(t, tc.Handshake())
			_, _ = io.WriteString(tc, "CONNECT echo.test:7000 HTTP/1.1\r\nHost: echo.test:7000\r\n\r\n")
			_, _ = bufio.NewReader(tc).ReadString('\n')
			_ = tc.Close()
		}
		close(h.release)
		c.CloseIdleConnections()
		h.stop()
	}()

	if !assert.Eventually(t, func() bool {
		runtime.GC()
		return runtime.NumGoroutine() <= baseline+4
	}, 10*time.Second, 100*time.Millisecond, "goroutines: started with %d, now %d", baseline, runtime.NumGoroutine()) {
		var b bytes.Buffer
		_ = pprof.Lookup("goroutine").WriteTo(&b, 1)
		t.Log(b.String())
	}
}
