package upstream

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeResolver struct {
	calls atomic.Int64
	delay time.Duration
	fn    func(host string) ([]netip.Addr, error)
}

func (f *fakeResolver) LookupNetIP(ctx context.Context, _, host string) ([]netip.Addr, error) {
	f.calls.Add(1)
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.fn(host)
}

func addrs(s ...string) []netip.Addr {
	out := make([]netip.Addr, len(s))
	for i, x := range s {
		out[i] = netip.MustParseAddr(x)
	}
	return out
}

func fixed(s ...string) *fakeResolver {
	return &fakeResolver{fn: func(string) ([]netip.Addr, error) { return addrs(s...), nil }}
}

func TestDefaults(t *testing.T) {
	d := DefaultOptions()
	assert.Equal(t, 10*time.Second, d.DialTimeout)
	assert.Equal(t, 2048, d.MaxIdleConns)
	assert.Equal(t, 64, d.MaxIdleConnsPerHost)
	assert.Equal(t, 30*time.Second, d.DNSCacheTTL)

	c := New(Options{})
	tr := c.Transport()
	assert.Nil(t, tr.Proxy)
	assert.True(t, tr.ForceAttemptHTTP2)
	assert.False(t, tr.DisableCompression)
	assert.Equal(t, 2048, tr.MaxIdleConns)
	assert.Equal(t, 30*time.Second, tr.ResponseHeaderTimeout)
}

func TestDNSCacheHitMiss(t *testing.T) {
	r := fixed("203.0.113.1")
	d := newDNSCache(New(Options{Resolver: r}).opts)
	for range 5 {
		got, err := d.lookup(t.Context(), "ip", "example.test")
		require.NoError(t, err)
		assert.Equal(t, addrs("203.0.113.1"), got)
	}
	assert.EqualValues(t, 1, r.calls.Load())
	assert.EqualValues(t, 4, d.hits.Load())
	assert.EqualValues(t, 1, d.misses.Load())
}

func TestDNSCacheStats(t *testing.T) {
	r := fixed("203.0.113.1")
	c := New(Options{Resolver: r})
	_, _ = c.dns.lookup(t.Context(), "ip", "a.test")
	_, _ = c.dns.lookup(t.Context(), "ip", "a.test")
	h, m := c.DNSStats()
	assert.EqualValues(t, 1, h)
	assert.EqualValues(t, 1, m)
}

func TestDNSCacheTTLExpiry(t *testing.T) {
	r := fixed("203.0.113.1")
	c := New(Options{Resolver: r, DNSCacheTTL: 40 * time.Millisecond})
	_, err := c.dns.lookup(t.Context(), "ip", "a.test")
	require.NoError(t, err)
	time.Sleep(80 * time.Millisecond)
	_, err = c.dns.lookup(t.Context(), "ip", "a.test")
	require.NoError(t, err)
	assert.EqualValues(t, 2, r.calls.Load())
}

func TestDNSNegativeCache(t *testing.T) {
	r := &fakeResolver{fn: func(string) ([]netip.Addr, error) { return nil, errors.New("nxdomain") }}
	c := New(Options{Resolver: r, DNSNegativeTTL: 50 * time.Millisecond})
	for range 3 {
		_, err := c.dns.lookup(t.Context(), "ip", "bad.test")
		require.Error(t, err)
	}
	assert.EqualValues(t, 1, r.calls.Load())
	time.Sleep(90 * time.Millisecond)
	_, err := c.dns.lookup(t.Context(), "ip", "bad.test")
	require.Error(t, err)
	assert.EqualValues(t, 2, r.calls.Load())
}

func TestDNSEmptyResultIsError(t *testing.T) {
	c := New(Options{Resolver: fixed()})
	_, err := c.dns.lookup(t.Context(), "ip", "empty.test")
	require.Error(t, err)
}

func TestDNSCacheDisabled(t *testing.T) {
	r := fixed("203.0.113.1")
	c := New(Options{Resolver: r, DNSCacheTTL: -1})
	for range 3 {
		_, err := c.dns.lookup(t.Context(), "ip", "a.test")
		require.NoError(t, err)
	}
	assert.EqualValues(t, 3, r.calls.Load())
}

