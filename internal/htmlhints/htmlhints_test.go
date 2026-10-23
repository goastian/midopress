package htmlhints

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func scan(t *testing.T, page string) Page {
	t.Helper()
	base, err := url.Parse("https://Shop.example.com/catalog/index.html?x=1#top")
	require.NoError(t, err)
	return Scan([]byte(page), base, 0)
}

func TestImageWidths(t *testing.T) {
	p := scan(t, `<html><body>
<img src="a.jpg" width="300" height="200">
<img src="/b.png" style="width: 120px; height:80px">
<img src="//cdn.example.net/c.webp?v=2#frag" width=480>
<img src="https://other.test:443/d.jpg" width="64px">
<img src="e.jpg" width="100%">
<img src="f.jpg">
<img src="g.jpg" width="auto">
<img src="data:image/gif;base64,R0lGOD" width="10">
<img width="50">
<img src="h.jpg" width="0">
<img src="a.jpg" width="640">
<img src="a.jpg" width="320">
</body></html>`)
	assert.Equal(t, map[string]int{
		"https://shop.example.com/catalog/a.jpg": 640, // the widest wins
		"https://shop.example.com/b.png":         120,
		"https://cdn.example.net/c.webp?v=2":     480,
		"https://other.test/d.jpg":               64,
	}, p.Images)
}

func TestBaseElementChangesWhatRelativeMeans(t *testing.T) {
	p := scan(t, `<head><base href="https://static.example.com/img/"><base href="https://ignored.test/"></head>
<img src="x.jpg" width="200"><img src="/y.jpg" width="100">`)
	assert.Equal(t, map[string]int{"https://static.example.com/img/x.jpg": 200, "https://static.example.com/y.jpg": 100}, p.Images)
}

func TestIntegrity(t *testing.T) {
	p := scan(t, `<script src="/app.js" integrity="sha384-abc" crossorigin></script>
<script src="/plain.js"></script>
<link rel="stylesheet" href="https://cdn.example.net/site.css" integrity="sha256-xyz">
<link rel="stylesheet" href="/free.css">
<script integrity="sha256-q">inline()</script>
<script>var s = '<script src="/in-a-string.js" integrity="x"><\/script>'</script>`)
	assert.Equal(t, map[string]struct{}{
		"https://shop.example.com/app.js":  {},
		"https://cdn.example.net/site.css": {},
	}, p.Integrity)
}

func TestKeyIgnoresWhatDoesNotChangeTheRequest(t *testing.T) {
	for in, want := range map[string]string{
		"HTTPS://Example.COM:443/a?b=1#c": "https://example.com/a?b=1",
		"http://example.com:80":           "http://example.com/",
		"https://example.com:8443/a":      "https://example.com:8443/a",
		"https://user:pw@example.com/a":   "https://example.com/a",
	} {
		u, err := url.Parse(in)
		require.NoError(t, err)
		assert.Equal(t, want, Key(u), in)
	}
}

func TestLimits(t *testing.T) {
	base, _ := url.Parse("https://example.com/")
	var b strings.Builder
	for i := range 30000 {
		b.WriteString(`<img src="/i`)
		b.WriteString(strings.Repeat("a", i%3))
		b.WriteString(`.jpg" width="100">`)
	}
	big := Scan([]byte(b.String()), base, 0)
	assert.LessOrEqual(t, len(big.Images), 3)

	page := `<img src="/early.jpg" width="100">` + strings.Repeat(" ", 5000) + `<img src="/late.jpg" width="100">`
	cut := Scan([]byte(page), base, 1000)
	assert.Contains(t, cut.Images, "https://example.com/early.jpg")
	assert.NotContains(t, cut.Images, "https://example.com/late.jpg")

	// a page that stops in the middle of a tag
	half := Scan([]byte(`<img src="/ok.jpg" width="9"><img src="/cut.jpg" wid`), base, 0)
	assert.Equal(t, map[string]int{"https://example.com/ok.jpg": 9}, half.Images)
}

func FuzzScan(f *testing.F) {
	f.Add(`<img src=a.jpg width=3><script src=x integrity=y>`)
	f.Add(`<base href="//x"><link href=a integrity=b>`)
	f.Add("<img src=\x00 width=\xff style='width:")
	base, _ := url.Parse("https://example.com/p/")
	f.Fuzz(func(t *testing.T, s string) {
		p := Scan([]byte(s), base, 0)
		for u, w := range p.Images {
			if !strings.HasPrefix(u, "http") || w < 1 {
				t.Fatalf("bad entry %q %d", u, w)
			}
		}
	})
}
