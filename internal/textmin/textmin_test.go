package textmin

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()
	assert.Equal(t, Config{HTML: Safe, CSS: Safe, JS: Off, SVG: Safe, JSON: Safe, SkipMinified: true}, c)
	assert.True(t, c.Enabled())
}

func TestConfigEnabled(t *testing.T) {
	assert.False(t, Config{}.Enabled())
	assert.False(t, newConfig(Off, Off, Off, Off, Off).Enabled())
	assert.False(t, newConfig("bogus", "", Off, Off, Off).Enabled())
	assert.True(t, newConfig(Off, Off, Safe, Off, Off).Enabled())
	assert.True(t, newConfig(Off, Off, Off, Off, Aggressive).Enabled())
}

func TestHandles(t *testing.T) {
	def := New(DefaultConfig())
	withJS := New(safeConfig())
	aggressive := New(aggressiveConfig())
	tests := []struct {
		mediaType                   string
		wantDefault, wantJS, wantAg bool
	}{
		{"text/html", true, true, true},
		{"text/html; charset=utf-8", true, true, true},
		{"TEXT/HTML;charset=UTF-8", true, true, true},
		{" text/html ", true, true, true},
		{"application/xhtml+xml", false, false, true},
		{"text/css", true, true, true},
		{"text/javascript", false, true, true},
		{"application/javascript", false, true, true},
		{"application/x-javascript", false, true, true},
		{"application/ecmascript", false, true, true},
		{"image/svg+xml", true, true, true},
		{"application/json", true, true, true},
		{"text/json", true, true, true},
		{"application/ld+json", true, true, true},
		{"application/vnd.api+json", true, true, true},
		{"application/json-seq", false, false, false},
		{"+json", false, false, false},
		{"text/plain", false, false, false},
		{"application/xml", false, false, false},
		{"image/png", false, false, false},
		{"", false, false, false},
		{"garbage", false, false, false},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.wantDefault, def.Handles(tc.mediaType), "default %q", tc.mediaType)
		assert.Equal(t, tc.wantJS, withJS.Handles(tc.mediaType), "all safe %q", tc.mediaType)
		assert.Equal(t, tc.wantAg, aggressive.Handles(tc.mediaType), "aggressive %q", tc.mediaType)
	}

	htmlOnly := New(newConfig(Safe, Off, Off, Off, Off))
	assert.True(t, htmlOnly.Handles("text/html"))
	assert.False(t, htmlOnly.Handles("text/css"))
	assert.False(t, htmlOnly.Handles("application/json"))
	assert.False(t, New(Config{}).Handles("text/html"))
}

func TestMinifyUnsupported(t *testing.T) {
	m := New(DefaultConfig())
	var w countingWriter
	for _, mt := range []string{"text/plain", "text/javascript", "application/xhtml+xml"} {
		err := m.Minify(mt, &w, []byte("x"))
		assert.ErrorIs(t, err, ErrUnsupported, mt)
	}
	assert.Zero(t, w.writes)
}

func TestMinifyEmpty(t *testing.T) {
	m := New(safeConfig())
	var w countingWriter
	require.NoError(t, m.Minify("text/html", &w, nil))
	require.NoError(t, m.Minify("application/json", &w, []byte{}))
	assert.Zero(t, w.writes)
}

func TestMinifyTooLarge(t *testing.T) {
	cfg := safeConfig()
	cfg.SkipMinified = false
	m := New(cfg)

	big := []byte("[1" + strings.Repeat(" ", MaxSize-3) + "]")
	require.Len(t, big, MaxSize)
	var out bytes.Buffer
	require.NoError(t, m.Minify("application/json", &out, big))
	assert.Equal(t, "[1]", out.String())

	big = append(big, ' ')
	var w countingWriter
	assert.ErrorIs(t, m.Minify("application/json", &w, big), ErrTooLarge)
	assert.Zero(t, w.writes)
}

