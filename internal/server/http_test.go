package server_test

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForwardsPlainResponses(t *testing.T) {
	h := newHarness(t)
	c := h.client()

	t.Run("status codes and bodies are relayed", func(t *testing.T) {
		for _, code := range []int{200, 201, 204, 301, 400, 404, 418, 500, 503} {
			resp, _ := h.get(c, h.plainURL("/status/"+strconv.Itoa(code)))
			assert.Equal(t, code, resp.StatusCode)
		}
	})

	t.Run("binary content is untouched", func(t *testing.T) {
		resp, body := h.get(c, h.plainURL("/binary"), "Accept-Encoding", "gzip, br")
		assert.Equal(t, fix().noise, body)
		assert.Empty(t, resp.Header.Get("Content-Encoding"))
	})

	t.Run("redirects are passed on, not followed", func(t *testing.T) {
		resp, _ := h.get(c, h.plainURL("/redirect"))
		assert.Equal(t, http.StatusFound, resp.StatusCode)
		assert.Equal(t, "/html", resp.Header.Get("Location"))
	})

	t.Run("request bodies reach the origin", func(t *testing.T) {
		payload := strings.Repeat("a request body ", 5000)
		resp, body := h.do(c, http.MethodPost, h.plainURL("/echo"), strings.NewReader(payload), "Content-Type", "application/x-test")
		assert.Equal(t, "POST", resp.Header.Get("X-Seen-Method"))
		assert.Equal(t, payload, string(body))
	})

	t.Run("hop-by-hop headers do not reach the origin", func(t *testing.T) {
		_, body := h.get(c, h.plainURL("/headers"),
			"Connection", "X-Remove-Me, keep-alive", "X-Remove-Me", "1", "Keep-Alive", "timeout=1", "X-Keep", "yes")
		var seen http.Header
		require.NoError(t, json.Unmarshal(body, &seen))
		assert.Empty(t, seen.Get("X-Remove-Me"))
		assert.Empty(t, seen.Get("Keep-Alive"))
		assert.Empty(t, seen.Get("Proxy-Connection"))
		assert.Empty(t, seen.Get("Proxy-Authorization"))
		assert.Equal(t, "yes", seen.Get("X-Keep"))
		assert.Empty(t, seen.Get("Via"), "the proxy does not announce itself")
		assert.Empty(t, seen.Get("X-Forwarded-For"), "the client address is not leaked")
	})

	t.Run("hop-by-hop response headers do not reach the client", func(t *testing.T) {
		resp, body := h.get(c, h.plainURL("/hopbyhop"))
		assert.Equal(t, "hop", string(body))
		assert.Empty(t, resp.Header.Get("X-Secret"))
		assert.Empty(t, resp.Header.Get("Keep-Alive"))
		assert.Empty(t, resp.Header.Get("Proxy-Authenticate"))
	})

	t.Run("trailers are relayed", func(t *testing.T) {
		resp, body := h.get(c, h.plainURL("/trailers"))
		assert.Equal(t, "payload", string(body))
		assert.Equal(t, "done", resp.Trailer.Get("X-Check"))
	})

	t.Run("unsupported schemes are refused", func(t *testing.T) {
		conn := h.rawConn()
		_, _ = io.WriteString(conn, "GET ftp://example.com/file HTTP/1.1\r\nHost: example.com\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}

func TestTextCompressionThroughProxy(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	f := fix()

	tests := []struct {
		name, accept, wantEnc string
	}{
		{"gzip", "gzip", "gzip"},
		{"brotli", "gzip, deflate, br", "br"},
		{"no spaces", "gzip,deflate,br", "br"},
		{"zstd", "gzip, zstd", "zstd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := h.get(c, h.plainURL("/html"), "Accept-Encoding", tt.accept)
			assert.Equal(t, tt.wantEnc, resp.Header.Get("Content-Encoding"))
			assert.Contains(t, resp.Header.Values("Vary"), "Accept-Encoding")
			assert.Equal(t, `W/"v1"`, resp.Header.Get("Etag"))
			assert.Less(t, len(body), len(f.html)/5)
			assert.Equal(t, f.html, decoded(t, resp, body))
		})
	}

	t.Run("json", func(t *testing.T) {
		resp, body := h.get(c, h.plainURL("/json"), "Accept-Encoding", "gzip")
		assert.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))
		assert.Equal(t, f.json, decoded(t, resp, body))
	})

	t.Run("a client that accepts nothing gets the original", func(t *testing.T) {
		resp, body := h.get(c, h.plainURL("/html"))
		assert.Empty(t, resp.Header.Get("Content-Encoding"))
		assert.Equal(t, f.html, body)
	})

	t.Run("incompressible text is not made bigger", func(t *testing.T) {
		resp, body := h.get(c, h.plainURL("/noise"), "Accept-Encoding", "gzip, br")
		assert.Empty(t, resp.Header.Get("Content-Encoding"))
		assert.Equal(t, f.noise, body)
	})

	t.Run("text of unknown length is compressed as it streams", func(t *testing.T) {
		resp, body := h.get(c, h.plainURL("/html-chunked"), "Accept-Encoding", "gzip, br")
		assert.Equal(t, "br", resp.Header.Get("Content-Encoding"))
		assert.Equal(t, f.html, decoded(t, resp, body))
	})

	t.Run("incompressible text of unknown length grows only marginally", func(t *testing.T) {
		resp, body := h.get(c, h.plainURL("/noise-chunked"), "Accept-Encoding", "gzip")
		assert.LessOrEqual(t, len(body), len(f.noise)*101/100)
		assert.Equal(t, f.noise, decoded(t, resp, body))
	})

	t.Run("a gzip stream of unknown length is relayed as is", func(t *testing.T) {
		resp, body := h.get(c, h.plainURL("/pregzipped-chunked"), "Accept-Encoding", "gzip, br")
		assert.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))
		assert.Equal(t, f.gzippedHTML, body)
	})

	t.Run("a decompression bomb of unknown length is relayed at once", func(t *testing.T) {
		start := time.Now()
		resp, body := h.get(c, h.plainURL("/bomb-chunked"), "Accept-Encoding", "gzip, br")
		assert.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))
		assert.Equal(t, f.bomb, body)
		assert.Less(t, time.Since(start), 2*time.Second)
	})

	t.Run("gzip from the origin is recompressed to brotli", func(t *testing.T) {
		resp, body := h.get(c, h.plainURL("/pregzipped"), "Accept-Encoding", "gzip, br")
		assert.Equal(t, "br", resp.Header.Get("Content-Encoding"))
		assert.Equal(t, f.html, decoded(t, resp, body))
	})

	t.Run("gzip from the origin is kept when brotli is not accepted", func(t *testing.T) {
		resp, body := h.get(c, h.plainURL("/pregzipped"), "Accept-Encoding", "gzip")
		assert.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))
		assert.Equal(t, f.gzippedHTML, body)
	})

	t.Run("a decompression bomb is relayed, not expanded", func(t *testing.T) {
		resp, body := h.get(c, h.plainURL("/bomb"), "Accept-Encoding", "gzip, br")
		assert.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))
		assert.Equal(t, f.bomb, body)
	})

	t.Run("a client without Accept-Encoding does not receive gzip", func(t *testing.T) {
		resp, body := h.get(c, h.plainURL("/pregzipped"))
		assert.Empty(t, resp.Header.Get("Content-Encoding"))
		assert.Equal(t, f.html, body)
	})
}

