package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/rules"
)

const noTransform = "public, max-age=86400, no-transform"

func TestNoTransformFollowsTheProfile(t *testing.T) {
	jpg := jpegBytes(t, 160, 120, 95)
	html := htmlBody(4000)
	for _, tc := range []struct {
		profile    string
		wantImage  bool
		overridden bool
	}{
		{img.ProfileLossless, false, false},
		{img.ProfileBalanced, false, false},
		{img.ProfileSaver, true, true},
		{img.ProfileExtreme, true, true},
	} {
		t.Run(tc.profile, func(t *testing.T) {
			p := newPipeline(func(c *Config) { c.Profile = tc.profile })
			rec, out := run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", jpg, withHeader("Cache-Control", noTransform)))
			if tc.wantImage {
				if tc.profile == img.ProfileLossless {
					return
				}
				require.Equal(t, Transformed, out.Reason)
				assert.True(t, out.IgnoredNoTransform)
				assert.Less(t, rec.Body.Len(), len(jpg))
				assert.Contains(t, rec.Header().Get("Cache-Control"), "no-transform", "the header is passed on as it came")
			} else {
				assert.Equal(t, PassNoTransform, out.Reason)
				assert.Equal(t, "response", out.Cause)
				assert.False(t, out.IgnoredNoTransform)
				assert.Equal(t, jpg, rec.Body.Bytes())
			}

			// text is only ever concerned by "all"
			rec, out = run(p, newReq("Accept-Encoding", "gzip"), newResp("text/html", html, withHeader("Cache-Control", noTransform)))
			assert.Equal(t, PassNoTransform, out.Reason, "images, not text")
			assert.Equal(t, html, rec.Body.Bytes())
		})
	}
}

func TestNoTransformConfiguration(t *testing.T) {
	jpg := jpegBytes(t, 160, 120, 95)
	html := htmlBody(4000)
	img1 := func(p *Pipeline) Outcome {
		_, out := run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", jpg, withHeader("Cache-Control", noTransform)))
		return out
	}
	text1 := func(p *Pipeline) Outcome {
		_, out := run(p, newReq("Accept-Encoding", "gzip"), newResp("text/html", html, withHeader("Cache-Control", noTransform)))
		return out
	}

	t.Run("respect wins over a profile that would not", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.Profile = img.ProfileExtreme; c.NoTransform = "respect" })
		assert.Equal(t, PassNoTransform, img1(p).Reason)
	})
	t.Run("images turns it on for a profile that respects it", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.NoTransform = "images" })
		out := img1(p)
		assert.Equal(t, Transformed, out.Reason)
		assert.True(t, out.IgnoredNoTransform)
		assert.Equal(t, PassNoTransform, text1(p).Reason)
	})
	t.Run("all adds text", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.NoTransform = "all" })
		assert.Equal(t, Transformed, img1(p).Reason)
		out := text1(p)
		assert.Equal(t, Transformed, out.Reason)
		assert.True(t, out.IgnoredNoTransform)
	})
	t.Run("profile is the default and means what the profile says", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.NoTransform = "profile"; c.Profile = img.ProfileSaver })
		assert.Equal(t, Transformed, img1(p).Reason)
	})
	t.Run("a profile of its own can say it", func(t *testing.T) {
		profiles := img.Profiles()
		custom := *profiles[img.ProfileBalanced]
		custom.Name, custom.NoTransform = "mine", img.NoTransformImages
		profiles["mine"] = &custom
		p := newPipeline(func(c *Config) { c.Profiles = profiles; c.Profile = "mine" })
		assert.Equal(t, Transformed, img1(p).Reason)
	})
	t.Run("a rule changes it for what it matches", func(t *testing.T) {
		p := newPipeline(func(c *Config) {
			c.Rules = compile(t, rules.Rule{Match: `~d cdn\.example\.com`, Set: map[string]any{"no_transform": "images"}})
		})
		assert.Equal(t, PassNoTransform, img1(p).Reason)
		req := newReq("Accept", "image/webp")
		req.URL.Host = "cdn.example.com"
		_, out := run(p, req, newResp("image/jpeg", jpg, withHeader("Cache-Control", noTransform)))
		assert.Equal(t, Transformed, out.Reason)
	})
	t.Run("a bad value in a rule is an error, which sends the original", func(t *testing.T) {
		_, err := newPipeline().live.Load().newFlow(newReq(), rules.Decision{Set: map[string]any{"no_transform": "sometimes"}})
		require.Error(t, err)
	})
}

func TestNoTransformThatIsAlwaysRespected(t *testing.T) {
	jpg := jpegBytes(t, 160, 120, 95)
	p := newPipeline(func(c *Config) { c.Profile = img.ProfileExtreme; c.NoTransform = "all" })

	t.Run("the client said it", func(t *testing.T) {
		rec, out := run(p, newReq("Accept", "image/webp", "Cache-Control", "no-transform"), newResp("image/jpeg", jpg))
		assert.Equal(t, PassNoTransform, out.Reason)
		assert.Equal(t, "request", out.Cause)
		assert.Equal(t, jpg, rec.Body.Bytes())
	})
	t.Run("a range of a file", func(t *testing.T) {
		req := newReq("Accept", "image/webp", "Range", "bytes=0-99")
		rec, out := run(p, req, newResp("image/jpeg", jpg[:100], withStatus(206), withHeader("Content-Range", "bytes 0-99/5000"), withHeader("Cache-Control", noTransform)))
		assert.NotEqual(t, Transformed, out.Reason)
		assert.Equal(t, jpg[:100], rec.Body.Bytes())
	})
	t.Run("something that is not text or an image", func(t *testing.T) {
		body := []byte("binary that says it should not be touched")
		rec, out := run(p, newReq(), newResp("application/octet-stream", body, withHeader("Cache-Control", noTransform)))
		assert.Equal(t, PassNoTransform, out.Reason)
		assert.Equal(t, body, rec.Body.Bytes())
	})
	t.Run("an image that fails to recode is sent as it was", func(t *testing.T) {
		broken := append([]byte("\xff\xd8\xff"), make([]byte, 3000)...)
		rec, out := run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", broken, withHeader("Cache-Control", noTransform)))
		assert.NotEqual(t, Transformed, out.Reason)
		assert.False(t, out.IgnoredNoTransform, "nothing was overridden because nothing was changed")
		assert.Equal(t, broken, rec.Body.Bytes())
	})
}
