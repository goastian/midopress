package textmin

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func newConfig(html, css, js, svg, json Mode) Config {
	return Config{HTML: html, CSS: css, JS: js, SVG: svg, JSON: json}
}

func safeConfig() Config { return newConfig(Safe, Safe, Safe, Safe, Safe) }

func aggressiveConfig() Config {
	return newConfig(Aggressive, Aggressive, Aggressive, Aggressive, Aggressive)
}

// minifyString minifies in and verifies that the input slice is not modified.
func minifyString(t testing.TB, m *Minifier, mediaType, in string) string {
	t.Helper()
	src := []byte(in)
	var out bytes.Buffer
	require.NoError(t, m.Minify(mediaType, &out, src))
	require.Equal(t, in, string(src), "Minify modified its input")
	return out.String()
}

type countingWriter struct {
	writes int
	bytes  int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.writes++
	w.bytes += len(p)
	return len(p), nil
}
