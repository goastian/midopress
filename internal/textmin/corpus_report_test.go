package textmin

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/stretchr/testify/require"
)

func gzipSize(b []byte) int {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return buf.Len()
}

func brotliSize(b []byte) int {
	var buf bytes.Buffer
	zw := brotli.NewWriterLevel(&buf, 9)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return buf.Len()
}

func shortURL(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "www.")
	if len(u) > 52 {
		u = u[:49] + "..."
	}
	return u
}

func runCorpus(t *testing.T, name string, cfg Config) {
	m := New(cfg)
	var table strings.Builder
	fmt.Fprintf(&table, "\n%s\n%-52s %-5s %9s %9s %6s %9s %9s %6s %9s %9s %6s\n", name,
		"url", "type", "raw", "min", "gain", "gzip", "gzip-min", "gain", "br", "br-min", "gain")
	for _, e := range loadCorpus(t) {
		if !m.Handles(e.ContentType) {
			continue
		}
		k := classify(e.ContentType)
		var out bytes.Buffer
		if err := m.Minify(e.ContentType, &out, e.Body); err != nil {
			require.Equal(t, kindJS, k, "only JS may fail on the corpus: %s: %v", e.URL, err)
			require.Zero(t, out.Len(), "nothing may be written on error")
			fmt.Fprintf(&table, "%-52s %-5d %9d  error, original kept: %.60s\n", shortURL(e.URL), k, len(e.Body), strings.SplitN(err.Error(), "\n", 2)[0])
			continue
		}
		min := out.Bytes()
		require.LessOrEqual(t, len(min), len(e.Body), e.URL)
		if m.mode(k) == Safe {
			requireSafeEqual(t, k, e.Body, min, cfg.JS != Off)
		}
		g0, g1 := gzipSize(e.Body), gzipSize(min)
		b0, b1 := brotliSize(e.Body), brotliSize(min)
		pct := func(a, b int) string { return fmt.Sprintf("%5.1f%%", 100*float64(a-b)/float64(a)) }
		fmt.Fprintf(&table, "%-52s %-5d %9d %9d %6s %9d %9d %6s %9d %9d %6s\n", shortURL(e.URL), k,
			len(e.Body), len(min), pct(len(e.Body), len(min)), g0, g1, pct(g0, g1), b0, b1, pct(b0, b1))
	}
	t.Log(table.String())
}

func TestCorpusDefaults(t *testing.T) {
	runCorpus(t, "DefaultConfig (JS off); kinds: 1 html, 2 css, 4 svg, 5 json", DefaultConfig())
}

func TestCorpusAllSafe(t *testing.T) {
	cfg := DefaultConfig()
	cfg.JS = Safe
	cfg.SkipMinified = false
	runCorpus(t, "all Safe, JS Safe, SkipMinified off; kinds: 3 js", cfg)
}
