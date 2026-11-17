package imageapi

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/upstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fixture(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 32, 24))))
	return b.Bytes()
}

func service(t *testing.T) *Service {
	t.Helper()
	s, err := New(Options{Key: bytes.Repeat([]byte{42}, 32), PublicURL: "https://images.example.com",
		MaxBytes: 1 << 20, MaxPixels: 100_000, MaxWidth: 4096, MaxDPR: 3, Concurrency: 1,
		Timeout: time.Second, SignatureTTL: time.Minute, CacheTTL: time.Minute, Backend: "wasm",
		Profiles: img.Profiles, Transform: Process(os.Args[0])})
	require.NoError(t, err)
	raw := fixture(t)
	s.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(raw)), ContentLength: int64(len(raw)), Request: r}, nil
	})
	t.Cleanup(s.Close)
	return s
}

func signed(t *testing.T, s *Service, mode string) string {
	t.Helper()
	u, _, err := s.Sign(Request{URL: "https://public.example.com/photo.png", Profile: "lossless", Mode: mode})
	require.NoError(t, err)
	return u
}

func perform(s *Service, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	return w
}

func TestSignaturesProtectEveryParameterAndCache(t *testing.T) {
	s := service(t)
	u := signed(t, s, "original")
	assert.Equal(t, 200, perform(s, u).Code)
	assert.Equal(t, "hit", perform(s, u).Header().Get("X-MidoPress-Cache"))
	for _, key := range []string{"url", "v", "profile", "format", "w", "dpr", "mode", "exp", "sig"} {
		t.Run(key, func(t *testing.T) {
			parsed, _ := url.Parse(u)
			q := parsed.Query()
			q.Set(key, q.Get(key)+"x")
			parsed.RawQuery = q.Encode()
			before := s.cache.Stats()
			w := perform(s, parsed.String())
			assert.Equal(t, http.StatusForbidden, w.Code)
			assert.Equal(t, before, s.cache.Stats(), "authentication must precede cache access")
			assert.NotContains(t, w.Body.String(), "public.example.com")
		})
	}
	assert.Equal(t, 403, perform(s, u+"&w=0").Code)
	assert.Equal(t, 403, perform(s, u+"&unknown=1").Code)
	assert.Equal(t, 403, perform(s, u+"&bad=%zz").Code)
	s.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	assert.Equal(t, 403, perform(s, u).Code)
}

func TestDestinationsAndLimitsCannotBeSigned(t *testing.T) {
	s := service(t)
	for _, resource := range []string{
		"file:///etc/passwd", "ftp://example.com/a", "https://user:password@example.com/a", "http://localhost/a",
		"http://127.0.0.1/a", "http://[::1]/a", "http://[::ffff:127.0.0.1]/a", "http://10.0.0.1/a",
		"http://169.254.169.254/latest/meta-data", "http://100.100.100.200/a", "http://192.0.2.1/a",
		"https://example.com:444/a", "http://example.com:443/a", "https://example.com/a#fragment",
		"https://example.com/a?token=private", "https://example.com/a?X-Amz-Signature=private", "http://[64:ff9b::7f00:1]/a",
	} {
		_, _, err := s.Sign(Request{URL: resource})
		assert.Error(t, err, resource)
	}
	for _, r := range []Request{
		{Width: 4097}, {Width: 2000, DPR: 3}, {DPR: 4}, {DPR: -1}, {Width: -1}, {Format: "svg"}, {Profile: "missing"},
	} {
		r.URL = "https://example.com/a"
		_, _, err := s.Sign(r)
		assert.Error(t, err, "%+v", r)
	}
	for _, ip := range []string{"0.0.0.0", "127.1.2.3", "169.254.1.1", "10.0.0.1", "172.16.0.1", "192.168.0.1", "100.64.0.1", "198.18.0.1", "224.0.0.1", "255.255.255.255", "::", "::1", "fc00::1", "fe80::1", "2001:db8::1", "2002:7f00:1::", "::ffff:10.0.0.1"} {
		assert.False(t, publicAddress(netip.MustParseAddr(ip)), ip)
	}
	assert.True(t, publicAddress(netip.MustParseAddr("8.8.8.8")))
	assert.True(t, publicAddress(netip.MustParseAddr("2606:4700:4700::1111")))
}

type resolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f resolverFunc) LookupNetIP(c context.Context, network, host string) ([]netip.Addr, error) {
	return f(c, network, host)
}

func TestDNSAndRedirectGuards(t *testing.T) {
	s := service(t)
	s.transport = upstream.New(upstream.Options{DenyPrivate: true, Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("::1")}, nil
	})})
	u := signed(t, s, "original")
	assert.Equal(t, 403, perform(s, u).Code, "a public DNS name resolving to private IPs is refused before connecting")
	for _, destination := range []string{"http://169.254.169.254/a", "https://example.com:8443/a", "https://secret@example.com/a", "ftp://example.com/a"} {
		calls := 0
		s.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {destination}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		})
		assert.Equal(t, 403, perform(s, u).Code, destination)
		assert.Equal(t, 1, calls)
	}
	calls := 0
	s.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"/again"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	assert.Equal(t, 403, perform(s, u).Code)
	assert.Equal(t, 6, calls)
}

