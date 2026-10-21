package proxy_test

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/chai2010/webp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/proxy"
	tc "github.com/goastian/midopress/internal/transcoder"
)

const (
	brotliLevel = 6
	gzipLevel   = 6
)

type testEnv struct {
	origin   *httptest.Server
	proxy    *proxy.Proxy
	proxyURL string
	client   *http.Client
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()

	srv := httptest.NewUnstartedServer(nil)
	proxyAddr := srv.Listener.Addr().String()

	p := proxy.New(proxyAddr, "")
	p.AddTranscoder("image/gif", &tc.Gif{})
	p.AddTranscoder("image/jpeg", tc.NewJpeg(50))
	p.AddTranscoder("image/png", &tc.Png{})
	p.AddTranscoder("text/html", &tc.Zip{
		Transcoder:             &tc.Identity{},
		BrotliCompressionLevel: brotliLevel,
		GzipCompressionLevel:   gzipLevel,
		SkipCompressed:         true,
	})
	srv.Config.Handler = p
	srv.Start()
	t.Cleanup(srv.Close)

	proxyURL, err := url.Parse("http://" + proxyAddr)
	require.NoError(t, err)

	return &testEnv{
		origin:   newOrigin(t),
		proxy:    p,
		proxyURL: proxyURL.String(),
		client: &http.Client{Transport: &http.Transport{
			DisableCompression: true,
			Proxy:              http.ProxyURL(proxyURL),
		}},
	}
}

func (e *testEnv) get(t *testing.T, path string, header http.Header) (*http.Response, []byte) {
	t.Helper()
	return e.getURL(t, e.origin.URL+path, header)
}

func (e *testEnv) getURL(t *testing.T, target string, header http.Header) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	require.NoError(t, err)
	maps.Copy(req.Header, header)
	resp, err := e.client.Do(req)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return resp, body
}

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestForwardsStatus(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.get(t, "/status/200", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHTMLWithoutCompression(t *testing.T) {
	e := newEnv(t)
	resp, body := e.get(t, "/html", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Content-Encoding"))
	assert.Equal(t, loadFixtures().html, body)
}

func TestGzip(t *testing.T) {
	e := newEnv(t)
	resp, body := e.get(t, "/html", hdr("Accept-Encoding", "gzip"))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))
	assert.Less(t, len(body), len(loadFixtures().html))

	r, err := gzip.NewReader(bytes.NewReader(body))
	require.NoError(t, err)
	plain, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, loadFixtures().html, plain)
}

func TestBrotli(t *testing.T) {
	e := newEnv(t)
	resp, body := e.get(t, "/html", hdr("Accept-Encoding", "br, gzip"))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "br", resp.Header.Get("Content-Encoding"))
	assert.Less(t, len(body), len(loadFixtures().html))

	plain, err := io.ReadAll(brotli.NewReader(bytes.NewReader(body)))
	require.NoError(t, err)
	assert.Equal(t, loadFixtures().html, plain)
}

func TestGif(t *testing.T) {
	e := newEnv(t)
	resp, body := e.get(t, "/image/gif", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "image/gif", resp.Header.Get("Content-Type"))

	_, err := gif.Decode(bytes.NewReader(body))
	assert.NoError(t, err)
}

func TestGifToWebP(t *testing.T) {
	e := newEnv(t)
	resp, body := e.get(t, "/image/gif", hdr("Accept", "image/webp"))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "image/webp", resp.Header.Get("Content-Type"))

	_, err := webp.Decode(bytes.NewReader(body))
	assert.NoError(t, err)
}

func TestJpeg(t *testing.T) {
	e := newEnv(t)
	resp, body := e.get(t, "/image/jpeg", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "image/jpeg", resp.Header.Get("Content-Type"))
	assert.Less(t, len(body), len(loadFixtures().jpeg))

	_, err := jpeg.Decode(bytes.NewReader(body))
	assert.NoError(t, err)
}

func TestJpegQuality(t *testing.T) {
	for _, header := range []string{"X-MidoPress-Quality", "X-Compy-Quality"} {
		t.Run(header, func(t *testing.T) {
			e := newEnv(t)

			resp, low := e.get(t, "/image/jpeg", hdr(header, "10"))
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Equal(t, "image/jpeg", resp.Header.Get("Content-Type"))

			resp, high := e.get(t, "/image/jpeg", hdr(header, "90"))
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Equal(t, "image/jpeg", resp.Header.Get("Content-Type"))

			assert.Less(t, len(low), len(high))
		})
	}
}

func TestJpegToWebP(t *testing.T) {
	e := newEnv(t)
	resp, body := e.get(t, "/image/jpeg", hdr("Accept", "image/webp,image/jpeg"))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "image/webp", resp.Header.Get("Content-Type"))

	_, err := webp.Decode(bytes.NewReader(body))
	assert.NoError(t, err)
}

func TestPng(t *testing.T) {
	e := newEnv(t)
	resp, body := e.get(t, "/image/png", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "image/png", resp.Header.Get("Content-Type"))

	_, err := png.Decode(bytes.NewReader(body))
	assert.NoError(t, err)
}

func TestPngToWebP(t *testing.T) {
	e := newEnv(t)
	resp, body := e.get(t, "/image/png", hdr("Accept", "image/webp"))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "image/webp", resp.Header.Get("Content-Type"))

	_, err := webp.Decode(bytes.NewReader(body))
	assert.NoError(t, err)
}

func TestMinify(t *testing.T) {
	e := newEnv(t)
	e.proxy.AddTranscoder("text/html", &tc.Zip{
		Transcoder:             tc.NewMinifier(),
		BrotliCompressionLevel: brotliLevel,
		GzipCompressionLevel:   gzipLevel,
		SkipCompressed:         false,
	})

	resp, body := e.get(t, "/html", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Less(t, len(body), len(loadFixtures().html))
	assert.Contains(t, string(body), "Call me Ishmael")
}

func TestAuthentication(t *testing.T) {
	e := newEnv(t)
	e.proxy.SetAuthentication("user", "pass")
	basic := func(creds string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(creds))
	}

	t.Run("no credentials", func(t *testing.T) {
		resp, _ := e.get(t, "/status/200", nil)
		assert.Equal(t, http.StatusProxyAuthRequired, resp.StatusCode)
		assert.Contains(t, resp.Header.Get("Proxy-Authenticate"), `realm="MidoPress"`)
	})

	t.Run("wrong credentials", func(t *testing.T) {
		resp, _ := e.get(t, "/status/200", hdr("Proxy-Authorization", basic("wrong:bad")))
		assert.Equal(t, http.StatusProxyAuthRequired, resp.StatusCode)
	})

	t.Run("valid credentials", func(t *testing.T) {
		resp, _ := e.get(t, "/status/200", hdr("Proxy-Authorization", basic("user:pass")))
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func TestAdmin(t *testing.T) {
	e := newEnv(t)

	tests := []struct {
		path string
		want int
	}{
		{"/", http.StatusOK},
		{"/cacert", http.StatusNotFound},
		{"/fake", http.StatusNotImplemented},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			resp, _ := e.getURL(t, e.proxyURL+tt.path, nil)
			assert.Equal(t, tt.want, resp.StatusCode)
		})
	}
}
