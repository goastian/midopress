package pngopt

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func benchPhoto(b *testing.B) (asis, goenc []byte, img image.Image) {
	b.Helper()
	data, err := os.ReadFile("/tmp/opencode/img/kodim01.png")
	if err != nil {
		b.Skip("kodim01.png not available")
	}
	img = decodeStd(b, data)
	return data, nrgbaToPNG(b, img), img
}

func flatGraphic1000() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 1000, 1000))
	for y := range 1000 {
		for x := range 1000 {
			c := color.NRGBA{250, 250, 250, 255}
			switch {
			case (x/125+y/125)%2 == 0:
				c = color.NRGBA{30, 90, 200, 255}
			case x > 400 && x < 600 && y > 300 && y < 700:
				c = color.NRGBA{220, 60, 40, 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func BenchmarkOptimizePhoto(b *testing.B) {
	asis, goenc, _ := benchPhoto(b)
	for _, in := range []struct {
		name string
		data []byte
	}{{"asis", asis}, {"goenc", goenc}} {
		for _, lvl := range []Level{Fast, Default, Best} {
			b.Run(fmt.Sprintf("%s/level=%d", in.name, lvl), func(b *testing.B) {
				b.SetBytes(int64(len(in.data)))
				b.ReportAllocs()
				var out []byte
				for b.Loop() {
					var err error
					out, err = optimize(in.data, lvl)
					if err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(len(out)), "out-bytes")
			})
		}
	}
}

func BenchmarkOptimizeFlat(b *testing.B) {
	in := nrgbaToPNG(b, flatGraphic1000())
	for _, lvl := range []Level{Fast, Default, Best} {
		b.Run(fmt.Sprintf("level=%d", lvl), func(b *testing.B) {
			b.SetBytes(int64(len(in)))
			b.ReportAllocs()
			var out []byte
			for b.Loop() {
				var err error
				out, err = optimize(in, lvl)
				if err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(len(out)), "out-bytes")
		})
	}
}

func BenchmarkQuantize(b *testing.B) {
	_, _, img := benchPhoto(b)
	for _, d := range []float64{-1, 0.5} {
		b.Run(fmt.Sprintf("dither=%.1f", d), func(b *testing.B) {
			b.SetBytes(int64(img.Bounds().Dx() * img.Bounds().Dy() * 4))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := Quantize(img, QuantizeOptions{Colors: 256, Dither: d}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestSizeReport(t *testing.T) {
	flat := nrgbaToPNG(t, flatGraphic1000())
	for lvl := Fast; lvl <= Best; lvl++ {
		out, err := optimize(flat, lvl)
		require.NoError(t, err)
		requireSamePixels(t, flat, out)
		opti := "-"
		if lvl == Default {
			if n, ok := optipngSize(t, flat, "-o2"); ok {
				opti = fmt.Sprint(n)
			}
		}
		t.Logf("flat 1000x1000 level=%d: in=%d out=%d optipng-o2=%s", lvl, len(flat), len(out), opti)
	}
	data, err := os.ReadFile("/tmp/opencode/img/kodim01.png")
	if err != nil {
		t.Skip("kodim01.png not available")
	}
	for lvl := Fast; lvl <= Default; lvl++ {
		out, err := optimize(data, lvl)
		require.NoError(t, err)
		t.Logf("kodim01 level=%d: in=%d out=%d", lvl, len(data), len(out))
	}
}
