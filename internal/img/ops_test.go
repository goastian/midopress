package img

import (
	"image"
	"image/color"
	"image/color/palette"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// samePixel compares two colors by value, whatever their Go types.
func samePixel(t *testing.T, want, got color.Color, msg ...any) {
	t.Helper()
	wr, wg, wb, wa := want.RGBA()
	gr, gg, gb, ga := got.RGBA()
	assert.Equal(t, [4]uint32{wr, wg, wb, wa}, [4]uint32{gr, gg, gb, ga}, msg...)
}

func labelled(w, h int) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			m.SetNRGBA(x, y, color.NRGBA{R: uint8(x*40 + 10), G: uint8(y*40 + 10), B: uint8(x + y*7), A: 255})
		}
	}
	return m
}

// forward maps a source pixel to where the given EXIF orientation puts it.
func forward(o, x, y, sw, sh int) (int, int) {
	switch o {
	case 2:
		return sw - 1 - x, y
	case 3:
		return sw - 1 - x, sh - 1 - y
	case 4:
		return x, sh - 1 - y
	case 5:
		return y, x
	case 6:
		return sh - 1 - y, x
	case 7:
		return sh - 1 - y, sw - 1 - x
	case 8:
		return y, sw - 1 - x
	}
	return x, y
}

func TestOrientMatchesTheExifDefinition(t *testing.T) {
	src := labelled(5, 3)
	for o := 1; o <= 8; o++ {
		out := Orient(src, o)
		sw, sh := 5, 3
		wantW, wantH := sw, sh
		if o >= 5 {
			wantW, wantH = sh, sw
		}
		require.Equal(t, image.Rect(0, 0, wantW, wantH), out.Bounds(), "orientation %d", o)
		for y := range sh {
			for x := range sw {
				dx, dy := forward(o, x, y, sw, sh)
				samePixel(t, src.At(x, y), out.At(dx, dy), "orientation %d pixel (%d,%d)", o, x, y)
			}
		}
	}
}

func TestOrientRoundTrips(t *testing.T) {
	src := labelled(4, 7)
	turned := image.Image(src)
	for range 4 {
		turned = Orient(turned, 6)
	}
	assert.Equal(t, Normalizee(src).Bounds(), turned.Bounds())
	samePixel(t, src.At(2, 5), turned.At(2, 5))
	samePixel(t, src.At(3, 0), Orient(Orient(src, 5), 5).At(3, 0))
}

// Normalizee is Normalize without the flag, for tests.
func Normalizee(m image.Image) image.Image { n, _ := Normalize(m); return n }

func TestOrientLeavesOtherValuesAlone(t *testing.T) {
	src := labelled(3, 3)
	for _, o := range []int{0, 1, 9, -1} {
		assert.Same(t, src, Orient(src, o))
	}
}

func TestOrientKeepsAlpha(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	src.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 100})
	out := Orient(src, 3)
	assert.IsType(t, &image.NRGBA{}, out)
	assert.Equal(t, color.NRGBA{R: 255, A: 100}, out.(*image.NRGBA).NRGBAAt(1, 1))
}

func TestIsOpaque(t *testing.T) {
	assert.True(t, IsOpaque(image.NewYCbCr(image.Rect(0, 0, 4, 4), image.YCbCrSubsampleRatio420)))
	assert.True(t, IsOpaque(image.NewGray(image.Rect(0, 0, 4, 4))))
	assert.True(t, IsOpaque(labelled(3, 3)))

	n := labelled(3, 3)
	n.SetNRGBA(1, 1, color.NRGBA{A: 254})
	assert.False(t, IsOpaque(n))

	p := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.NRGBA{A: 255}, color.NRGBA{A: 0}})
	p.SetColorIndex(1, 1, 1)
	assert.False(t, IsOpaque(p))
	assert.True(t, IsOpaque(image.NewPaletted(image.Rect(0, 0, 2, 2), palette.Plan9)))
}

func TestNormalize(t *testing.T) {
	t.Run("an opaque RGBA is returned as is", func(t *testing.T) {
		m := image.NewRGBA(image.Rect(0, 0, 3, 3))
		for i := 3; i < len(m.Pix); i += 4 {
			m.Pix[i] = 255
		}
		n, alpha := Normalize(m)
		assert.Same(t, m, n)
		assert.False(t, alpha)
	})
	t.Run("YCbCr becomes RGBA", func(t *testing.T) {
		n, alpha := Normalize(image.NewYCbCr(image.Rect(0, 0, 4, 4), image.YCbCrSubsampleRatio420))
		assert.IsType(t, &image.RGBA{}, n)
		assert.False(t, alpha)
	})
	t.Run("transparency selects NRGBA", func(t *testing.T) {
		p := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.NRGBA{A: 0}, color.NRGBA{R: 9, A: 255}})
		n, alpha := Normalize(p)
		assert.IsType(t, &image.NRGBA{}, n)
		assert.True(t, alpha)
	})
	t.Run("the origin moves to zero", func(t *testing.T) {
		m := labelled(6, 6).SubImage(image.Rect(2, 2, 5, 5))
		n, _ := Normalize(m)
		assert.Equal(t, image.Rect(0, 0, 3, 3), n.Bounds())
		samePixel(t, m.At(2, 2), n.At(0, 0))
	})
}

