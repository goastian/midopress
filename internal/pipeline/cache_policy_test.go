package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goastian/midopress/internal/codec"
	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/rules"
	"github.com/goastian/midopress/internal/textmin"
	"github.com/goastian/midopress/internal/xcache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type privacyCase struct {
	name              string
	request, response http.Header
}

func privacyCases() []privacyCase {
	return []privacyCase{
		{"request-no-store", http.Header{"Cache-Control": {"no-store"}}, nil},
		{"request-no-cache", http.Header{"Cache-Control": {"no-cache"}}, nil},
		{"request-cookie", http.Header{"Cookie": {"session=fixture"}}, nil},
		{"request-authorization", http.Header{"Authorization": {"Bearer fixture"}}, nil},
		{"empty-cookie", http.Header{"Cookie": {""}}, nil},
		{"empty-authorization", http.Header{"Authorization": {""}}, nil},
		{"response-no-store", nil, http.Header{"Cache-Control": {"public, max-age=3600", "no-store"}}},
		{"response-private", nil, http.Header{"Cache-Control": {"public, private=\"set-cookie\", max-age=3600"}}},
		{"response-no-cache", nil, http.Header{"Cache-Control": {"no-cache"}}},
		{"set-cookie", nil, http.Header{"Set-Cookie": {"session=fixture"}}},
		{"vary-cookie", nil, http.Header{"Vary": {"Accept-Encoding", "Cookie"}}},
		{"vary-authorization", nil, http.Header{"Vary": {"Authorization"}}},
		{"vary-star", nil, http.Header{"Vary": {"*"}}},
		{"authenticate", nil, http.Header{"Www-Authenticate": {"Bearer"}}},
	}
}

func applyPrivacy(req *http.Request, resp *http.Response, tc privacyCase) {
	for k, v := range tc.request {
		req.Header[k] = v
	}
	for k, v := range tc.response {
		resp.Header[k] = v
	}
}

func TestSharedCacheEligibility(t *testing.T) {
	assert.True(t, sharedCacheAllowed(newReq(), http.Header{"Cache-Control": {"public, max-age=3600"}}))
	for _, tc := range privacyCases() {
		t.Run(tc.name, func(t *testing.T) {
			req := newReq()
			resp := newResp("text/plain", nil)
			applyPrivacy(req, resp, tc)
			assert.False(t, sharedCacheAllowed(req, resp.Header))
		})
	}
	req := newReq()
	req.Method = http.MethodPost
	assert.False(t, sharedCacheAllowed(req, nil))
}