func TestImagesThroughProxy(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	f := fix()

	t.Run("jpeg", func(t *testing.T) {
		resp, body := h.get(c, h.plainURL("/image/jpeg"))
		assert.Equal(t, "image/jpeg", resp.Header.Get("Content-Type"))
		assert.Less(t, len(body), len(f.jpeg))
		assert.Equal(t, int64(len(body)), resp.ContentLength)
	})

	t.Run("quality header", func(t *testing.T) {
		_, low := h.get(c, h.plainURL("/image/jpeg"), "X-MidoPress-Quality", "10")
		_, high := h.get(c, h.plainURL("/image/jpeg"), "X-MidoPress-Quality", "90")
		_, legacy := h.get(c, h.plainURL("/image/jpeg"), "X-Compy-Quality", "10")
		assert.Less(t, len(low), len(high))
		assert.Equal(t, len(low), len(legacy))
	})

	t.Run("webp when the browser accepts it", func(t *testing.T) {
		resp, body := h.get(c, h.plainURL("/image/jpeg"), "Accept", "image/avif,image/webp,*/*")
		assert.Equal(t, "image/webp", resp.Header.Get("Content-Type"))
		assert.Equal(t, "RIFF", string(body[:4]))
		assert.Contains(t, resp.Header.Values("Vary"), "Accept")
	})

	t.Run("png to webp", func(t *testing.T) {
		resp, body := h.get(c, h.plainURL("/image/png"), "Accept", "image/webp")
		assert.Equal(t, "image/webp", resp.Header.Get("Content-Type"))
		assert.Less(t, len(body), len(f.png))
	})

	t.Run("an animated gif is not flattened", func(t *testing.T) {
		resp, body := h.get(c, h.plainURL("/image/animated"), "Accept", "image/webp")
		assert.Equal(t, "image/gif", resp.Header.Get("Content-Type"))
		assert.Equal(t, f.gifAnimated, body)
	})
}

