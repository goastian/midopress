package pngopt

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func toNRGBAImage(img image.Image) *image.NRGBA {
	out := image.NewNRGBA(img.Bounds())
	draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Src)
	return out
}

// psnr computes the PSNR over RGB of two images composited on white.
func psnr(a, b image.Image) float64 {
	bd := a.Bounds()
	var se float64
	n := 0
	for y := bd.Min.Y; y < bd.Max.Y; y++ {
		for x := bd.Min.X; x < bd.Max.X; x++ {
			ra, ga, ba, aa := a.At(x, y).RGBA()
			rb, gb, bb, ab := b.At(x, y).RGBA()
			comp := func(c, al uint32) float64 { return float64(c+(0xffff-al)) / 257 }
			for _, d := range [][2]float64{{comp(ra, aa), comp(rb, ab)}, {comp(ga, aa), comp(gb, ab)}, {comp(ba, aa), comp(bb, ab)}} {
				se += (d[0] - d[1]) * (d[0] - d[1])
				n++
			}
		}
	}
	if se == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255/(se/float64(n)))
}

func flatGraphic(w, h, colours int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			k := (x/10)*7 + (y/10)*13
			c := k % colours
			img.SetNRGBA(x, y, color.NRGBA{uint8(c * 37), uint8(c * 91 >> 1), uint8(c * 53), 255})
		}
	}
	return img
}

func TestQuantizeExactWhenFewColours(t *testing.T) {
	src := flatGraphic(80, 80, 20)
	q, err := Quantize(src, QuantizeOptions{Colors: 32})
	require.NoError(t, err)
	require.LessOrEqual(t, len(q.Palette), 32)
	require.True(t, math.IsInf(psnr(src, q), 1), "palette must be exact")
}

func TestQuantizeFlatGraphicHasNoNoise(t *testing.T) {
	src := flatGraphic(200, 200, 300)
	q, err := Quantize(src, QuantizeOptions{Colors: 64, Dither: 1})
	require.NoError(t, err)
	require.LessOrEqual(t, len(q.Palette), 64)
	for by := 0; by < 20; by++ {
		for bx := 0; bx < 20; bx++ {
			ref := q.ColorIndexAt(bx*10, by*10)
			for y := by * 10; y < by*10+10; y++ {
				for x := bx * 10; x < bx*10+10; x++ {
					require.Equal(t, ref, q.ColorIndexAt(x, y), "block (%d,%d) is not uniform", bx, by)
				}
			}
		}
	}
}

func TestQuantizeAlpha(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 120, 120))
	for y := range 120 {
		for x := range 120 {
			d := math.Hypot(float64(x-60), float64(y-60))
			switch {
			case d > 55:
			case d > 50:
				src.SetNRGBA(x, y, color.NRGBA{uint8(x * 2), uint8(y * 2), 90, uint8(255 * (55 - d) / 5)})
			default:
				src.SetNRGBA(x, y, color.NRGBA{uint8(x * 2), uint8(y * 2), uint8(x + y), 255})
			}
		}
	}
	q, err := Quantize(src, QuantizeOptions{Colors: 16})
	require.NoError(t, err)
	require.LessOrEqual(t, len(q.Palette), 16)
	for y := range 120 {
		for x := range 120 {
			_, _, _, a := q.At(x, y).RGBA()
			_, _, _, a0 := src.At(x, y).RGBA()
			if a0 == 0 {
				require.Zero(t, a, "transparent pixel (%d,%d) must stay transparent", x, y)
			}
			if a0 == 0xffff {
				require.Equal(t, uint32(0xffff), a, "opaque pixel (%d,%d) must stay opaque", x, y)
			}
		}
	}
}

