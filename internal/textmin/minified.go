package textmin

const (
	minifiedMinSize     = 1024
	minifiedMaxAvgLine  = 150
	minifiedMaxFirst    = 2000
	minifiedFirstSpace  = 12
	minifiedAvgSpacePct = 8
)

// LooksMinified reports whether src is probably already minified, so that
// minifying it again would waste CPU. It makes a single pass without
// allocating. Text is considered minified when it has very long lines
// (on average, or a very long first line) and a low share of whitespace.
// Inputs under 1 KiB and inputs containing NUL bytes are never reported
// as minified.
func LooksMinified(src []byte) bool {
	if len(src) < minifiedMinSize {
		return false
	}
	newlines, spaces, first := 0, 0, -1
	for i, c := range src {
		switch c {
		case 0:
			return false
		case '\n':
			if first < 0 {
				first = i
			}
			newlines++
			spaces++
		case ' ', '\t', '\r':
			spaces++
		}
	}
	if first < 0 {
		first = len(src)
	}
	pct := spaces * 100 / len(src)
	return first > minifiedMaxFirst && pct <= minifiedFirstSpace ||
		len(src)/(newlines+1) > minifiedMaxAvgLine && pct <= minifiedAvgSpacePct
}
