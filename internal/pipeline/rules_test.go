package pipeline

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/metrics"
	"github.com/goastian/midopress/internal/rules"
	"github.com/goastian/midopress/internal/textmin"
)

func compile(t *testing.T, rs ...rules.Rule) *rules.RuleSet {
	t.Helper()
	set, err := rules.Compile(rs, rules.WithProfiles("lossless", "balanced", "saver", "extreme"))
	require.NoError(t, err)
	return set
}

func TestRulePassthrough(t *testing.T) {
	jpg := jpegBytes(t, 320, 240, 95)
	p := newPipeline(func(c *Config) {
		c.Rules = compile(t, rules.Rule{Match: `~u /private/ | ~t image/png`, Action: rules.ActionPassthrough})
	})
	rec, out := run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", jpg))
	require.Equal(t, Transformed, out.Reason)
	_ = rec

	req := newReq("Accept", "image/webp")
	req.URL.Path = "/private/photo.jpg"
	rec, out = run(p, req, newResp("image/jpeg", jpg))
	assert.Equal(t, PassRule, out.Reason)
	assert.Equal(t, jpg, rec.Body.Bytes())

	rec, out = run(p, newReq("Accept", "image/webp"), newResp("image/png", pngBytes(t, 300, 300)))
	assert.Equal(t, PassRule, out.Reason, "the content type atom needs the response")
	_ = rec
}

func TestRuleChoosesTheProfile(t *testing.T) {
	jpg := jpegBytes(t, 640, 420, 92)
	p := newPipeline(func(c *Config) {
		c.Rules = compile(t, rules.Rule{Match: `~d cdn\.example\.com & ~t image/`, Profile: "extreme"})
	})
	req := newReq("Accept", "image/webp", "Sec-CH-Viewport-Width", "400", "Sec-CH-DPR", "1")
	req.URL.Host = "cdn.example.com"
	rec, out := run(p, req, newResp("image/jpeg", jpg))
	require.Equal(t, Transformed, out.Reason)
	extreme := rec.Body.Len()

	req = newReq("Accept", "image/webp", "Sec-CH-Viewport-Width", "400", "Sec-CH-DPR", "1")
	req.URL.Host = "other.example.com"
	rec, out = run(p, req, newResp("image/jpeg", jpg))
	require.Equal(t, Transformed, out.Reason)
	assert.Less(t, extreme, rec.Body.Len()/2, "the rule made this one extreme, the other stayed balanced and kept its size")
}

func TestRuleSetChangesMinification(t *testing.T) {
	src := []byte("<!DOCTYPE html>\n<html>\n  <body>\n" + strings.Repeat("    <p>   hello    world   </p>\n\n", 100) +
		"    <script>\n      var   a  =  1;\n      var b   =   2;\n    </script>\n  </body>\n</html>\n")
	p := newPipeline(func(c *Config) {
		c.Text.Minify = textmin.Config{HTML: textmin.Safe, CSS: textmin.Safe, JS: textmin.Off, SVG: textmin.Safe, JSON: textmin.Safe}
		c.Rules = compile(t,
			rules.Rule{Match: `~u /nominify/`, Set: map[string]any{"minify_html": "off"}},
			rules.Rule{Match: `~u /js/`, Set: map[string]any{"minify_js": true}},
		)
	})
	body := func(path string) (string, Outcome) {
		req := newReq("Accept-Encoding", "identity")
		req.URL.Path = path
		rec, out := run(p, req, newResp("text/html", src))
		return rec.Body.String(), out
	}
	plain, out := body("/")
	require.Equal(t, Transformed, out.Reason)
	assert.Contains(t, plain, "var   a  =  1;", "javascript is off by default")

	_, out = body("/nominify/page")
	assert.NotEqual(t, Transformed, out.Reason, "html minification turned off by the rule leaves nothing to send")

	js, out := body("/js/page")
	require.Equal(t, Transformed, out.Reason)
	assert.NotContains(t, js, "var   a  =  1;", "the rule turned javascript minification on")
}

func TestRuleSetImagesAndText(t *testing.T) {
	jpg := jpegBytes(t, 320, 240, 95)
	p := newPipeline(func(c *Config) {
		c.Rules = compile(t,
			rules.Rule{Match: `~u /noimg`, Set: map[string]any{"images": false}},
			rules.Rule{Match: `~u /notext`, Set: map[string]any{"text": false}},
			rules.Rule{Match: `~u /q20`, Set: map[string]any{"quality": 20}},
		)
	})
	req := newReq()
	req.URL.Path = "/noimg"
	_, out := run(p, req, newResp("image/jpeg", jpg))
	assert.Equal(t, PassDisabled, out.Reason)

	req = newReq("Accept-Encoding", "gzip")
	req.URL.Path = "/notext"
	_, out = run(p, req, newResp("text/html", htmlBody(100)))
	assert.Equal(t, PassDisabled, out.Reason)

	req = newReq()
	req.URL.Path = "/q20"
	low, out := run(p, req, newResp("image/jpeg", jpg))
	require.Equal(t, Transformed, out.Reason)
	high, out := run(p, newReq("X-MidoPress-Quality", "90"), newResp("image/jpeg", jpg))
	require.Equal(t, Transformed, out.Reason)
	assert.Less(t, low.Body.Len(), high.Body.Len())
}