func TestDNSSingleflight(t *testing.T) {
	r := fixed("203.0.113.1")
	r.delay = 100 * time.Millisecond
	c := New(Options{Resolver: r})
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			_, err := c.dns.lookup(t.Context(), "ip", "a.test")
			assert.NoError(t, err)
		})
	}
	wg.Wait()
	assert.EqualValues(t, 1, r.calls.Load())
}

func TestDNSLookupContextCancel(t *testing.T) {
	r := fixed("203.0.113.1")
	r.delay = 2 * time.Second
	c := New(Options{Resolver: r})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.dns.lookup(ctx, "ip", "a.test")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), time.Second)
}

func TestDNSRotation(t *testing.T) {
	c := New(Options{Resolver: fixed("203.0.113.1", "203.0.113.2", "203.0.113.3")})
	var firsts []netip.Addr
	for range 6 {
		got, err := c.dns.lookup(t.Context(), "ip", "a.test")
		require.NoError(t, err)
		require.Len(t, got, 3)
		firsts = append(firsts, got[0])
	}
	assert.Equal(t, addrs("203.0.113.1", "203.0.113.2", "203.0.113.3", "203.0.113.1", "203.0.113.2", "203.0.113.3"), firsts)
}

func TestInterleave(t *testing.T) {
	in := addrs("2001:db8::1", "2001:db8::2", "203.0.113.1", "203.0.113.2", "2001:db8::3")
	want := addrs("2001:db8::1", "203.0.113.1", "2001:db8::2", "203.0.113.2", "2001:db8::3")
	assert.Equal(t, want, interleave(in))

	in = addrs("203.0.113.1", "2001:db8::1", "203.0.113.2")
	want = addrs("203.0.113.1", "2001:db8::1", "203.0.113.2")
	assert.Equal(t, want, interleave(in))

	same := addrs("203.0.113.1", "203.0.113.2", "203.0.113.3")
	assert.Equal(t, same, interleave(same))
}

func TestIsPrivate(t *testing.T) {
	for _, s := range []string{
		"127.0.0.1", "::1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.1.1", "100.64.0.1",
		"100.127.255.255", "0.0.0.0", "::", "224.0.0.1", "ff02::1", "fe80::1", "fc00::1", "::ffff:127.0.0.1",
	} {
		assert.True(t, isPrivate(netip.MustParseAddr(s)), s)
	}
	for _, s := range []string{"8.8.8.8", "203.0.113.1", "2606:4700::1111", "100.128.0.1"} {
		assert.False(t, isPrivate(netip.MustParseAddr(s)), s)
	}
}

func TestDenyPrivateLoopbackLiteral(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	c := New(Options{DenyPrivate: true})
	_, err = c.DialContext(t.Context(), "tcp", ln.Addr().String())
	require.ErrorIs(t, err, ErrBlockedAddress)

	c = New(Options{})
	conn, err := c.DialContext(t.Context(), "tcp", ln.Addr().String())
	require.NoError(t, err)
	_ = conn.Close()
}

func TestDenyPrivateRebinding(t *testing.T) {
	r := fixed("127.0.0.1", "::1", "10.0.0.1")
	c := New(Options{DenyPrivate: true, Resolver: r})
	_, err := c.DialContext(t.Context(), "tcp", "rebind.test:80")
	require.ErrorIs(t, err, ErrBlockedAddress)
	assert.EqualValues(t, 1, r.calls.Load())
}

func TestCustomDeny(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	var seen atomic.Value
	boom := errors.New("nope")
	c := New(Options{Deny: func(ap netip.AddrPort) error {
		seen.Store(ap)
		return boom
	}})
	_, err = c.DialContext(t.Context(), "tcp", ln.Addr().String())
	require.ErrorIs(t, err, ErrBlockedAddress)
	require.ErrorIs(t, err, boom)
	assert.Equal(t, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port)), seen.Load())

	c = New(Options{Deny: func(netip.AddrPort) error { return nil }})
	conn, err := c.DialContext(t.Context(), "tcp", ln.Addr().String())
	require.NoError(t, err)
	_ = conn.Close()
}

func TestBlockedThenAllowedAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	r := fixed("10.0.0.1", "127.0.0.1")
	c := New(Options{Resolver: r, Deny: func(ap netip.AddrPort) error {
		if ap.Addr().IsPrivate() {
			return errors.New("private")
		}
		return nil
	}})
	conn, err := c.DialContext(t.Context(), "tcp", fmt.Sprintf("multi.test:%d", port))
	require.NoError(t, err)
	_ = conn.Close()
}

func TestStaggeredDialFallsBackToWorkingAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	r := fixed("192.0.2.1", "127.0.0.1")
	c := New(Options{Resolver: r, DialTimeout: 5 * time.Second})
	c.dialer.Control = func(_, address string, _ syscall.RawConn) error {
		if ap := netip.MustParseAddrPort(address); ap.Addr() == netip.MustParseAddr("192.0.2.1") {
			time.Sleep(10 * time.Second)
		}
		return nil
	}
	start := time.Now()
	conn, err := c.DialContext(t.Context(), "tcp", fmt.Sprintf("slow.test:%d", port))
	require.NoError(t, err)
	_ = conn.Close()
	assert.GreaterOrEqual(t, time.Since(start), staggerDelay-20*time.Millisecond)
	assert.Less(t, time.Since(start), 3*time.Second)
}

func TestDialContextCancel(t *testing.T) {
	c := New(Options{Resolver: fixed("192.0.2.1", "192.0.2.2")})
	c.dialer.Control = func(string, string, syscall.RawConn) error {
		time.Sleep(5 * time.Second)
		return nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.DialContext(ctx, "tcp", "x.test:80")
	require.Error(t, err)
	assert.Less(t, time.Since(start), 2*time.Second)
}

func TestDialTimeout(t *testing.T) {
	c := New(Options{Resolver: fixed("192.0.2.1", "192.0.2.2"), DialTimeout: 100 * time.Millisecond})
	c.dialer.Control = func(string, string, syscall.RawConn) error {
		time.Sleep(5 * time.Second)
		return nil
	}
	start := time.Now()
	_, err := c.DialContext(t.Context(), "tcp", "x.test:80")
	require.Error(t, err)
	assert.Less(t, time.Since(start), 2*time.Second)
}

func TestDialErrors(t *testing.T) {
	c := New(Options{})
	_, err := c.DialContext(t.Context(), "udp", "127.0.0.1:80")
	require.Error(t, err)
	_, err = c.DialContext(t.Context(), "tcp", "nohostport")
	require.Error(t, err)
	_, err = c.DialContext(t.Context(), "tcp", "127.0.0.1:http")
	require.Error(t, err)
	c = New(Options{Resolver: &fakeResolver{fn: func(string) ([]netip.Addr, error) { return nil, errors.New("boom") }}})
	_, err = c.DialContext(t.Context(), "tcp", "x.test:80")
	require.Error(t, err)
}

func TestIPLiteralBypassesDNS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	r := fixed("203.0.113.1")
	c := New(Options{Resolver: r})
	conn, err := c.DialContext(t.Context(), "tcp", ln.Addr().String())
	require.NoError(t, err)
	_ = conn.Close()
	assert.Zero(t, r.calls.Load())
	h, m := c.DNSStats()
	assert.Zero(t, h+m)
}

type countingListener struct {
	net.Listener
	accepted atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}
	return conn, err
}

func newCountingServer(t *testing.T, h http.Handler, tlsMode, h2 bool) (*httptest.Server, *countingListener) {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	cl := &countingListener{Listener: srv.Listener}
	srv.Listener = cl
	srv.EnableHTTP2 = h2
	if tlsMode {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return srv, cl
}

func get(t testing.TB, c *Client, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := c.RoundTrip(req)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return resp
}

func TestConnectionReuse(t *testing.T) {
	srv, cl := newCountingServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}), false, false)
	c := New(Options{})
	defer c.CloseIdleConnections()

	for range 100 {
		get(t, c, srv.URL)
	}
	assert.EqualValues(t, 1, cl.accepted.Load())

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				get(t, c, srv.URL)
			}
		})
	}
	wg.Wait()
	assert.LessOrEqual(t, cl.accepted.Load(), int64(16))
	before := cl.accepted.Load()
	for range 50 {
		get(t, c, srv.URL)
	}
	assert.Equal(t, before, cl.accepted.Load())
}

