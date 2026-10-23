package pipeline

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/codec"
	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/rules"
	"github.com/goastian/midopress/internal/textmin"
)

func pageReq(path string, headers ...string) *http.Request {
	r := newReq(headers...)
	r.URL.Path = path
	return r
}

func page(body string) []byte {
	pad := strings.Repeat("<p>filler text that makes the page worth compressing</p>\n", 40)
	return []byte("<!doctype html><html><head><title>t</title></head><body>" + body + pad + "</body></html>")
}

func decodedWidth(t *testing.T, data []byte, mime string) int {
	t.Helper()
	f := map[string]img.Format{"image/webp": img.WebP, "image/avif": img.AVIF, "image/jpeg": img.JPEG}[mime]
	m, err := img.NewWASM().Decode(f, data)
	require.NoError(t, err)
	return m.Bounds().Dx()
}

func TestAPageTellsHowWideItsImagesAre(t *testing.T) {
	big := jpegBytes(t, 300, 225, 90)
	html := page(`<img src="/photos/big.jpg" width="100" height="75"><img src="/photos/free.jpg">`)

	t.Run("a profile that resizes uses it when the client did not say its screen", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.Profile = img.ProfileSaver })
		_, out := run(p, pageReq("/index.html", "Accept-Encoding", "gzip"), newResp("text/html", html))
		require.Equal(t, Transformed, out.Reason)

		rec, out := run(p, pageReq("/photos/big.jpg", "Accept", "image/webp"), newResp("image/jpeg", big))
		require.Equal(t, Transformed, out.Reason)
		w := decodedWidth(t, rec.Body.Bytes(), rec.Header().Get("Content-Type"))
		assert.Equal(t, 200, w, "100 CSS pixels at a density of 2")

		// an image the page did not size is left at its size
		rec, out = run(p, pageReq("/photos/free.jpg", "Accept", "image/webp"), newResp("image/jpeg", big))
		require.Equal(t, Transformed, out.Reason)
		assert.Equal(t, 300, decodedWidth(t, rec.Body.Bytes(), rec.Header().Get("Content-Type")))
	})

	t.Run("what the client says about its screen wins", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.Profile = img.ProfileSaver })
		run(p, pageReq("/index.html", "Accept-Encoding", "gzip"), newResp("text/html", html))
		rec, out := run(p, pageReq("/photos/big.jpg", "Accept", "image/webp", "X-MidoPress-Viewport", "150x1"), newResp("image/jpeg", big))
		require.Equal(t, Transformed, out.Reason)
		assert.Equal(t, 150, decodedWidth(t, rec.Body.Bytes(), rec.Header().Get("Content-Type")))
	})

	t.Run("a profile that does not resize never does", func(t *testing.T) {
		p := newPipeline()
		run(p, pageReq("/index.html", "Accept-Encoding", "gzip"), newResp("text/html", html))
		rec, out := run(p, pageReq("/photos/big.jpg", "Accept", "image/webp"), newResp("image/jpeg", big))
		require.Equal(t, Transformed, out.Reason)
		assert.Equal(t, 300, decodedWidth(t, rec.Body.Bytes(), rec.Header().Get("Content-Type")))
	})

	t.Run("it can be turned off", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.Profile = img.ProfileSaver; c.Image.PageHints = false })
		run(p, pageReq("/index.html", "Accept-Encoding", "gzip"), newResp("text/html", html))
		rec, _ := run(p, pageReq("/photos/big.jpg", "Accept", "image/webp"), newResp("image/jpeg", big))
		assert.Equal(t, 300, decodedWidth(t, rec.Body.Bytes(), rec.Header().Get("Content-Type")))
	})

	t.Run("the density it assumes can be changed", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.Profile = img.ProfileSaver; c.Image.HintDPR = 1 })
		run(p, pageReq("/index.html", "Accept-Encoding", "gzip"), newResp("text/html", html))
		rec, _ := run(p, pageReq("/photos/big.jpg", "Accept", "image/webp"), newResp("image/jpeg", big))
		assert.Equal(t, 100, decodedWidth(t, rec.Body.Bytes(), rec.Header().Get("Content-Type")))
	})

	t.Run("a page that comes as a stream is read before it is sent on", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.Profile = img.ProfileSaver })
		// unknown length: it is streamed, and the image is near the top
		resp := newResp("text/html", html, withUnknownLength())
		rec, out := run(p, pageReq("/index.html", "Accept-Encoding", "gzip"), resp)
		require.Equal(t, Transformed, out.Reason)
		require.NotZero(t, rec.Body.Len())
		rec, out = run(p, pageReq("/photos/big.jpg", "Accept", "image/webp"), newResp("image/jpeg", big))
		require.Equal(t, Transformed, out.Reason)
		assert.Equal(t, 200, decodedWidth(t, rec.Body.Bytes(), rec.Header().Get("Content-Type")))
	})
}

