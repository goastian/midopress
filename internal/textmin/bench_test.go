package textmin

import (
	"io"
	"testing"
)

func benchmarkMinify(b *testing.B, mediaType string, body []byte) {
	cfg := safeConfig()
	cfg.SkipMinified = false
	m := New(cfg)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := m.Minify(mediaType, io.Discard, body); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMinifyHTML(b *testing.B) {
	b.Run("go.dev", func(b *testing.B) {
		for _, e := range loadCorpus(b) {
			if e.URL == "https://go.dev/" {
				benchmarkMinify(b, "text/html", e.Body)
			}
		}
	})
	b.Run("wikipedia.org", func(b *testing.B) {
		for _, e := range loadCorpus(b) {
			if e.URL == "https://www.wikipedia.org/" {
				benchmarkMinify(b, "text/html", e.Body)
			}
		}
	})
}

func BenchmarkMinifyCSS(b *testing.B) {
	benchmarkMinify(b, "text/css", corpusBody(b, "bootstrap.min.css"))
}

func BenchmarkMinifyJS(b *testing.B) {
	b.Run("jquery.js", func(b *testing.B) { benchmarkMinify(b, "text/javascript", corpusBody(b, "jquery/3.7.1/jquery.js")) })
	b.Run("htmx.min.js", func(b *testing.B) { benchmarkMinify(b, "text/javascript", corpusBody(b, "htmx.min.js")) })
}

func BenchmarkMinifySVG(b *testing.B) {
	benchmarkMinify(b, "image/svg+xml", corpusBody(b, "ladder.svg"))
}

func BenchmarkMinifyJSON(b *testing.B) {
	benchmarkMinify(b, "application/json", corpusBody(b, "registry.npmjs.org/left-pad"))
}

func BenchmarkLooksMinified(b *testing.B) {
	body := corpusBody(b, "jquery/3.7.1/jquery.js")
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		LooksMinified(body)
	}
}