func TestSessionResponsesNeverEnterCache(t *testing.T) {
	s := service(t)
	u := signed(t, s, "original")
	assert.Equal(t, 200, perform(s, u).Code)
	for _, h := range []http.Header{
		{"Cache-Control": {"private"}}, {"Cache-Control": {"public", "no-store"}}, {"Cache-Control": {"no-transform"}},
		{"Set-Cookie": {"session=secret"}}, {"Vary": {"Cookie"}}, {"Vary": {"Authorization"}}, {"Vary": {"*"}},
	} {
		previous := s.cache.Stats()
		s.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			assert.Empty(t, r.Header.Get("Cookie"))
			assert.Empty(t, r.Header.Get("Authorization"))
			assert.Empty(t, r.Header.Get("Referer"))
			return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader("private")), Request: r}, nil
		})
		assert.Equal(t, 409, perform(s, u).Code)
		assert.Equal(t, previous, s.cache.Stats())
	}
	for _, h := range []string{"Cookie", "Authorization", "Proxy-Authorization"} {
		r := httptest.NewRequest("GET", u, nil)
		r.Header.Set(h, "secret")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		assert.Equal(t, 409, w.Code)
		assert.NotContains(t, w.Body.String(), "secret")
	}
}

func TestSizeTimeoutAndConcurrency(t *testing.T) {
	s := service(t)
	u := signed(t, s, "original")
	s.o.MaxBytes = 1024
	for _, declared := range []int64{-1, 2048} {
		s.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: declared, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 2048))), Request: r}, nil
		})
		assert.Equal(t, 413, perform(s, u).Code)
	}
	s.o.Timeout = 20 * time.Millisecond
	entered := make(chan struct{})
	s.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- perform(s, u) }()
	<-entered
	assert.Equal(t, 429, perform(s, u).Code)
	assert.Equal(t, 504, (<-done).Code)
	assert.Empty(t, s.slots)
}

func TestVariantIsolationAndProfileInvalidation(t *testing.T) {
	s := service(t)
	profiles := img.Profiles()
	s.o.Profiles = func() map[string]*img.Profile { return profiles }
	var jobs atomic.Int32
	s.o.Transform = func(_ context.Context, j Job) (*Output, error) {
		jobs.Add(1)
		return &Output{Data: j.Raw, Format: img.PNG, Decision: "original", Backend: "go"}, nil
	}
	base := Request{URL: "https://example.com/a.png", Mode: "original"}
	for _, spec := range []Request{
		base, {URL: base.URL, Version: "2", Mode: "original"}, {URL: base.URL, Profile: "saver", Mode: "original"},
		{URL: base.URL, Format: "webp", Mode: "original"}, {URL: base.URL, Width: 300, Mode: "original"},
		{URL: base.URL, Width: 300, DPR: 2, Mode: "original"}, {URL: base.URL, Mode: "optimized"},
	} {
		u, _, err := s.Sign(spec)
		require.NoError(t, err)
		assert.Equal(t, "miss", perform(s, u).Header().Get("X-MidoPress-Cache"))
		assert.Equal(t, "hit", perform(s, u).Header().Get("X-MidoPress-Cache"))
	}
	assert.EqualValues(t, 7, jobs.Load())
	u, _, _ := s.Sign(base)
	profiles["balanced"].TargetDSSIM = .001
	assert.Equal(t, "miss", perform(s, u).Header().Get("X-MidoPress-Cache"))
	s.ClearCache()
	assert.Zero(t, s.cache.Len())
}

func TestWorkerLimitsAndCancellation(t *testing.T) {
	raw := fixture(t)
	assert.NoError(t, checkPixels(raw, img.PNG, 768))
	assert.Error(t, checkPixels(raw, img.PNG, 767))
	assert.Error(t, checkPixels([]byte("<svg/>"), img.Unknown, 1000))
	result, err := Process(os.Args[0])(context.Background(), Job{Raw: raw, Request: Request{Mode: "original"}, Backend: "wasm", MaxPixels: 1000})
	require.NoError(t, err)
	assert.Equal(t, raw, result.Data)
	if os.PathSeparator == '/' {
		worker := filepath.Join(t.TempDir(), "slow-worker")
		require.NoError(t, os.WriteFile(worker, []byte("#!/bin/sh\nexec sleep 30\n"), 0700))
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := Process(worker)(ctx, Job{Raw: raw})
		assert.True(t, errors.Is(err, context.DeadlineExceeded))
		assert.Less(t, time.Since(start), 2*time.Second)
	}
}