func TestRangeAndNoTransform(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	f := fix()

	t.Run("range requests are not transformed", func(t *testing.T) {
		resp, body := h.get(c, h.plainURL("/range"), "Range", "bytes=100-199", "Accept-Encoding", "gzip, br")
		assert.Equal(t, http.StatusPartialContent, resp.StatusCode)
		assert.Equal(t, f.ranged[100:200], body)
		assert.Equal(t, "bytes 100-199/10000", resp.Header.Get("Content-Range"))
		assert.Empty(t, resp.Header.Get("Content-Encoding"))
	})

	t.Run("a full response to a range-capable resource is still compressed", func(t *testing.T) {
		resp, body := h.get(c, h.plainURL("/range"), "Accept-Encoding", "gzip")
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, f.ranged, decoded(t, resp, body))
	})

	t.Run("no-transform is respected", func(t *testing.T) {
		resp, body := h.get(c, h.plainURL("/notransform"), "Accept-Encoding", "gzip, br")
		assert.Empty(t, resp.Header.Get("Content-Encoding"))
		assert.Equal(t, f.html, body)
		assert.Equal(t, "public, no-transform", resp.Header.Get("Cache-Control"))
	})

	t.Run("no-transform on the request is respected", func(t *testing.T) {
		resp, body := h.get(c, h.plainURL("/html"), "Accept-Encoding", "gzip", "Cache-Control", "no-transform")
		assert.Empty(t, resp.Header.Get("Content-Encoding"))
		assert.Equal(t, f.html, body)
	})

	t.Run("HEAD keeps its headers and has no body", func(t *testing.T) {
		resp, body := h.do(c, http.MethodHead, h.plainURL("/html"), nil, "Accept-Encoding", "gzip")
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Empty(t, body)
		assert.Equal(t, "text/html; charset=utf-8", resp.Header.Get("Content-Type"))
		assert.Empty(t, resp.Header.Get("Content-Encoding"))
	})

	t.Run("conditional requests keep their 304", func(t *testing.T) {
		resp, body := h.get(c, h.plainURL("/range"), "If-Modified-Since", time.Unix(1700000000, 0).UTC().Format(http.TimeFormat), "Accept-Encoding", "gzip")
		assert.Equal(t, http.StatusNotModified, resp.StatusCode)
		assert.Empty(t, body)
	})
}

