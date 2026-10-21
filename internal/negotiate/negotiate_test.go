package negotiate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseAcceptEncoding(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   Encodings
	}{
		{"empty", "", 0},
		{"firefox https", "gzip, deflate, br, zstd", Gzip | Deflate | Brotli | Zstd},
		{"no spaces", "gzip,deflate,br", Gzip | Deflate | Brotli},
		{"firefox http", "gzip, deflate", Gzip | Deflate},
		{"uppercase", "GZIP, Br", Gzip | Brotli},
		{"q zero removes", "gzip, br;q=0", Gzip},
		{"q zero decimals", "gzip;q=0.000, br", Brotli},
		{"q fraction keeps", "br;q=0.5, gzip;q=0.1", Brotli | Gzip},
		{"identity only", "identity", 0},
		{"wildcard is not enough", "*", 0},
		{"wildcard with explicit", "*;q=0.1, gzip", Gzip},
		{"x-gzip alias", "x-gzip", Gzip},
		{"spaces around params", "br ; q = 0.8", Brotli},
		{"extra params", "br;foo=bar;q=0", 0},
		{"malformed q counts as accepted", "br;q=abc", Brotli},
		{"trailing comma", "gzip,", Gzip},
		{"unknown coding", "compress, dcb", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ParseAcceptEncoding(tt.header))
		})
	}
}

func TestEncodingsHas(t *testing.T) {
	e := Gzip | Brotli
	assert.True(t, e.Has(Gzip))
	assert.True(t, e.Has(Gzip|Brotli))
	assert.False(t, e.Has(Zstd))
	assert.False(t, e.Has(Gzip|Zstd))
}

func TestAcceptsMedia(t *testing.T) {
	tests := []struct {
		header string
		media  string
		want   bool
	}{
		{"image/webp", "image/webp", true},
		{"image/avif,image/webp,image/apng,*/*;q=0.8", "image/webp", true},
		{"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", "image/webp", false},
		{"image/webp;q=0", "image/webp", false},
		{"image/*", "image/webp", false},
		{"*/*", "image/webp", false},
		{"IMAGE/WEBP", "image/webp", true},
		{"", "image/webp", false},
		{"image/webp ; q=0.5", "image/webp", true},
	}
	for _, tt := range tests {
		t.Run(tt.header, func(t *testing.T) {
			assert.Equal(t, tt.want, AcceptsMedia(tt.header, tt.media))
		})
	}
}

func TestParseQuality(t *testing.T) {
	assert.Equal(t, 1000, parseQuality("1"))
	assert.Equal(t, 1000, parseQuality("1.000"))
	assert.Equal(t, 0, parseQuality("0"))
	assert.Equal(t, 500, parseQuality("0.5"))
	assert.Equal(t, 123, parseQuality("0.123"))
	assert.Equal(t, 120, parseQuality("0.12"))
	assert.Equal(t, 1000, parseQuality("7"))
}

func TestNoAllocations(t *testing.T) {
	const ae = "gzip, deflate, br, zstd"
	const accept = "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8"
	allocs := testing.AllocsPerRun(100, func() {
		_ = ParseAcceptEncoding(ae)
		_ = AcceptsMedia(accept, "image/webp")
	})
	assert.Zero(t, allocs)
}

func BenchmarkParseAcceptEncoding(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = ParseAcceptEncoding("gzip, deflate, br, zstd")
	}
}
