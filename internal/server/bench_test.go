package server_test

import (
	"crypto/tls"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// The benchmarks send requests from many goroutines at once through the whole
// proxy, to a fixture server in the same process. Compare each one with
// BenchmarkDirect to see what the proxy costs.

func benchClient(h *harness, proxied bool) *http.Client {
	c := h.client(func(tr *http.Transport) {
		tr.MaxIdleConns = 1024
		tr.MaxIdleConnsPerHost = 1024
		if !proxied {
			tr.Proxy = nil
		}
	})
	return c
}

func benchGet(b *testing.B, c *http.Client, url string, headers ...string) {
	b.Helper()
	b.SetParallelism(8)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			b.Error(err)
			return
		}
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		for pb.Next() {
			resp, err := c.Do(req)
			if err != nil {
				b.Error(err)
				return
			}
			n, _ := io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			b.SetBytes(n)
		}
	})
}

func BenchmarkDirect(b *testing.B) {
	h := newHarness(b)
	benchGet(b, benchClient(h, false), h.plain.URL+"/binary")
}

func BenchmarkProxyPassthrough(b *testing.B) {
	h := newHarness(b)
	benchGet(b, benchClient(h, true), h.plainURL("/binary"))
}

func BenchmarkProxyTextGzip(b *testing.B) {
	h := newHarness(b)
	benchGet(b, benchClient(h, true), h.plainURL("/html"), "Accept-Encoding", "gzip")
}

func BenchmarkProxyTextBrotli(b *testing.B) {
	h := newHarness(b)
	benchGet(b, benchClient(h, true), h.plainURL("/html"), "Accept-Encoding", "gzip, br")
}

func BenchmarkProxyTextStreaming(b *testing.B) {
	h := newHarness(b)
	benchGet(b, benchClient(h, true), h.plainURL("/html-chunked"), "Accept-Encoding", "gzip")
}

func BenchmarkProxyAlreadyCompressed(b *testing.B) {
	h := newHarness(b)
	benchGet(b, benchClient(h, true), h.plainURL("/pregzipped"), "Accept-Encoding", "gzip")
}

func BenchmarkProxyInterceptedSmall(b *testing.B) {
	h := newHarness(b)
	benchGet(b, benchClient(h, true), h.secureURL("/status/204"))
}

func BenchmarkProxyInterceptedBrotli(b *testing.B) {
	h := newHarness(b)
	benchGet(b, benchClient(h, true), h.secureURL("/html"), "Accept-Encoding", "gzip, br")
}

func BenchmarkProxyJPEG(b *testing.B) {
	h := newHarness(b)
	benchGet(b, benchClient(h, true), h.plainURL("/image/jpeg"))
}

// BenchmarkInterceptedHandshake measures what a new tunnel costs: CONNECT, a
// TLS handshake with a certificate made on the fly, and one request.
func BenchmarkInterceptedHandshake(b *testing.B) {
	h := newHarness(b)
	b.SetParallelism(4)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			tr := &http.Transport{
				Proxy:             http.ProxyURL(h.proxyURL),
				TLSClientConfig:   &tls.Config{RootCAs: h.trustAll()},
				DisableKeepAlives: true,
			}
			resp, err := (&http.Client{Transport: tr}).Get(h.secureURL("/status/204"))
			if err != nil {
				b.Error(err)
				return
			}
			_ = resp.Body.Close()
			tr.CloseIdleConnections()
		}
	})
	require.Zero(b, h.srv.Proxy().Stats().InFlight)
}

// BenchmarkTunnel measures plain CONNECT tunnels (no interception) that carry
// one TLS handshake with the origin and one small request.
func BenchmarkTunnel(b *testing.B) {
	h := newHarness(b, withoutMITM())
	b.SetParallelism(4)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			tr := &http.Transport{
				Proxy:             http.ProxyURL(h.proxyURL),
				TLSClientConfig:   &tls.Config{RootCAs: h.originPool},
				DisableKeepAlives: true,
			}
			resp, err := (&http.Client{Transport: tr}).Get(h.secureURL("/status/204"))
			if err != nil {
				b.Error(err)
				return
			}
			_ = resp.Body.Close()
		}
	})
}