func TestStreamingResponses(t *testing.T) {
	t.Run("server sent events reach the client as they happen", func(t *testing.T) {
		h := newHarness(t)
		c := h.client()
		req, _ := http.NewRequest(http.MethodGet, h.plainURL("/sse"), nil)
		req.Header.Set("Accept-Encoding", "gzip, br")
		resp, err := c.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		assert.Empty(t, resp.Header.Get("Content-Encoding"))

		br := bufio.NewReader(resp.Body)
		line, err := br.ReadString('\n')
		require.NoError(t, err)
		assert.Equal(t, "data: first\n", line, "the first event arrives while the origin is still connected")

		close(h.release)
		for {
			line, err = br.ReadString('\n')
			if strings.HasPrefix(line, "data: second") {
				break
			}
			require.NoError(t, err)
		}
	})

	t.Run("a long compressed stream is flushed progressively", func(t *testing.T) {
		h := newHarness(t)
		c := h.client()
		req, _ := http.NewRequest(http.MethodGet, h.plainURL("/stream"), nil)
		req.Header.Set("Accept-Encoding", "gzip")
		resp, err := c.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))

		zr := newGzipReader(t, resp.Body)
		first := make([]byte, 100)
		_, err = io.ReadFull(zr, first)
		require.NoError(t, err, "the first chunk must be readable before the origin finishes")
		assert.True(t, bytes.HasPrefix(first, []byte("first chunk")))

		close(h.release)
		rest, err := io.ReadAll(zr)
		require.NoError(t, err)
		assert.True(t, strings.HasSuffix(string(rest), "second chunk"))
	})
}

func TestUpstreamFailures(t *testing.T) {
	t.Run("unreachable origin", func(t *testing.T) {
		h := newHarness(t)
		h.deadHosts.Store("down.test", true)
		resp, _ := h.get(h.client(), "http://down.test/")
		assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	})

	t.Run("origin that never answers", func(t *testing.T) {
		h := newHarness(t, withConfig(func(c *configT) { c.Upstream.ResponseHeaderTimeout = 300 * time.Millisecond }))
		start := time.Now()
		resp, _ := h.get(h.client(), h.plainURL("/slow"))
		assert.Equal(t, http.StatusGatewayTimeout, resp.StatusCode)
		assert.Less(t, time.Since(start), 2*time.Second)
	})

	t.Run("a truncated body is not presented as complete", func(t *testing.T) {
		h := newHarness(t)
		req, _ := http.NewRequest(http.MethodGet, h.plainURL("/truncated"), nil)
		resp, err := h.client().Do(req)
		if err != nil {
			return // the failure was reported before any header, which is fine too
		}
		defer func() { _ = resp.Body.Close() }()
		_, err = io.ReadAll(resp.Body)
		assert.Error(t, err, "the client must see the failure, not a silently short body")
	})

	t.Run("a cut-off chunked body is not closed as if it were complete", func(t *testing.T) {
		h := newHarness(t)
		req, _ := http.NewRequest(http.MethodGet, h.plainURL("/truncated-chunked"), nil)
		resp, err := h.client().Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		_, err = io.ReadAll(resp.Body)
		assert.Error(t, err, "without the final chunk the client must see an error")
		assert.Equal(t, uint64(1), h.srv.Proxy().Stats().Aborted)
	})

	t.Run("a bad request line does not take the proxy down", func(t *testing.T) {
		h := newHarness(t)
		conn := h.rawConn()
		_, _ = io.WriteString(conn, "THIS IS NOT HTTP\r\n\r\n")
		_, _ = io.ReadAll(conn)
		resp, _ := h.get(h.client(), h.plainURL("/status/204"))
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	})
}