func TestFakeHostThroughDialHookTLS(t *testing.T) {
	srv, _ := newCountingServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Host)
	}), true, false)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	c := New(Options{RootCAs: pool, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
	}})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com/", nil)
	require.NoError(t, err)
	resp, err := c.RoundTrip(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "example.com", string(body))
}

func TestUntrustedCertificateFails(t *testing.T) {
	srv, _ := newCountingServer(t, http.NotFoundHandler(), true, false)
	c := New(Options{})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := c.RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)
}

func TestHTTP2Negotiation(t *testing.T) {
	srv, cl := newCountingServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Proto)
	}), true, true)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	c := New(Options{RootCAs: pool, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
	}})
	defer c.CloseIdleConnections()
	for range 20 {
		resp := get(t, c, "https://example.com/")
		assert.Equal(t, 2, resp.ProtoMajor)
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			for range 10 {
				assert.Equal(t, 2, get(t, c, "https://example.com/").ProtoMajor)
			}
		})
	}
	wg.Wait()
	assert.LessOrEqual(t, cl.accepted.Load(), int64(3))
}

func TestResponseHeaderTimeout(t *testing.T) {
	release := make(chan struct{})
	srv, _ := newCountingServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
	}), false, false)
	defer close(release)
	c := New(Options{ResponseHeaderTimeout: 100 * time.Millisecond})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	start := time.Now()
	resp, err := c.RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout awaiting response headers")
	assert.Less(t, time.Since(start), 2*time.Second)
}

func TestRedirectsPassThrough(t *testing.T) {
	srv, _ := newCountingServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}), false, false)
	resp := get(t, New(Options{}), srv.URL)
	assert.Equal(t, http.StatusFound, resp.StatusCode)
}

func TestAcceptEncodingGzipDefault(t *testing.T) {
	var got atomic.Value
	srv, _ := newCountingServer(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("Accept-Encoding"))
	}), false, false)
	get(t, New(Options{}), srv.URL)
	assert.Equal(t, "gzip", got.Load())
}

func TestDialTLS(t *testing.T) {
	srv, _ := newCountingServer(t, http.NotFoundHandler(), true, false)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())

	c := New(Options{RootCAs: pool})
	conn, err := c.DialTLS(t.Context(), srv.Listener.Addr().String(), "example.com")
	require.NoError(t, err)
	assert.Equal(t, "http/1.1", conn.ConnectionState().NegotiatedProtocol)
	_ = conn.Close()

	_, err = c.DialTLS(t.Context(), srv.Listener.Addr().String(), "wrong.invalid")
	require.Error(t, err)

	untrusted := New(Options{})
	_, err = untrusted.DialTLS(t.Context(), srv.Listener.Addr().String(), "example.com")
	require.Error(t, err)

	guarded := New(Options{RootCAs: pool, DenyPrivate: true})
	_, err = guarded.DialTLS(t.Context(), srv.Listener.Addr().String(), "example.com")
	require.ErrorIs(t, err, ErrBlockedAddress)
}

func TestDialTLSHandshakeTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
		}
	}()
	c := New(Options{TLSHandshakeTimeout: 100 * time.Millisecond})
	start := time.Now()
	_, err = c.DialTLS(t.Context(), ln.Addr().String(), "example.com")
	require.Error(t, err)
	assert.Less(t, time.Since(start), 2*time.Second)
}

func TestConcurrencyStress(t *testing.T) {
	srv, cl := newCountingServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "payload")
	}), false, false)
	r := &fakeResolver{fn: func(string) ([]netip.Addr, error) {
		return addrs("127.0.0.1", "::1"), nil
	}}
	c := New(Options{Resolver: r, DNSCacheTTL: 20 * time.Millisecond})
	defer c.CloseIdleConnections()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	url := fmt.Sprintf("http://stress.test:%d/", port)

	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			for range 100 {
				resp := get(t, c, url)
				assert.Equal(t, 200, resp.StatusCode)
			}
		})
	}
	wg.Wait()
	assert.LessOrEqual(t, cl.accepted.Load(), int64(200))
}

func BenchmarkRoundTripParallel(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()
	c := New(Options{})
	defer c.CloseIdleConnections()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
			if err != nil {
				b.Error(err)
				return
			}
			resp, err := c.RoundTrip(req)
			if err != nil {
				b.Error(err)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	})
}