func TestMinifySkipsMinifiedInput(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("[")
	for i := range 400 {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"k":"abcdefghij","v":12345}`)
	}
	sb.WriteString("]")
	in := sb.String()
	require.True(t, LooksMinified([]byte(in)))

	m := New(DefaultConfig())
	assert.Equal(t, in, minifyString(t, m, "application/json", in))

	spaced := strings.Replace(in, ":", " : ", 1)
	assert.Equal(t, spaced, minifyString(t, m, "application/json", spaced), "still skipped: whitespace share is low")

	cfg := DefaultConfig()
	cfg.SkipMinified = false
	assert.Equal(t, in, minifyString(t, New(cfg), "application/json", in))
}

func TestMinifyNeverGrowsOutput(t *testing.T) {
	cfg := aggressiveConfig()
	cfg.SkipMinified = false
	m := New(cfg)
	// the CSS minifier rewrites "0.0" style values but never makes them longer,
	// and when a minifier does grow the input the original must be written
	for _, in := range []string{"a{b:c}", "a{color:#fff}", "a{margin:0}"} {
		assert.LessOrEqual(t, len(minifyString(t, m, "text/css", in)), len(in))
	}
}

func TestMinifyDoesNotModifySource(t *testing.T) {
	cfg := safeConfig()
	cfg.SkipMinified = false
	m := New(cfg)
	inputs := map[string]string{
		"text/html":        "<A HREF=\"HTTP://X.com\" CLASS=\"  a   b \"><B>x</B>  y</A><style>A{COLOR:RED}</style>",
		"text/css":         "A:HOVER{COLOR:RED}",
		"text/javascript":  "var  a = 1;",
		"image/svg+xml":    "<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"10PX\"><G  ID=\"a\"/></svg>",
		"application/json": "{ \"a\" : 1 }",
	}
	for mt, in := range inputs {
		src := []byte(in)
		var out bytes.Buffer
		require.NoError(t, m.Minify(mt, &out, src), mt)
		assert.Equal(t, in, string(src), mt)
	}
}

type failingWriter struct{}

var errWrite = errors.New("write failed")

func (failingWriter) Write([]byte) (int, error) { return 0, errWrite }

func TestMinifyPropagatesWriteErrors(t *testing.T) {
	m := New(safeConfig())
	assert.ErrorIs(t, m.Minify("application/json", failingWriter{}, []byte(`{ "a" : 1 }`)), errWrite)
}

func TestMinifyConcurrent(t *testing.T) {
	cfg := safeConfig()
	cfg.SkipMinified = false
	m := New(cfg)
	type job struct {
		mediaType string
		in        []byte
	}
	jobs := []job{
		{"text/html", corpusBody(t, "iana.org/domains/reserved")},
		{"text/css", []byte("a  {  color : red ; }\n.b{margin:0 0 0 0}")},
		{"application/javascript", corpusBody(t, "htmx.min.js")},
		{"image/svg+xml", corpusBody(t, "SVG_logo.svg")},
		{"application/json", corpusBody(t, "api.github.com")},
		{"text/html", []byte("<style>a  {  color : red ; }</style><script>var  a = 1;</script><p>  x  </p>")},
	}
	want := make([][]byte, len(jobs))
	for i, j := range jobs {
		var out bytes.Buffer
		require.NoError(t, m.Minify(j.mediaType, &out, j.in))
		want[i] = out.Bytes()
	}

	var wg sync.WaitGroup
	for g := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range 40 {
				i := (g + n) % len(jobs)
				var out bytes.Buffer
				if err := m.Minify(jobs[i].mediaType, &out, jobs[i].in); err != nil {
					t.Errorf("job %d: %v", i, err)
					return
				}
				if !bytes.Equal(out.Bytes(), want[i]) {
					t.Errorf("job %d: output differs between runs", i)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestSemanticCheckersDetectDamage(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		assert.True(t, checkFails(func(ft testing.TB) { requireJSONEqual(ft, []byte(`{"a":1.0}`), []byte(`{"a":1}`)) }),
			"1.0 and 1 differ when numbers are decoded as json.Number")
		assert.False(t, checkFails(func(ft testing.TB) { requireJSONEqual(ft, []byte(`{"a":[1,2]}`), []byte(`{"a": [1, 2]}`)) }))
	})
	t.Run("svg", func(t *testing.T) {
		orig := []byte(`<svg viewBox="0 0 1 1"><g id="a"><path d="M0 0"/></g></svg>`)
		check := func(minified string) bool {
			return checkFails(func(ft testing.TB) { requireSVGPreserved(ft, orig, []byte(minified)) })
		}
		assert.True(t, check(`<svg viewBox="0 0 1 1"><g><path d="M0 0"/></g></svg>`), "lost id")
		assert.True(t, check(`<svg><g id="a"><path d="M0 0"/></g></svg>`), "lost viewBox")
		assert.True(t, check(`<svg viewBox="0 0 1 1"><g id="a"><path d="M0 0"/><rect/></g></svg>`), "gained element")
		assert.False(t, check(`<svg viewBox="0 0 1 1"><g id="a"><path d="M0 0"/></g></svg>`))
	})
	t.Run("js", func(t *testing.T) {
		assert.True(t, checkFails(func(ft testing.TB) {
			requireJSPreserved(ft, []byte("function keep(){}var also=1"), []byte("function keep(){}"))
		}), "lost top-level name")
		assert.True(t, checkFails(func(ft testing.TB) { requireJSPreserved(ft, []byte("var a=1"), []byte("var a=")) }), "does not parse")
	})
}
