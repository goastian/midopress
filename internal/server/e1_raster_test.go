package server_test

import (
	"bytes"
	"image"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/img"
)

// This validator checks decoded canvases, not color transforms. It must accept
// PNG (which is not a Backend.Decode format) and plain modern output without
// adding an unrelated full-image CMS deadline to a network benchmark.
func TestE1RasterValidatorHandlesPortableRasterFormats(t *testing.T) {
	pixels, err := png.Decode(bytes.NewReader(fix().png))
	require.NoError(t, err)
	for _, fixture := range []struct {
		name string
		raw  []byte
	}{{"jpeg", fix().jpeg}, {"png", fix().png}, {"gif", fix().gifStatic}} {
		t.Run(fixture.name, func(t *testing.T) {
			size := e1RasterCanvas(t, fixture.raw)
			assert.Positive(t, size.X)
			assert.Positive(t, size.Y)
		})
	}
	for _, format := range []img.Format{img.WebP, img.AVIF} {
		t.Run(format.String(), func(t *testing.T) {
			raw, err := img.NewWASM().Encode(format, pixels, img.EncodeOptions{Quality: 90, Effort: 1})
			require.NoError(t, err)
			assert.Equal(t, image.Pt(320, 240), e1RasterCanvas(t, raw))
		})
	}
}