func TestResize(t *testing.T) {
	src := labelled(40, 30)
	out := Resize(src, 20, 15)
	assert.Equal(t, image.Rect(0, 0, 20, 15), out.Bounds())
	assert.IsType(t, &image.RGBA{}, out, "opaque stays opaque")

	alpha := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	alpha.SetNRGBA(0, 0, color.NRGBA{A: 10})
	assert.IsType(t, &image.NRGBA{}, Resize(alpha, 4, 4))

	flat := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for i := 0; i < len(flat.Pix); i += 4 {
		flat.Pix[i], flat.Pix[i+1], flat.Pix[i+2], flat.Pix[i+3] = 200, 200, 200, 255
	}
	r := Resize(flat, 10, 10).(*image.RGBA)
	assert.Equal(t, uint8(200), r.Pix[0])
	assert.Equal(t, uint8(255), r.Pix[3])
	assert.Equal(t, uint8(200), r.Pix[len(r.Pix)-2])
}

func TestScaledSize(t *testing.T) {
	tests := []struct {
		name    string
		w, h    int
		hints   Hints
		nw, nh  int
		shrinks bool
	}{
		{"no hints", 4000, 3000, Hints{}, 4000, 3000, false},
		{"much larger than the screen", 4000, 3000, Hints{ViewportWidth: 400, DPR: 2}, 800, 600, true},
		{"dpr defaults to 1", 4000, 3000, Hints{ViewportWidth: 1000}, 1000, 750, true},
		{"never enlarged", 300, 200, Hints{ViewportWidth: 1000, DPR: 2}, 300, 200, false},
		{"within 15 percent is kept", 1100, 700, Hints{ViewportWidth: 1000}, 1100, 700, false},
		{"just over 15 percent shrinks", 1200, 800, Hints{ViewportWidth: 1000}, 1000, 667, true},
		{"tiny targets are ignored", 4000, 3000, Hints{ViewportWidth: 20}, 4000, 3000, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nw, nh, ok := scaledSize(tt.w, tt.h, tt.hints)
			assert.Equal(t, tt.shrinks, ok)
			assert.Equal(t, [2]int{tt.nw, tt.nh}, [2]int{nw, nh})
		})
	}
}

func TestSniff(t *testing.T) {
	tests := map[Format][]byte{
		JPEG: {0xFF, 0xD8, 0xFF, 0xE0},
		PNG:  []byte("\x89PNG\r\n\x1a\n...."),
		GIF:  []byte("GIF89a...."),
		WebP: []byte("RIFF\x00\x00\x00\x00WEBPVP8 "),
		AVIF: []byte("\x00\x00\x00\x1cftypavif\x00\x00\x00\x00"),
		JXL:  {0xFF, 0x0A, 0x00},
	}
	for want, b := range tests {
		assert.Equal(t, want, Sniff(b), want.String())
	}
	assert.Equal(t, JXL, Sniff([]byte{0, 0, 0, 0x0C, 'J', 'X', 'L', ' ', 0x0D, 0x0A, 0x87, 0x0A}))
	assert.Equal(t, AVIF, Sniff([]byte("\x00\x00\x00\x20ftypavis\x00\x00\x00\x00")))
	for _, b := range [][]byte{nil, {}, []byte("<html>"), []byte("RIFF....WAVE"), []byte("\x00\x00\x00\x1cftypheic")} {
		assert.Equal(t, Unknown, Sniff(b))
	}
}

func TestCapsFromAccept(t *testing.T) {
	firefox := "image/avif,image/webp,image/png,image/svg+xml,image/*;q=0.8,*/*;q=0.5"
	assert.Equal(t, Caps{WebP: true, AVIF: true}, CapsFromAccept(firefox))
	assert.Equal(t, Caps{WebP: true, AVIF: true, JXL: true}, CapsFromAccept("image/jxl,"+firefox))
	assert.Equal(t, Caps{}, CapsFromAccept("*/*"))
	assert.Equal(t, Caps{}, CapsFromAccept("image/*"))
	assert.Equal(t, Caps{}, CapsFromAccept("image/webp;q=0"))
	c := Caps{WebP: true}
	assert.True(t, c.Has(WebP))
	assert.False(t, c.Has(AVIF))
	assert.True(t, c.Has(JPEG))
	assert.False(t, c.Has(Unknown))
}

func TestFormatStrings(t *testing.T) {
	assert.Equal(t, "image/avif", AVIF.MIME())
	assert.Equal(t, "avif", AVIF.String())
	assert.Equal(t, "", Unknown.MIME())
	assert.Equal(t, "unknown", Format(99).String())
}

func TestProfiles(t *testing.T) {
	ps := Profiles()
	for _, name := range []string{ProfileLossless, ProfileBalanced, ProfileSaver, ProfileExtreme} {
		p, err := Lookup(name)
		require.NoError(t, err)
		assert.Equal(t, name, p.Name)
		assert.NotNil(t, ps[name])
	}
	_, err := Lookup("turbo")
	assert.Error(t, err)

	// the profiles must get stricter in the order a person expects
	assert.True(t, ps[ProfileLossless].Lossless)
	assert.Greater(t, ps[ProfileBalanced].TargetSSIM, ps[ProfileSaver].TargetSSIM)
	assert.Greater(t, ps[ProfileSaver].TargetSSIM, ps[ProfileExtreme].TargetSSIM)
	assert.False(t, ps[ProfileBalanced].ResizeToViewport, "balanced never changes the dimensions")
	assert.True(t, ps[ProfileSaver].ResizeToViewport)
	assert.True(t, ps[ProfileExtreme].AnimatedToStill)
	assert.False(t, ps[ProfileSaver].AnimatedToStill)
	assert.GreaterOrEqual(t, ps[ProfileExtreme].Text.BrotliStatic, ps[ProfileBalanced].Text.BrotliStatic)
}
