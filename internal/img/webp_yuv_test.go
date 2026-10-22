package img

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/gen2brain/webp"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/quality"
)

// The reference PNGs are what libwebp's dwebp makes of the files, which is what
// a browser shows. The conversion has to give the same bytes, not nearly.
func TestWebPDecodeIsWhatLibwebpMakes(t *testing.T) {
	be := NewWASM()
	for _, name := range []string{"opaque", "alpha"} {
		data, err := os.ReadFile(filepath.Join("testdata", name+".webp"))
		require.NoError(t, err)
		f, err := os.Open(filepath.Join("testdata", name+".ref.png"))
		require.NoError(t, err)
		want, err := png.Decode(f)
		_ = f.Close()
		require.NoError(t, err)

		got, err := be.Decode(WebP, data)
		require.NoError(t, err)
		require.Equal(t, want.Bounds().Size(), got.Bounds().Size(), name)
		for y := range want.Bounds().Dy() {
			for x := range want.Bounds().Dx() {
				wr, wg, wb, wa := color.NRGBAModel.Convert(want.At(x, y)).RGBA()
				gr, gg, gb, ga := color.NRGBAModel.Convert(got.At(x, y)).RGBA()
				if wa == 0 && ga == 0 {
					continue
				}
				require.Equal(t, [4]uint32{wr, wg, wb, wa}, [4]uint32{gr, gg, gb, ga}, "%s at (%d,%d)", name, x, y)
			}
		}

		raw, err := webp.Decode(bytes.NewReader(data))
		require.NoError(t, err)
		dRaw, _ := quality.DSSIM(want, raw)
		dOurs, err := quality.DSSIM(want, got)
		require.NoError(t, err)
		assert.Zero(t, dOurs)
		t.Logf("%s: the decoder as it comes is %.5f away from libwebp, ours %.5f", name, dRaw, dOurs)
		assert.Greater(t, dRaw, 0.0005, "which is why the conversion exists")
	}
}

func TestWebPToRGBAHandlesAlphaAndOddSizes(t *testing.T) {
	be := NewWASM()
	for _, size := range [][2]int{{33, 17}, {1, 1}, {2, 3}, {64, 64}} {
		n := image.NewNRGBA(image.Rect(0, 0, size[0], size[1]))
		for i := range n.Pix {
			n.Pix[i] = uint8(i * 7)
		}
		for i := 3; i < len(n.Pix); i += 4 {
			n.Pix[i] = uint8(i)
		}
		data, err := be.Encode(WebP, n, EncodeOptions{Quality: 90, Effort: 2})
		require.NoError(t, err)
		got, err := be.Decode(WebP, data)
		require.NoError(t, err)
		assert.Equal(t, image.Rect(0, 0, size[0], size[1]), got.Bounds(), "%v", size)
	}
}
