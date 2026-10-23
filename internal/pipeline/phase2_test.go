package pipeline

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/codec"
	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/textmin"
	"github.com/goastian/midopress/internal/xcache"
)

func TestProfileSelection(t *testing.T) {
	p := newPipeline()
	lv := p.live.Load()
	h := func(kv ...string) http.Header { return newReq(kv...).Header }
	pick := func(c Client, hd http.Header, rule string) string { return lv.pick(c, hd, rule).Name }

	assert.Equal(t, img.ProfileBalanced, pick(Client{}, h(), ""))
	assert.Equal(t, img.ProfileExtreme, pick(Client{}, h("X-MidoPress-Profile", "Extreme"), ""))
	assert.Equal(t, img.ProfileBalanced, pick(Client{}, h("X-MidoPress-Profile", "turbo"), ""), "an unknown name is ignored")
	assert.Equal(t, img.ProfileSaver, pick(Client{}, h("Save-Data", "on"), ""), "Save-Data raises balanced to saver")

	// device token > header > rule > Save-Data > default
	assert.Equal(t, img.ProfileLossless, pick(Client{Profile: "lossless"}, h("X-MidoPress-Profile", "extreme"), "saver"))
	assert.Equal(t, img.ProfileExtreme, pick(Client{}, h("X-MidoPress-Profile", "extreme"), "lossless"))
	assert.Equal(t, img.ProfileLossless, pick(Client{}, h("Save-Data", "on"), "lossless"), "a rule wins over Save-Data")
	assert.Equal(t, img.ProfileBalanced, pick(Client{Profile: "gone"}, h(), ""), "a device profile that does not exist is ignored")

	ext := newPipeline(func(c *Config) { c.Profile = img.ProfileExtreme })
	assert.Equal(t, img.ProfileExtreme, ext.live.Load().pick(Client{}, h("Save-Data", "on"), "").Name, "never lowered")
	unknown := newPipeline(func(c *Config) { c.Profile = "nope" })
	assert.Equal(t, img.ProfileBalanced, unknown.live.Load().profile)
}

func TestHintsFrom(t *testing.T) {
	h := func(kv ...string) http.Header { return newReq(kv...).Header }
	assert.Equal(t, img.Hints{}, hintsFrom(h()))
	assert.Equal(t, img.Hints{ViewportWidth: 400, DPR: 2}, hintsFrom(h("Sec-CH-Viewport-Width", "391", "Sec-CH-DPR", "2")), "width rounds up to 50")
	assert.Equal(t, img.Hints{ViewportWidth: 450, DPR: 1.5}, hintsFrom(h("Viewport-Width", "412", "DPR", "1.5")), "legacy hints work")
	assert.Equal(t, img.Hints{ViewportWidth: 500, DPR: 3}, hintsFrom(h("X-MidoPress-Viewport", "480x3", "Sec-CH-Viewport-Width", "100")), "the extension header wins")
	assert.Equal(t, img.Hints{}, hintsFrom(h("Sec-CH-Viewport-Width", "-5")))
	assert.Equal(t, img.Hints{}, hintsFrom(h("Sec-CH-Viewport-Width", "999999")))
	assert.Equal(t, img.Hints{ViewportWidth: 400}, hintsFrom(h("Sec-CH-Viewport-Width", "400", "Sec-CH-DPR", "banana")))
}

func TestImageKeyDependsOnEverythingThatChangesTheResult(t *testing.T) {
	bal, _ := img.Lookup(img.ProfileBalanced)
	sav, _ := img.Lookup(img.ProfileSaver)
	raw := []byte("pixels")
	base := imageKey(raw, bal, img.Caps{WebP: true}, img.Hints{}, 0)
	assert.Equal(t, base, imageKey(raw, bal, img.Caps{WebP: true}, img.Hints{}, 0))
	for name, other := range map[string]string{
		"bytes":   imageKey([]byte("other"), bal, img.Caps{WebP: true}, img.Hints{}, 0),
		"profile": imageKey(raw, sav, img.Caps{WebP: true}, img.Hints{}, 0),
		"caps":    imageKey(raw, bal, img.Caps{WebP: true, AVIF: true}, img.Hints{}, 0),
		"hints":   imageKey(raw, bal, img.Caps{WebP: true}, img.Hints{ViewportWidth: 400, DPR: 2}, 0),
		"fixed":   imageKey(raw, bal, img.Caps{WebP: true}, img.Hints{}, 80),
	} {
		assert.NotEqual(t, base, other, name)
	}
}