func TestQuantizeDeterministicAndEncodes(t *testing.T) {
	src := decodeStd(t, nrgbaToPNG(t, noisyGradient(150, 100)))
	a, err := Quantize(src, QuantizeOptions{Colors: 100})
	require.NoError(t, err)
	b, err := Quantize(src, QuantizeOptions{Colors: 100})
	require.NoError(t, err)
	require.Equal(t, a.Pix, b.Pix)
	require.Equal(t, a.Palette, b.Palette)
	data, err := Encode(a, Default)
	require.NoError(t, err)
	got := decodeStd(t, data)
	require.Equal(t, a.Bounds(), got.Bounds())
	for y := range 100 {
		for x := range 150 {
			require.Equal(t, color.NRGBA64Model.Convert(a.At(x, y)), color.NRGBA64Model.Convert(got.At(x, y)))
		}
	}
	require.Equal(t, 3, int(ihdrOf(t, data).ctype))
}

func noisyGradient(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	r := noise(3)
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x*255/w + r.IntN(6)), uint8(y*255/h + r.IntN(6)), uint8((x + y) % 256), 255})
		}
	}
	return img
}

func TestQuantizeOptions(t *testing.T) {
	src := flatGraphic(10, 10, 5)
	_, err := Quantize(src, QuantizeOptions{Colors: 1})
	require.Error(t, err)
	_, err = Quantize(src, QuantizeOptions{Colors: 257})
	require.Error(t, err)
	q, err := Quantize(noisyGradient(40, 40), QuantizeOptions{Colors: 2, Dither: -1})
	require.NoError(t, err)
	require.LessOrEqual(t, len(q.Palette), 2)
}

func TestQuantizeKodak(t *testing.T) {
	imgs := kodakPNGs(t)
	name := sortedKeys(imgs)[0]
	src := toNRGBAImage(decodeStd(t, imgs[name]))
	for _, dither := range []float64{-1, 0.5, 1} {
		q, err := Quantize(src, QuantizeOptions{Colors: 256, Dither: dither})
		require.NoError(t, err)
		p := psnr(src, q)
		data, err := Encode(q, Default)
		require.NoError(t, err)
		t.Logf("%s 256 colours dither=%.1f psnr=%.2f dB size=%d (lossless input %d)", name, dither, p, len(data), len(imgs[name]))
		require.Greater(t, p, 28.0)
	}
	if ffmpegPSNR, ok := ffmpegPaletteGen(t, src, 256); ok {
		t.Logf("ffmpeg palettegen/paletteuse (sierra2_4a) psnr=%.2f dB", ffmpegPSNR)
	}
}

func ffmpegPaletteGen(t *testing.T, src *image.NRGBA, colors int) (float64, bool) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return 0, false
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "in.png")
	require.NoError(t, os.WriteFile(in, nrgbaToPNG(t, src), 0o600))
	out := filepath.Join(dir, "out.png")
	filter := fmt.Sprintf("split[a][b];[a]palettegen=max_colors=%d[p];[b][p]paletteuse", colors)
	if err := exec.Command("ffmpeg", "-v", "error", "-y", "-i", in, "-vf", filter, out).Run(); err != nil {
		return 0, false
	}
	cmd := exec.Command("ffmpeg", "-i", in, "-i", out, "-lavfi", "psnr", "-f", "null", "-")
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return 0, false
	}
	m := regexp.MustCompile(`average:([0-9.]+)`).FindSubmatch(errb.Bytes())
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(string(m[1]), 64)
	return v, err == nil
}