func TestPrivateImageDoesNotSharePublicSingleflight(t *testing.T) {
	p := newPipeline()
	defer p.Close()
	jpg := jpegBytes(t, 160, 120, 95)
	profile := p.live.Load().profiles[img.ProfileBalanced]
	key := p.imageCacheKey(jpg, profile, img.Caps{WebP: true}, img.Hints{}, 0)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() { close(release); <-finished }()
	go func() {
		defer close(finished)
		_, _, _ = p.flight.Do(key, func() (any, error) { close(entered); <-release; return nil, nil })
	}()
	<-entered
	req := newReq("Accept", "image/webp", "Cookie", "session=fixture")
	ctx, cancel := context.WithTimeout(req.Context(), time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	rec, out := run(p, req, newResp("image/jpeg", jpg))
	require.Equal(t, Transformed, out.Reason, "private work must not join the blocked public flight")
	assertImagePolicy(t, p, jpg, rec, out)
	assert.Zero(t, p.cache.Len())
}

func TestPrivateImagesNeverReadWriteCaptureOrQueueSharedState(t *testing.T) {
	jpg := jpegBytes(t, 160, 120, 95)
	for _, tc := range privacyCases() {
		t.Run(tc.name, func(t *testing.T) {
			p := newPipeline()
			defer p.Close()
			p.SetCapture(true)
			_, public := run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", jpg))
			require.Equal(t, Transformed, public.Reason)
			require.NotEmpty(t, public.Original)
			before := p.CacheStats()
			for range 2 {
				req := newReq("Accept", "image/webp")
				resp := newResp("image/jpeg", jpg)
				applyPrivacy(req, resp, tc)
				rec, out := run(p, req, resp)
				assertImagePolicy(t, p, jpg, rec, out)
				assert.False(t, out.CacheHit)
				assert.Empty(t, out.Original)
				assert.Empty(t, out.Result)
			}
			after := p.CacheStats()
			assert.Equal(t, before, after, "private traffic must not even consult shared caches")
			assert.True(t, p.bg.idle())
		})
	}
}

func TestPrivateTextFontsHintsAndBackgroundLeaveNoDiskCache(t *testing.T) {
	font, err := os.ReadFile(filepath.Join("..", "fonts", "testdata", "AnonymousPro-Regular.ttf"))
	require.NoError(t, err)
	for _, tc := range privacyCases() {
		t.Run(tc.name, func(t *testing.T) {
			cache, err := xcache.Open(xcache.Options{MemoryBytes: 1 << 20, Dir: t.TempDir(), DiskBytes: 4 << 20})
			require.NoError(t, err)
			defer cache.Close()
			p := newPipeline(func(c *Config) {
				c.Cache = cache
				c.Profile = img.ProfileExtreme
				c.TranscodeWait = time.Millisecond
				c.MaxConcurrent = 1
			})
			defer p.Close()
			for _, body := range []struct {
				mime string
				data []byte
			}{
				{"text/plain", bytes.Repeat([]byte("fixture text repeats exactly\n"), 100)},
				{"text/html", page(`<img src="/session.jpg" width="100"><script src="/private.js" integrity="sha384-fixture"></script>`)},
				{"font/ttf", font},
			} {
				req := newReq("Accept-Encoding", "br", "Sec-Fetch-Dest", "font")
				resp := newResp(body.mime, body.data, withHeader("Cache-Control", static))
				applyPrivacy(req, resp, tc)
				_, out := run(p, req, resp)
				assert.NoError(t, out.Err)
			}
			assert.Zero(t, p.pages.width(requestURL(pageReq("/session.jpg"))))
			assert.False(t, p.pages.pinned(requestURL(pageReq("/private.js"))))
			p.sem <- struct{}{}
			req := newReq("Accept", "image/webp")
			resp := newResp("image/jpeg", jpegBytes(t, 160, 120, 95))
			applyPrivacy(req, resp, tc)
			_, out := run(p, req, resp)
			assert.Equal(t, PassBusy, out.Reason)
			p.releaseCPU()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			assert.True(t, p.WaitBackground(ctx))
			assert.Zero(t, cache.Len())
			assert.Zero(t, cache.Stats().DiskWrites)
			assert.Zero(t, cache.Stats().Hits())
		})
	}
}

func TestVariantKeysInvalidateActualSettings(t *testing.T) {
	p := newPipeline()
	defer p.Close()
	prof := *p.live.Load().profiles[img.ProfileBalanced]
	raw := []byte("fixture content")
	key := p.imageCacheKey(raw, &prof, img.Caps{WebP: true}, img.Hints{ViewportWidth: 300, DPR: 2}, 0)
	p.engine.Metric = img.MetricDSSIM
	assert.NotEqual(t, key, p.imageCacheKey(raw, &prof, img.Caps{WebP: true}, img.Hints{ViewportWidth: 300, DPR: 2}, 0))
	p.engine.Metric = img.MetricSSIM
	prof.TargetSSIM = .99
	assert.NotEqual(t, key, p.imageCacheKey(raw, &prof, img.Caps{WebP: true}, img.Hints{ViewportWidth: 300, DPR: 2}, 0))
	fl, err := p.live.Load().newFlow(newReq(), rules.Decision{})
	require.NoError(t, err)
	text := p.textCacheKey(fl, "br", raw)
	cfg := DefaultConfig()
	cfg.Levels.Brotli = 11
	require.NoError(t, p.Reload(cfg))
	fl, err = p.live.Load().newFlow(newReq(), rules.Decision{})
	require.NoError(t, err)
	assert.NotEqual(t, text, p.textCacheKey(fl, "br", raw))
	text = p.textCacheKey(fl, "br", raw)
	previous := p.cacheBackend
	p.cacheBackend = "different-backend-runtime"
	assert.NotEqual(t, text, p.textCacheKey(fl, "br", raw))
	p.cacheBackend = previous
}

func TestDiskVariantsWarmAcrossOpenButInvalidateMetricAndBackend(t *testing.T) {
	dir := t.TempDir()
	open := func() *xcache.Cache {
		cache, err := xcache.Open(xcache.Options{Dir: dir, DiskBytes: 8 << 20})
		require.NoError(t, err)
		return cache
	}
	jpg := jpegBytes(t, 160, 120, 95)
	cache := open()
	p := newPipeline(func(c *Config) { c.Cache = cache })
	_, out := run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", jpg))
	require.Equal(t, Transformed, out.Reason)
	assert.False(t, out.CacheHit)
	cache.Flush()
	p.Close()
	cache.Close()
	cache = open()
	defer cache.Close()
	p = newPipeline(func(c *Config) { c.Cache = cache })
	defer p.Close()
	_, out = run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", jpg))
	require.True(t, out.CacheHit, "same binary/profile/options can use the reopened disk cache")
	assert.Positive(t, cache.Stats().DiskHits)
	hits := cache.Stats().Hits()
	p.engine.Metric = img.MetricDSSIM
	_, out = run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", jpg))
	assert.False(t, out.CacheHit)
	assert.Equal(t, hits, cache.Stats().Hits())
	p.cacheBackend = "changed actual backend identity"
	_, out = run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", jpg))
	assert.False(t, out.CacheHit)
	assert.Equal(t, hits, cache.Stats().Hits())
}

func TestPrivatePinnedSubresourceStaysExactEvenWithExplicitMinification(t *testing.T) {
	p := newPipeline(func(c *Config) {
		c.Text.AllowIntegrityChanges = true
		c.Text.Minify = textmin.DefaultConfig()
		c.Text.Minify.JS = textmin.Safe
	})
	defer p.Close()
	run(p, pageReq("/index", "Accept-Encoding", "gzip"), newResp("text/html", page(`<script src="/session.js" integrity="sha384-fixture"></script>`)))
	js := bytes.Repeat([]byte("function  sum ( a , b ) { return a + b; }\n"), 100)
	rec, out := run(p, pageReq("/session.js", "Cookie", "session=fixture", "Accept-Encoding", "gzip"), newResp("application/javascript", js))
	require.Equal(t, Transformed, out.Reason)
	assert.False(t, out.Minified)
	assert.Equal(t, js, decode(t, codec.Gzip, rec.Body.Bytes()))
}

func TestAllProfilesRespectNoTransformUnlessExplicitlyEnabled(t *testing.T) {
	for _, profile := range []string{img.ProfileLossless, img.ProfileBalanced, img.ProfileSaver, img.ProfileExtreme} {
		p := newPipeline(func(c *Config) { c.Profile = profile })
		defer p.Close()
		for _, headers := range [][]string{nil, {"X-MidoPress-Profile", "extreme"}, {"Save-Data", "on"}} {
			req := newReq(headers...)
			req.Header.Set("Accept", "image/webp")
			jpg := jpegBytes(t, 160, 120, 95)
			rec, out := run(p, req, newResp("image/jpeg", jpg, withHeader("Cache-Control", "no-transform")))
			assert.Equal(t, PassNoTransform, out.Reason)
			assert.Equal(t, jpg, rec.Body.Bytes())
		}
	}
}

func TestDynamicIntegrityBytesSurviveEveryProfileAndRule(t *testing.T) {
	for _, profile := range []string{img.ProfileLossless, img.ProfileBalanced, img.ProfileSaver, img.ProfileExtreme} {
		p := newPipeline(func(c *Config) {
			c.Profile = profile
			c.Text.Minify = textmin.DefaultConfig()
			c.Rules = compile(t, rules.Rule{Match: "~all", Set: map[string]any{"minify_js": true, "minify_css": true}})
		})
		defer p.Close()
		for _, body := range []struct {
			mime string
			data []byte
		}{{"application/javascript", []byte("function  sum ( a, b ) { return a + b; }\n")}, {"text/css", []byte("body { color : red; margin : 0; }\n")}} {
			body.data = bytes.Repeat(body.data, 100)
			rec, out := run(p, newReq("Accept-Encoding", "gzip"), newResp(body.mime, body.data))
			require.Equal(t, Transformed, out.Reason)
			assert.False(t, out.Minified)
			decoded := decode(t, codec.Gzip, rec.Body.Bytes())
			assert.Equal(t, sha256.Sum256(body.data), sha256.Sum256(decoded), "an unseen dynamic SRI consumer receives exact bytes")
		}
	}
}