func TestAStreamedPageIsKnownWhenItsFirstPartIsSent(t *testing.T) {
	p := newPipeline(func(c *Config) { c.Profile = img.ProfileSaver })
	big := jpegBytes(t, 300, 225, 90)
	head := page(`<img src="/photos/early.jpg" width="100">`)
	var tail bytes.Buffer
	for i := range 4000 {
		fmt.Fprintf(&tail, "<p>paragraph %d of a long page that keeps coming</p>\n", i)
	}

	pr, pw := io.Pipe()
	resp := newResp("text/html", nil, withUnknownLength())
	resp.Body = pr
	done := make(chan struct{})
	rec := &syncRecorder{}
	go func() {
		defer close(done)
		p.Handle(rec, pageReq("/long.html", "Accept-Encoding", "gzip"), resp)
	}()
	_, err := pw.Write(head)
	require.NoError(t, err)
	_, err = pw.Write(bytes.Repeat([]byte("<!-- padding -->\n"), 2500)) // 40 KiB, past the first scan
	require.NoError(t, err)

	// the rest of the page has not arrived; the image is already known
	require.Eventually(t, func() bool { return p.pages.width(requestURL(pageReq("/photos/early.jpg"))) > 0 }, 5*time.Second, 5*time.Millisecond)
	rec2, out := run(p, pageReq("/photos/early.jpg", "Accept", "image/webp"), newResp("image/jpeg", big))
	require.Equal(t, Transformed, out.Reason)
	assert.Equal(t, 200, decodedWidth(t, rec2.Body.Bytes(), rec2.Header().Get("Content-Type")))

	_, _ = pw.Write(tail.Bytes())
	_ = pw.Close()
	<-done
}

type syncRecorder struct {
	header http.Header
	body   bytes.Buffer
}

func (s *syncRecorder) Header() http.Header {
	if s.header == nil {
		s.header = http.Header{}
	}
	return s.header
}
func (s *syncRecorder) Write(b []byte) (int, error) { return s.body.Write(b) }
func (s *syncRecorder) WriteHeader(int)             {}
func (s *syncRecorder) Flush()                      {}

func TestAFileWithAnIntegrityHashIsNeverMinified(t *testing.T) {
	js := []byte(strings.Repeat("function  add ( a , b )  {\n    return   a + b ;\n}\n\n// a comment\n", 80))
	css := []byte(strings.Repeat("body  {  color :  red ;  }\n\n/* c */\n", 120))
	html := page(`<script src="/static/app.js" integrity="sha384-abc" crossorigin="anonymous"></script>` +
		`<link rel="stylesheet" href="/static/site.css" integrity="sha384-def">` +
		`<script src="/static/free.js"></script>`)

	for _, profile := range []string{img.ProfileBalanced, img.ProfileSaver} {
		t.Run(profile, func(t *testing.T) {
			p := newPipeline(func(c *Config) {
				c.Profile = profile
				c.Text.Minify = textmin.Config{HTML: textmin.Off, CSS: textmin.Safe, JS: textmin.Safe, SVG: textmin.Off, JSON: textmin.Off}
			})
			run(p, pageReq("/index.html", "Accept-Encoding", "gzip"), newResp("text/html", html))

			rec, out := run(p, pageReq("/static/app.js", "Accept-Encoding", "gzip"), newResp("application/javascript", js))
			require.Equal(t, Transformed, out.Reason)
			assert.False(t, out.Minified, "the page pins it")
			assert.Equal(t, js, decode(t, codec.Gzip, rec.Body.Bytes()), "the same bytes, only compressed")

			rec, out = run(p, pageReq("/static/site.css", "Accept-Encoding", "gzip"), newResp("text/css", css))
			require.Equal(t, Transformed, out.Reason)
			assert.False(t, out.Minified)
			assert.Equal(t, css, decode(t, codec.Gzip, rec.Body.Bytes()))

			_, out = run(p, pageReq("/static/free.js", "Accept-Encoding", "gzip"), newResp("application/javascript", js))
			require.Equal(t, Transformed, out.Reason)
			assert.True(t, out.Minified, "a file the page does not pin is minified")
		})
	}
}

func TestJavaScriptIsMinifiedBySaverAndExtremeOnly(t *testing.T) {
	js := []byte(strings.Repeat("function  add ( a , b )  {\n    return   a + b ;\n}\n\n// a comment\n", 80))
	for profile, want := range map[string]bool{img.ProfileLossless: false, img.ProfileBalanced: false, img.ProfileSaver: true, img.ProfileExtreme: true} {
		p := newPipeline(func(c *Config) { c.Profile = profile })
		_, out := run(p, pageReq("/app.js", "Accept-Encoding", "gzip"), newResp("application/javascript", js))
		require.Equal(t, Transformed, out.Reason, profile)
		assert.Equal(t, want, out.Minified, profile)
	}

	t.Run("a rule can switch it off for a site", func(t *testing.T) {
		p := newPipeline(func(c *Config) {
			c.Profile = img.ProfileExtreme
			c.Rules = compile(t, rules.Rule{Match: "~d example.test", Set: map[string]any{"minify_js": "off"}})
		})
		_, out := run(p, pageReq("/app.js", "Accept-Encoding", "gzip"), newResp("application/javascript", js))
		assert.False(t, out.Minified)
	})
}