func TestRepeatedImageComesFromTheCache(t *testing.T) {
	p := newPipeline()
	jpg := jpegBytes(t, 320, 240, 95)
	first, out := run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", jpg))
	require.Equal(t, Transformed, out.Reason)
	assert.Equal(t, 1, p.cache.Len())

	start := time.Now()
	second, out := run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", jpg))
	require.Equal(t, Transformed, out.Reason)
	assert.Equal(t, first.Body.Bytes(), second.Body.Bytes())
	assert.Less(t, time.Since(start), 100*time.Millisecond, "a cache hit does not recode")
	assert.Equal(t, 1, p.cache.Len())

	_, out = run(p, newReq("Accept", "image/webp", "X-MidoPress-Profile", "extreme"), newResp("image/jpeg", jpg))
	require.Equal(t, Transformed, out.Reason)
	assert.Equal(t, 2, p.cache.Len(), "another profile is another result")
}

func TestCacheCanBeDisabled(t *testing.T) {
	p := newPipeline(func(c *Config) { c.Cache = xcache.New(0) })
	jpg := jpegBytes(t, 200, 150, 95)
	_, out := run(p, newReq(), newResp("image/jpeg", jpg))
	require.Equal(t, Transformed, out.Reason)
	assert.Equal(t, 0, p.cache.Len())
}

func TestVaryOnImages(t *testing.T) {
	p := newPipeline()
	jpg := jpegBytes(t, 320, 240, 95)
	rec, out := run(p, newReq(), newResp("image/jpeg", jpg))
	require.Equal(t, Transformed, out.Reason)
	vary := strings.Join(rec.Header().Values("Vary"), ",")
	assert.Contains(t, vary, "Accept")
	assert.Contains(t, vary, "Save-Data")
	assert.NotContains(t, vary, "Sec-CH-Viewport-Width", "balanced never resizes")

	rec, out = run(p, newReq("X-MidoPress-Profile", "saver"), newResp("image/jpeg", jpg))
	require.Equal(t, Transformed, out.Reason)
	assert.Contains(t, strings.Join(rec.Header().Values("Vary"), ","), "Sec-CH-Viewport-Width")
}

func TestSaverShrinksToTheViewport(t *testing.T) {
	p := newPipeline()
	jpg := jpegBytes(t, 1200, 800, 92)
	small, out := run(p, newReq("X-MidoPress-Profile", "saver", "Sec-CH-Viewport-Width", "400", "Sec-CH-DPR", "1"), newResp("image/jpeg", jpg))
	require.Equal(t, Transformed, out.Reason)
	big, out := run(p, newReq("X-MidoPress-Profile", "saver"), newResp("image/jpeg", jpg))
	require.Equal(t, Transformed, out.Reason)
	assert.Less(t, small.Body.Len(), big.Body.Len()/2, "a phone does not get the desktop picture")
}

func TestBudgetFallsBackAndFinishesInTheBackground(t *testing.T) {
	p := newPipeline(func(c *Config) { c.Image.Timeout = time.Nanosecond })
	defer p.Close()
	jpg := jpegBytes(t, 700, 500, 92)
	acc := "image/avif,image/webp,image/jxl"

	rec, out := run(p, newReq("Accept", acc), newResp("image/jpeg", jpg))
	assert.Equal(t, PassTimeout, out.Reason, "the client is not kept waiting")
	assert.Equal(t, jpg, rec.Body.Bytes(), "and gets the original")

	key := imageKey(jpg, p.live.Load().profiles[img.ProfileBalanced], img.CapsFromAccept(acc), img.Hints{}, 0)
	require.Eventually(t, func() bool { _, ok := p.cache.Get(key); return ok }, 2*time.Minute, 100*time.Millisecond)

	rec, out = run(p, newReq("Accept", acc), newResp("image/jpeg", jpg))
	require.Equal(t, Transformed, out.Reason, "the next one gets what the background job made")
	assert.Contains(t, []string{"image/avif", "image/jxl", "image/webp", "image/jpeg"}, rec.Header().Get("Content-Type"))
	assert.Less(t, rec.Body.Len(), len(jpg))
}

func TestBackgroundOff(t *testing.T) {
	p := newPipeline(func(c *Config) { c.Image.Background = false })
	assert.False(t, p.bg.enabled())
	p.bg.submit("k", img.Input{}, time.Time{}, 0)
	assert.Empty(t, p.bg.inflight)
}

