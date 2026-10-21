package quality

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// opaqueImage hides the concrete type so that the generic At() path is used.
type opaqueImage struct{ image.Image }

func loadPhoto(tb testing.TB) *image.RGBA {
	tb.Helper()
	data, err := os.ReadFile("testdata/photo.jpg")
	require.NoError(tb, err)
	img, err := jpeg.Decode(bytes.NewReader(data))
	require.NoError(tb, err)
	return toRGBA(img)
}

func toRGBA(img image.Image) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
	draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Src)
	return out
}

// synthPhoto builds a photo-like picture: smooth gradients, a few edges and
// fine texture.
func synthPhoto(w, h int, seed uint64) *image.RGBA {
	rng := rand.New(rand.NewPCG(seed, 1))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			fx, fy := float64(x)/float64(w), float64(y)/float64(h)
			base := 110 + 70*math.Sin(6*fx+2*fy) + 40*math.Cos(11*fy-3*fx)
			if (x/37+y/29)%3 == 0 {
				base += 35
			}
			tex := 9 * (rng.Float64() - 0.5)
			r := base + tex + 25*fx
			g := base*0.9 + tex - 15*fy
			b := base*0.7 + tex + 30*math.Sin(9*fx)
			img.SetRGBA(x, y, color.RGBA{clamp8(r), clamp8(g), clamp8(b), 255})
		}
	}
	return img
}

func clamp8(v float64) uint8 {
	return uint8(math.Round(min(max(v, 0), 255)))
}

// noisy adds the same unit-variance-ish noise pattern scaled by sigma.
func noisy(src *image.RGBA, sigma float64) *image.RGBA {
	rng := rand.New(rand.NewPCG(42, 7))
	out := image.NewRGBA(src.Rect)
	for i := 0; i < len(src.Pix); i += 4 {
		n := (rng.Float64() + rng.Float64() + rng.Float64() + rng.Float64() - 2) * math.Sqrt(3)
		for k := range 3 {
			out.Pix[i+k] = clamp8(float64(src.Pix[i+k]) + sigma*n)
		}
		out.Pix[i+3] = 255
	}
	return out
}

func boxBlur(src *image.RGBA, radius int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	tmp := image.NewRGBA(src.Rect)
	out := image.NewRGBA(src.Rect)
	pass := func(dst, from *image.RGBA, dx, dy int) {
		for y := range h {
			for x := range w {
				var sum [3]int
				n := 0
				for k := -radius; k <= radius; k++ {
					xx, yy := x+k*dx, y+k*dy
					if xx < 0 || yy < 0 || xx >= w || yy >= h {
						continue
					}
					o := from.PixOffset(xx, yy)
					sum[0] += int(from.Pix[o])
					sum[1] += int(from.Pix[o+1])
					sum[2] += int(from.Pix[o+2])
					n++
				}
				o := dst.PixOffset(x, y)
				dst.Pix[o] = uint8(sum[0] / n)
				dst.Pix[o+1] = uint8(sum[1] / n)
				dst.Pix[o+2] = uint8(sum[2] / n)
				dst.Pix[o+3] = 255
			}
		}
	}
	pass(tmp, src, 1, 0)
	pass(out, tmp, 0, 1)
	return out
}

func jpegRoundTrip(tb testing.TB, src image.Image, q int) image.Image {
	tb.Helper()
	var buf bytes.Buffer
	require.NoError(tb, jpeg.Encode(&buf, src, &jpeg.Options{Quality: q}))
	out, err := jpeg.Decode(&buf)
	require.NoError(tb, err)
	return out
}

func randomRGBA(w, h int, seed uint64) *image.RGBA {
	rng := rand.New(rand.NewPCG(seed, 99))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i] = uint8(rng.UintN(256))
		img.Pix[i+1] = uint8(rng.UintN(256))
		img.Pix[i+2] = uint8(rng.UintN(256))
		img.Pix[i+3] = 255
	}
	return img
}