func TestHostDecision(t *testing.T) {
	p := newPipeline(func(c *Config) {
		c.Rules = compile(t,
			rules.Rule{Match: `~d (bank|paypal)\.`, Action: rules.ActionPassthroughTLS},
			rules.Rule{Match: `~src 10.0.0.0/8 & ~d intranet`, Action: rules.ActionPassthrough},
			rules.Rule{Match: `~u /x`, Action: rules.ActionPassthrough},
		)
	})
	assert.Equal(t, rules.ActionPassthroughTLS, p.HostDecision("www.bank.example", netip.Addr{}).Action)
	assert.Equal(t, rules.ActionNone, p.HostDecision("example.com", netip.Addr{}).Action)
	assert.Equal(t, rules.ActionPassthrough, p.HostDecision("intranet.corp", netip.MustParseAddr("10.1.2.3")).Action)
	assert.Equal(t, rules.ActionNone, p.HostDecision("intranet.corp", netip.MustParseAddr("192.168.1.1")).Action)
	assert.Equal(t, rules.ActionNone, newPipeline().HostDecision("x", netip.Addr{}).Action, "no rules, no decision")
}

func TestDeviceProfileWinsOverTheHeader(t *testing.T) {
	jpg := jpegBytes(t, 640, 420, 92)
	p := newPipeline()
	hdr := []string{"Accept", "image/webp", "X-MidoPress-Profile", "balanced", "Sec-CH-Viewport-Width", "300", "Sec-CH-DPR", "1"}
	req := newReq(hdr...)
	balanced, out := run(p, req, newResp("image/jpeg", jpg))
	require.Equal(t, Transformed, out.Reason)

	req = newReq(hdr...)
	req = req.WithContext(WithClient(context.Background(), Client{Device: "phone", Profile: "extreme"}))
	extreme, out := run(p, req, newResp("image/jpeg", jpg))
	require.Equal(t, Transformed, out.Reason)
	assert.Less(t, extreme.Body.Len(), balanced.Body.Len()/2)
	assert.Equal(t, Client{Device: "phone", Profile: "extreme"}, ClientFrom(req.Context()))
	assert.Equal(t, Client{}, ClientFrom(context.Background()))
}

func TestReloadChangesRulesAndProfilesAtomically(t *testing.T) {
	jpg := jpegBytes(t, 320, 240, 95)
	p := newPipeline()
	_, out := run(p, newReq(), newResp("image/jpeg", jpg))
	require.Equal(t, Transformed, out.Reason)

	cfg := DefaultConfig()
	cfg.Image.Metric = "ssim"
	cfg.Rules = compile(t, rules.Rule{Match: `~t image/`, Action: rules.ActionPassthrough})
	require.NoError(t, p.Reload(cfg))
	_, out = run(p, newReq(), newResp("image/jpeg", jpg))
	assert.Equal(t, PassRule, out.Reason)

	cfg.Profile = "does-not-exist"
	require.Error(t, p.Reload(cfg))
	_, out = run(p, newReq(), newResp("image/jpeg", jpg))
	assert.Equal(t, PassRule, out.Reason, "a bad configuration changes nothing")

	custom := img.Profiles()
	mobile := *custom[img.ProfileSaver]
	mobile.Name = "mobile"
	custom["mobile"] = &mobile
	cfg = DefaultConfig()
	cfg.Profile, cfg.Profiles = "mobile", custom
	require.NoError(t, p.Reload(cfg))
	assert.Equal(t, "mobile", p.live.Load().pick(Client{}, newReq().Header, "").Name)
}

func TestCacheEntriesCarryTheTTL(t *testing.T) {
	jpg := jpegBytes(t, 320, 240, 95)
	p := newPipeline(func(c *Config) { c.TTL = TTLConfig{Min: time.Minute, Max: 2 * time.Hour, Default: 30 * time.Minute} })
	resp := newResp("image/jpeg", jpg, withHeader("Cache-Control", "public, max-age=7200"))
	_, out := run(p, newReq(), resp)
	require.Equal(t, Transformed, out.Reason)
	key := imageKey(jpg, p.live.Load().profiles[img.ProfileBalanced], img.Caps{}, img.Hints{}, 0)
	e, ok := p.cache.Get(key)
	require.True(t, ok)
	assert.WithinDuration(t, time.Now().Add(2*time.Hour), e.Expires, time.Minute)

	other := jpegBytes(t, 330, 240, 95)
	_, out = run(p, newReq(), newResp("image/jpeg", other, withHeader("Cache-Control", "no-store")))
	require.Equal(t, Transformed, out.Reason)
	e, ok = p.cache.Get(imageKey(other, p.live.Load().profiles[img.ProfileBalanced], img.Caps{}, img.Hints{}, 0))
	require.True(t, ok)
	assert.WithinDuration(t, time.Now().Add(time.Minute), e.Expires, 10*time.Second)
}

func TestInstrumentCountsWhatHappened(t *testing.T) {
	reg := metrics.NewRegistry()
	p := newPipeline()
	p.Instrument(reg)
	jpg := jpegBytes(t, 320, 240, 95)
	_, out := run(p, newReq(), newResp("image/jpeg", jpg))
	require.Equal(t, Transformed, out.Reason)
	_, out = run(p, newReq(), newResp("image/jpeg", jpg))
	require.Equal(t, Transformed, out.Reason)
	_, _ = run(p, newReq(), newResp("application/pdf", []byte("%PDF-1.4 ......")))

	text := string(reg.Gather(nil))
	assert.Contains(t, text, `midopress_responses_total{reason="transformed"} 2`)
	assert.Contains(t, text, `midopress_responses_total{reason="type"} 1`)
	assert.Contains(t, text, `midopress_cache_hits_total 1`)
	assert.Contains(t, text, `midopress_cache_misses_total 1`)
	assert.Contains(t, text, `midopress_cache_hit_ratio 0.5`)
	assert.Contains(t, text, `midopress_pipeline_seconds_count{kind="image"} 2`)
	assert.Contains(t, text, `midopress_image_attempt_seconds_count{format="jpeg"}`)
	assert.Contains(t, text, `midopress_image_saved_bytes_total{format=`)
}