func TestFontsBecomeWOFF2OnlyForBrowsers(t *testing.T) {
	ttf, err := os.ReadFile("../fonts/testdata/AnonymousPro-Regular.ttf")
	require.NoError(t, err)
	p := newPipeline()

	rec, out := run(p, newReq("Accept", "font/woff2,font/woff;q=0.9,*/*;q=0.8", "Accept-Encoding", "br"), newResp("font/ttf", ttf))
	require.Equal(t, Transformed, out.Reason)
	assert.Equal(t, "font/woff2", rec.Header().Get("Content-Type"))
	assert.Empty(t, rec.Header().Get("Content-Encoding"))
	assert.Less(t, rec.Body.Len(), len(ttf)*2/3)
	assert.Equal(t, "wOF2", string(rec.Body.Bytes()[:4]))
	assert.Contains(t, strings.Join(rec.Header().Values("Vary"), ","), "Accept")

	rec, out = run(p, newReq("Sec-Fetch-Dest", "font", "Accept", "*/*"), newResp("application/x-font-ttf", ttf))
	require.Equal(t, Transformed, out.Reason)
	assert.Equal(t, "font/woff2", rec.Header().Get("Content-Type"))

	rec, out = run(p, newReq("Accept", "*/*", "Accept-Encoding", "br"), newResp("font/ttf", ttf))
	require.Equal(t, Transformed, out.Reason)
	assert.Equal(t, "font/ttf", rec.Header().Get("Content-Type"), "a client that did not ask for woff2 keeps the format")
	assert.Equal(t, "br", rec.Header().Get("Content-Encoding"))

	junk := append([]byte("OTTO"), make([]byte, 600)...)
	rec, out = run(p, newReq("Accept", "font/woff2", "Accept-Encoding", "identity"), newResp("font/otf", junk))
	assert.NotEqual(t, "font/woff2", rec.Header().Get("Content-Type"), "a broken font is not converted")
	_ = out
}

func TestMinifyModes(t *testing.T) {
	src := []byte("<!DOCTYPE html>\n<html>\n  <body>\n" + strings.Repeat("    <p>   hello    world   </p>\n\n", 100) +
		"    <script>\n      var   a  =  1;\n      var b   =   2;\n    </script>\n  </body>\n</html>\n")
	safe := textmin.Config{HTML: textmin.Safe, CSS: textmin.Safe, JS: textmin.Off, SVG: textmin.Safe, JSON: textmin.Safe}
	p := newPipeline(func(c *Config) { c.Text.Minify = safe })
	rec, out := run(p, newReq("Accept-Encoding", "identity"), newResp("text/html", src))
	require.Equal(t, Transformed, out.Reason)
	assert.Contains(t, rec.Body.String(), "var   a  =  1;", "JavaScript stays as it was when js is off")
	assert.Less(t, rec.Body.Len(), len(src))

	css := []byte(strings.Repeat("body {\n  color : red ;\n  margin : 0 ;\n}\n\n", 80))
	rec, out = run(p, newReq("Accept-Encoding", "identity"), newResp("text/css", css))
	require.Equal(t, Transformed, out.Reason)
	assert.Less(t, rec.Body.Len(), len(css)*3/4)
	assert.Contains(t, rec.Body.String(), "color:red")

	js := []byte(strings.Repeat("function foo ( a , b ) {\n   return a  +  b ;\n}\n", 60))
	rec, out = run(p, newReq("Accept-Encoding", "identity"), newResp("text/javascript", js))
	assert.NotEqual(t, Transformed, out.Reason, "no change at all: not worth sending")
	assert.Equal(t, js, rec.Body.Bytes())
}

func TestCacheableGuess(t *testing.T) {
	h := func(kv ...string) http.Header {
		out := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			out.Add(kv[i], kv[i+1])
		}
		return out
	}
	assert.True(t, cacheable(h("Cache-Control", "public, max-age=31536000, immutable")))
	assert.True(t, cacheable(h("Cache-Control", "max-age=3600")))
	assert.True(t, cacheable(h("Cache-Control", "s-maxage=120")))
	assert.False(t, cacheable(h("Cache-Control", "max-age=10")))
	assert.False(t, cacheable(h("Cache-Control", "private, max-age=3600")))
	assert.False(t, cacheable(h("Cache-Control", "no-store")))
	assert.False(t, cacheable(h("Cache-Control", "max-age=3600", "Set-Cookie", "a=b")))
	assert.False(t, cacheable(h()))
}

func TestStaticResponsesUseTheSlowLevel(t *testing.T) {
	body := []byte(strings.Repeat("The quick brown fox jumps over the lazy dog. Pack my box with five dozen liquor jugs.\n", 400))
	p := newPipeline(func(c *Config) { c.Profile = img.ProfileExtreme })

	dyn, out := run(p, newReq("Accept-Encoding", "br"), newResp("text/plain", body))
	require.Equal(t, Transformed, out.Reason)
	assert.Equal(t, 0, p.cache.Len(), "a response that changes all the time is not kept")
	static, out := run(p, newReq("Accept-Encoding", "br"), newResp("text/plain", body, withHeader("Cache-Control", "public, max-age=86400")))
	require.Equal(t, Transformed, out.Reason)
	assert.LessOrEqual(t, static.Body.Len(), dyn.Body.Len())
	assert.Equal(t, body, decode(t, codec.Brotli, static.Body.Bytes()))
	assert.Equal(t, 1, p.cache.Len(), "the slow result is kept by content")

	again, _ := run(p, newReq("Accept-Encoding", "br"), newResp("text/plain", body, withHeader("Cache-Control", "public, max-age=86400")))
	assert.Equal(t, static.Body.Bytes(), again.Body.Bytes())
}