func TestEncodeImageTypes(t *testing.T) {
	r := noise(9)
	nrgba := image.NewNRGBA(image.Rect(0, 0, 33, 21))
	rgba := image.NewRGBA(image.Rect(0, 0, 33, 21))
	gray := image.NewGray(image.Rect(0, 0, 33, 21))
	gray16 := image.NewGray16(image.Rect(0, 0, 33, 21))
	nrgba64 := image.NewNRGBA64(image.Rect(0, 0, 33, 21))
	pal := image.NewPaletted(image.Rect(0, 0, 33, 21), color.Palette{color.NRGBA{1, 2, 3, 255}, color.NRGBA{9, 9, 9, 0}, color.NRGBA{200, 100, 50, 128}, color.NRGBA{1, 2, 3, 255}})
	ycc := image.NewYCbCr(image.Rect(0, 0, 33, 21), image.YCbCrSubsampleRatio420)
	for y := range 21 {
		for x := range 33 {
			v := uint8(r.IntN(4) * 60)
			nrgba.SetNRGBA(x, y, color.NRGBA{v, uint8(x), 7, uint8(255 - r.IntN(2)*100)})
			rgba.SetRGBA(x, y, color.RGBA{v / 2, v / 3, v / 4, v})
			gray.SetGray(x, y, color.Gray{v})
			gray16.SetGray16(x, y, color.Gray16{uint16(r.IntN(70000))})
			nrgba64.SetNRGBA64(x, y, color.NRGBA64{uint16(x * 1001), uint16(y * 3001), uint16(r.IntN(65536)), uint16(65535 - x*100)})
			pal.SetColorIndex(x, y, uint8(r.IntN(4)))
			ycc.Y[ycc.YOffset(x, y)] = v
		}
	}
	imgs := map[string]image.Image{"nrgba": nrgba, "rgba": rgba, "gray": gray, "gray16": gray16, "nrgba64": nrgba64, "paletted": pal, "ycbcr": ycc}
	for name, img := range imgs {
		for _, lvl := range []Level{Fast, Default, Best} {
			data, err := Encode(img, lvl)
			require.NoError(t, err, name)
			got := decodeStd(t, data)
			require.Equal(t, img.Bounds(), got.Bounds())
			for y := range 21 {
				for x := range 33 {
					want := color.NRGBA64Model.Convert(img.At(x, y)).(color.NRGBA64)
					have := color.NRGBA64Model.Convert(got.At(x, y)).(color.NRGBA64)
					if name == "rgba" || name == "ycbcr" {
						// premultiplied or sub-8-bit input is rounded to 8-bit straight alpha
						require.InDelta(t, int(want.R), int(have.R), 400, "%s (%d,%d)", name, x, y)
						require.InDelta(t, int(want.G), int(have.G), 400, "%s (%d,%d)", name, x, y)
						require.InDelta(t, int(want.B), int(have.B), 400, "%s (%d,%d)", name, x, y)
						require.Equal(t, want.A, have.A)
						continue
					}
					require.Equal(t, want, have, "%s (%d,%d)", name, x, y)
				}
			}
		}
	}
	data, err := Encode(pal, Best)
	require.NoError(t, err)
	h := ihdrOf(t, data)
	require.Equal(t, 3, int(h.ctype))
	require.Equal(t, 2, int(h.depth), "3 distinct colours need 2 bits")
}

func TestEncodeRejectsHuge(t *testing.T) {
	_, err := Encode(&image.Gray{Rect: image.Rect(0, 0, 1<<14, 1<<14)}, Fast)
	require.ErrorIs(t, err, ErrTooLarge)
	_, err = Encode(image.NewGray(image.Rect(0, 0, 0, 5)), Fast)
	require.Error(t, err)
}

func TestFromImageRGBAMatchesModel(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 256, 3))
	for a := range 256 {
		for y := range 3 {
			v := uint8(min(a, 255-y*40))
			img.SetRGBA(a, y, color.RGBA{v, uint8(a) / 2, uint8(a) / 3, uint8(a)})
		}
	}
	r, err := fromImage(img)
	require.NoError(t, err)
	for y := range 3 {
		for x := range 256 {
			want := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			if want.A == 0 {
				want = color.NRGBA{}
			}
			o := (y*256 + x) * 4
			require.Equal(t, []byte{want.R, want.G, want.B, want.A}, r.pix[o:o+4], "(%d,%d)", x, y)
		}
	}
}