func TestLocalPages(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	local := "http://" + h.proxyAddr

	t.Run("stats page", func(t *testing.T) {
		h.get(c, h.plainURL("/html"), "Accept-Encoding", "gzip")
		resp, body := h.get(c, local+"/")
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Contains(t, string(body), "MidoPress")
		assert.NotContains(t, string(body), "NaN")
		assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	})

	t.Run("reachable under the other names of the machine", func(t *testing.T) {
		port := h.proxyAddr[strings.LastIndex(h.proxyAddr, ":")+1:]
		for _, host := range []string{"localhost", "127.0.0.1"} {
			resp, body := h.get(c, "http://"+host+":"+port+"/")
			assert.Equal(t, http.StatusOK, resp.StatusCode, host)
			assert.Contains(t, string(body), "MidoPress", host)
		}
	})

	t.Run("CA certificate download", func(t *testing.T) {
		resp, body := h.get(c, local+"/cacert")
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "application/x-x509-ca-cert", resp.Header.Get("Content-Type"))
		assert.Equal(t, h.ca.CertPEM(), body)
		assert.NotContains(t, string(body), "PRIVATE")
	})

	t.Run("unknown paths", func(t *testing.T) {
		resp, _ := h.get(c, local+"/fake")
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("health check needs no credentials", func(t *testing.T) {
		resp, err := http.Get("http://" + h.proxyAddr + "/health")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("a visit under any name or port is not forwarded", func(t *testing.T) {
		// what a health check or a browser does through a NAT or a published port
		for _, host := range []string{"203.0.113.9:12345", "example.com", "proxy.internal"} {
			conn := h.rawConn()
			_, _ = io.WriteString(conn, "GET /health HTTP/1.1\r\nHost: "+host+"\r\nConnection: close\r\n\r\n")
			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			require.NoError(t, err)
			body, _ := io.ReadAll(resp.Body)
			assert.Equal(t, http.StatusOK, resp.StatusCode, host)
			assert.Equal(t, "ok\n", string(body), host)

			conn = h.rawConn()
			_, _ = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: "+host+"\r\nConnection: close\r\n\r\n")
			resp, err = http.ReadResponse(bufio.NewReader(conn), nil)
			require.NoError(t, err)
			page, _ := io.ReadAll(resp.Body)
			assert.Contains(t, string(page), "MidoPress", host)
		}
	})

	t.Run("no CA without MITM", func(t *testing.T) {
		h := newHarness(t, withoutMITM())
		resp, _ := h.get(h.client(), "http://"+h.proxyAddr+"/cacert")
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

func TestEveryOriginEncoding(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	f := fix()

	tests := []struct {
		origin, accept, want string
	}{
		{"gzip", "gzip, br", "br"},
		{"zstd", "gzip, br, zstd", "br"},
		{"deflate", "gzip, deflate, br", "br"},
		{"br", "gzip, br", "br"},
		{"gzip", "gzip", "gzip"},
		{"zstd", "gzip, zstd", "zstd"},
		{"deflate", "gzip, deflate", "deflate"},
		{"br", "gzip, br, zstd", "br"},
	}
	for _, tt := range tests {
		t.Run(tt.origin+" to "+tt.want+" for "+tt.accept, func(t *testing.T) {
			resp, body := h.get(c, h.plainURL("/encoded?enc="+tt.origin), "Accept-Encoding", tt.accept)
			assert.Equal(t, tt.want, resp.Header.Get("Content-Encoding"))
			assert.Equal(t, f.html, decoded(t, resp, body), "whatever the coding, the page must be the same")
		})
	}
}

type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestLogsDoNotContainURLsUnlessAsked(t *testing.T) {
	traffic := func(h *harness) {
		c := h.client()
		h.get(c, h.plainURL("/html?secret=token123"), "Accept-Encoding", "gzip")
		h.get(c, h.secureURL("/html?private=page"), "Accept-Encoding", "gzip")
		h.deadHosts.Store("down.test", true)
		h.get(c, "http://down.test/very/private/path")
		pinned := h.client(func(tr *http.Transport) { tr.TLSClientConfig = &tls.Config{RootCAs: h.originPool} })
		_, _ = pinned.Get(h.secureURL("/pinned"))
	}
	secrets := []string{"token123", "private=page", "very/private/path", "down.test", "example.com", "/pinned"}

	t.Run("off by default, even at debug level", func(t *testing.T) {
		var logs safeBuffer
		h := newHarness(t, withLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))))
		traffic(h)
		h.stop()
		require.NotEmpty(t, logs.String(), "the debug log must have something in it")
		for _, s := range secrets {
			assert.NotContains(t, logs.String(), s)
		}
	})

	t.Run("on when asked", func(t *testing.T) {
		var logs safeBuffer
		h := newHarness(t,
			withLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))),
			withConfig(func(c *configT) { c.Log.URLs = true }))
		traffic(h)
		h.stop()
		assert.Contains(t, logs.String(), "down.test/very/private/path")
	})
}
