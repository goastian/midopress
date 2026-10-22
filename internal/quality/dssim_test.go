package quality

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dsOpaque hides the concrete type so that the generic At() path is used.
type dsOpaque struct{ image.Image }

func dsBinary(tb testing.TB) string {
	tb.Helper()
	if p, err := exec.LookPath("dssim"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".cargo", "bin", "dssim")
	if _, err := os.Stat(p); err != nil {
		tb.Skip("reference dssim binary not available")
	}
	return p
}

// dsShrink crops big test images when the race detector makes the pixel loops
// of the standard library very slow.
func dsShrink(img image.Image) image.Image {
	if !dsRaceEnabled {
		return img
	}
	r := image.Rect(0, 0, min(img.Bounds().Dx(), 224), min(img.Bounds().Dy(), 160)).Add(img.Bounds().Min)
	switch m := img.(type) {
	case *image.NRGBA:
		out := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
		draw.Draw(out, out.Bounds(), m, r.Min, draw.Src)
		return out
	default:
		out := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
		draw.Draw(out, out.Bounds(), img, r.Min, draw.Src)
		return out
	}
}

func dsLoadPNG(tb testing.TB, path string) image.Image {
	tb.Helper()
	img, err := dsDecode(path, png.Decode)
	if os.IsNotExist(err) {
		tb.Skipf("test image not available: %v", err)
	}
	require.NoError(tb, err)
	return dsShrink(img)
}

func dsDecode(path string, decode func(io.Reader) (image.Image, error)) (image.Image, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decode(bytes.NewReader(data))
}

func dsWritePNG(tb testing.TB, path string, img image.Image) {
	tb.Helper()
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	require.NoError(tb, enc.Encode(&buf, img))
	require.NoError(tb, os.WriteFile(path, buf.Bytes(), 0o600))
}

func dsRunBinary(tb testing.TB, bin, a, b string) float64 {
	tb.Helper()
	out, err := exec.Command(bin, a, b).CombinedOutput()
	require.NoError(tb, err, string(out))
	fields := strings.Fields(string(out))
	require.NotEmpty(tb, fields, string(out))
	v, err := strconv.ParseFloat(fields[0], 64)
	require.NoError(tb, err, string(out))
	return v
}

func dsToNRGBA(img image.Image) *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
	draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Src)
	return out
}

func dsJPEG(tb testing.TB, src image.Image, q int) image.Image {
	tb.Helper()
	var buf bytes.Buffer
	require.NoError(tb, jpeg.Encode(&buf, src, &jpeg.Options{Quality: q}))
	out, err := jpeg.Decode(&buf)
	require.NoError(tb, err)
	return toRGBAForTest(out)
}

// dsMap applies f to every pixel of an NRGBA copy of src.
func dsMap(src image.Image, f func(x, y int, c color.NRGBA) color.NRGBA) *image.NRGBA {
	out := dsToNRGBA(src)
	w, h := out.Rect.Dx(), out.Rect.Dy()
	for y := range h {
		row := out.Pix[y*out.Stride : y*out.Stride+4*w]
		for x := range w {
			p := row[4*x : 4*x+4]
			c := f(x, y, color.NRGBA{R: p[0], G: p[1], B: p[2], A: p[3]})
			p[0], p[1], p[2], p[3] = c.R, c.G, c.B, c.A
		}
	}
	return out
}

func dsClamp(v float64) uint8 { return uint8(math.Round(min(max(v, 0), 255))) }

func dsNoise(src image.Image, sigma float64) *image.NRGBA {
	rng := rand.New(rand.NewPCG(5, 6))
	return dsMap(src, func(_, _ int, c color.NRGBA) color.NRGBA {
		n := rng.NormFloat64() * sigma
		c.R, c.G, c.B = dsClamp(float64(c.R)+n), dsClamp(float64(c.G)+n), dsClamp(float64(c.B)+n)
		return c
	})
}

func dsBlur(src image.Image, radius int) *image.NRGBA {
	in := dsToNRGBA(src)
	b := in.Bounds()
	w, h := b.Dx(), b.Dy()
	tmp := image.NewNRGBA(in.Rect)
	out := image.NewNRGBA(in.Rect)
	pass := func(dst, from *image.NRGBA, dx, dy int) {
		for y := range h {
			for x := range w {
				var sum [4]int
				n := 0
				for k := -radius; k <= radius; k++ {
					xx, yy := x+k*dx, y+k*dy
					if xx < 0 || yy < 0 || xx >= w || yy >= h {
						continue
					}
					o := from.PixOffset(xx, yy)
					for c := range 4 {
						sum[c] += int(from.Pix[o+c])
					}
					n++
				}
				o := dst.PixOffset(x, y)
				for c := range 4 {
					dst.Pix[o+c] = uint8(sum[c] / n)
				}
			}
		}
	}
	pass(tmp, in, 1, 0)
	pass(out, tmp, 0, 1)
	return out
}

func dsChannelDamage(src image.Image) *image.NRGBA {
	return dsMap(src, func(_, _ int, c color.NRGBA) color.NRGBA {
		c.R = dsClamp(float64(c.R)*0.85 + 12)
		c.B = c.B &^ 0x3f
		return c
	})
}

func dsAlphaDamage(src image.Image) *image.NRGBA {
	return dsMap(src, func(x, y int, c color.NRGBA) color.NRGBA {
		if (x/7+y/5)%4 == 0 {
			c.A /= 2
		}
		return c
	})
}

type dsPair struct {
	name     string
	ref, mod image.Image
}

func dsCorpusPairs(tb testing.TB) []dsPair {
	tb.Helper()
	var pairs []dsPair
	kodim := []string{"kodim01", "kodim05", "kodim13", "kodim20"}
	alpha := []string{"graphic-alpha-demo", "gallery3-3", "graphic-icon", "graphic-logo"}
	if dsRaceEnabled {
		kodim, alpha = kodim[:1], alpha[:2]
	}
	for _, name := range kodim {
		ref := dsLoadPNG(tb, "/tmp/opencode/img/"+name+".png")
		for _, q := range []int{95, 85, 70, 50, 30} {
			pairs = append(pairs, dsPair{fmt.Sprintf("%s jpeg q%d", name, q), ref, dsJPEG(tb, ref, q)})
		}
		pairs = append(pairs,
			dsPair{name + " blur r1", ref, dsBlur(ref, 1)},
			dsPair{name + " blur r3", ref, dsBlur(ref, 3)},
			dsPair{name + " noise s4", ref, dsNoise(ref, 4)},
			dsPair{name + " noise s16", ref, dsNoise(ref, 16)},
			dsPair{name + " channel damage", ref, dsChannelDamage(ref)},
		)
	}
	for _, name := range []string{"photo-eiffel.jpg", "photo-lion.jpg"} {
		dec, err := dsDecode("/tmp/opencode/corpus/"+name, jpeg.Decode)
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(tb, err)
		ref := toRGBAForTest(dsShrink(dec))
		pairs = append(pairs,
			dsPair{name + " jpeg q60", ref, dsJPEG(tb, ref, 60)},
			dsPair{name + " noise s8", ref, dsNoise(ref, 8)},
		)
	}
	for _, name := range alpha {
		dec, err := dsDecode("/tmp/opencode/corpus/"+name+".png", png.Decode)
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(tb, err)
		ref := dsShrink(dec)
		pairs = append(pairs,
			dsPair{name + " alpha damage", ref, dsAlphaDamage(ref)},
			dsPair{name + " blur r2 (alpha too)", ref, dsBlur(ref, 2)},
			dsPair{name + " noise s10", ref, dsNoise(ref, 10)},
		)
	}
	return pairs
}

func dsRelErr(got, want float64) (abs, rel float64) {
	abs = math.Abs(got - want)
	if want != 0 {
		rel = abs / want
	}
	return abs, rel
}

func TestDSSIMAgreesWithReference(t *testing.T) {
	bin := dsBinary(t)
	pairs := dsCorpusPairs(t)
	require.GreaterOrEqual(t, len(pairs), 12)
	dir := t.TempDir()

	var maxRel, maxAbs float64
	var table strings.Builder
	written := map[image.Image]string{}
	for i, p := range pairs {
		a, ok := written[p.ref]
		if !ok {
			a = filepath.Join(dir, fmt.Sprintf("%da.png", i))
			dsWritePNG(t, a, p.ref)
			written[p.ref] = a
		}
		b := filepath.Join(dir, fmt.Sprintf("%db.png", i))
		dsWritePNG(t, b, p.mod)
		want := dsRunBinary(t, bin, a, b)
		r, err := NewDSSIMReference(p.ref, Options{MaxPixels: 1 << 30})
		require.NoError(t, err, p.name)
		got, err := r.Distance(p.mod)
		require.NoError(t, err, p.name)
		abs, rel := dsRelErr(got, want)
		fmt.Fprintf(&table, "%-34s ref=%.6f go=%.6f abs=%.2e rel=%.4f%%\n", p.name, want, got, abs, rel*100)
		within := rel <= 0.02 || abs <= 2e-5
		assert.True(t, within, "%s: got %.8f want %.8f (abs %.2e, rel %.3f%%)", p.name, got, want, abs, rel*100)
		if abs > 2e-5 {
			maxRel = max(maxRel, rel)
		}
		maxAbs = max(maxAbs, abs)
	}
	t.Logf("pairs=%d\n%s", len(pairs), table.String())
	t.Logf("max relative error (where abs>2e-5): %.4f%%, max absolute error: %.3e", maxRel*100, maxAbs)
}

func TestDSSIMIdentical(t *testing.T) {
	for _, img := range []image.Image{
		synthPhoto(130, 97, 1),
		dsToNRGBA(synthPhoto(64, 64, 2)),
		image.NewGray(image.Rect(0, 0, 33, 21)),
		dsAlphaDamage(synthPhoto(80, 60, 3)),
	} {
		d, err := DSSIM(img, img)
		require.NoError(t, err)
		assert.Equal(t, 0.0, d)
	}
	ref := synthPhoto(100, 80, 4)
	cp := image.NewRGBA(ref.Rect)
	copy(cp.Pix, ref.Pix)
	d, err := DSSIM(ref, cp)
	require.NoError(t, err)
	assert.Equal(t, 0.0, d)
}

func TestDSSIMMonotonic(t *testing.T) {
	ref := synthPhoto(256, 192, 7)
	r, err := NewDSSIMReference(ref)
	require.NoError(t, err)
	prev := 0.0
	for _, s := range []float64{1, 3, 6, 12, 24, 48} {
		d, err := r.Distance(dsNoise(ref, s))
		require.NoError(t, err)
		assert.Greater(t, d, prev, "noise sigma %v", s)
		prev = d
	}
	prev = 0
	for _, rad := range []int{1, 2, 3, 5, 8} {
		d, err := r.Distance(dsBlur(ref, rad))
		require.NoError(t, err)
		assert.Greater(t, d, prev, "blur radius %d", rad)
		prev = d
	}
	prev = 0
	for _, q := range []int{95, 80, 60, 40, 20, 5} {
		d, err := r.Distance(dsJPEG(t, ref, q))
		require.NoError(t, err)
		assert.Greater(t, d, prev, "jpeg quality %d", q)
		prev = d
	}
}

func TestDSSIMPixelFormatsAgree(t *testing.T) {
	base := synthPhoto(97, 61, 11)
	mod := dsNoise(base, 6)
	want, err := DSSIM(base, mod)
	require.NoError(t, err)
	require.Greater(t, want, 0.0)

	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, base, &jpeg.Options{Quality: 90}))
	decoded, err := jpeg.Decode(&buf)
	require.NoError(t, err)
	ycc := decoded.(*image.YCbCr)
	yccRGBA := toRGBAForTest(ycc)
	wantYCC, err := DSSIM(yccRGBA, mod)
	require.NoError(t, err)
	gotYCC, err := DSSIM(ycc, mod)
	require.NoError(t, err)
	assert.Equal(t, wantYCC, gotYCC)

	for name, img := range map[string]image.Image{
		"nrgba":   dsToNRGBA(base),
		"generic": dsOpaque{base},
		"rgba64": func() image.Image {
			o := image.NewRGBA64(base.Rect)
			draw.Draw(o, o.Bounds(), base, image.Point{}, draw.Src)
			return o
		}(),
	} {
		got, err := DSSIM(img, mod)
		require.NoError(t, err, name)
		assert.Equal(t, want, got, name)
	}

	for _, ratio := range []image.YCbCrSubsampleRatio{
		image.YCbCrSubsampleRatio444, image.YCbCrSubsampleRatio422, image.YCbCrSubsampleRatio420,
		image.YCbCrSubsampleRatio440, image.YCbCrSubsampleRatio411, image.YCbCrSubsampleRatio410,
	} {
		y := image.NewYCbCr(image.Rect(3, 5, 3+53, 5+37), ratio)
		rng := rand.New(rand.NewPCG(1, 2))
		for i := range y.Y {
			y.Y[i] = uint8(rng.UintN(256))
		}
		for i := range y.Cb {
			y.Cb[i], y.Cr[i] = uint8(rng.UintN(256)), uint8(rng.UintN(256))
		}
		na := image.NewNYCbCrA(y.Rect, ratio)
		copy(na.Y, y.Y)
		copy(na.Cb, y.Cb)
		copy(na.Cr, y.Cr)
		for i := range na.A {
			na.A[i] = 255
		}
		other := dsToNRGBA(synthPhoto(53, 37, 5))
		d0, err := DSSIM(dsOpaque{y}, other)
		require.NoError(t, err)
		d1, err := DSSIM(y, other)
		require.NoError(t, err)
		d2, err := DSSIM(na, other)
		require.NoError(t, err)
		assert.Equal(t, d0, d1, "ycbcr %v", ratio)
		assert.Equal(t, d0, d2, "nycbcra %v", ratio)
	}

	gray := image.NewGray(image.Rect(0, 0, 40, 30))
	pal := image.NewPaletted(gray.Rect, color.Palette{color.Gray{}, color.Gray{Y: 255}, color.NRGBA{R: 200, G: 10, B: 10, A: 128}, nil})
	for i := range gray.Pix {
		gray.Pix[i] = uint8(i * 7)
		pal.Pix[i] = uint8(i % 3)
	}
	other := dsToNRGBA(synthPhoto(40, 30, 6))
	for _, img := range []image.Image{gray, pal} {
		d0, err := DSSIM(dsOpaque{img}, other)
		require.NoError(t, err)
		d1, err := DSSIM(img, other)
		require.NoError(t, err)
		assert.Equal(t, d0, d1)
	}
}

func toRGBAForTest(img image.Image) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
	draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Src)
	return out
}

func TestDSSIMAlpha(t *testing.T) {
	base := dsAlphaDamage(synthPhoto(90, 70, 21))

	hidden := dsMap(base, func(_, _ int, c color.NRGBA) color.NRGBA {
		if c.A == 0 {
			c.R, c.G, c.B = 255, 0, 0
		}
		return c
	})
	transparent := dsMap(base, func(x, y int, c color.NRGBA) color.NRGBA {
		if (x/9+y/9)%3 == 0 {
			c.A = 0
		}
		return c
	})
	hiddenColors := dsMap(transparent, func(_, _ int, c color.NRGBA) color.NRGBA {
		if c.A == 0 {
			c.R, c.G, c.B = 1, 2, 3
		}
		return c
	})
	d, err := DSSIM(transparent, hiddenColors)
	require.NoError(t, err)
	assert.Equal(t, 0.0, d, "colors of fully transparent pixels must not matter")

	d, err = DSSIM(base, hidden)
	require.NoError(t, err)
	assert.Equal(t, 0.0, d)

	// premultiplied storage must give the same answer as straight storage
	pre := toRGBAForTest(transparent)
	d1, err := DSSIM(transparent, dsNoise(transparent, 5))
	require.NoError(t, err)
	d2, err := DSSIM(pre, dsNoise(transparent, 5))
	require.NoError(t, err)
	assert.InDelta(t, d1, d2, 0.02*d1)

	opaque := synthPhoto(90, 70, 21)
	d, err = DSSIM(opaque, transparent)
	require.NoError(t, err)
	assert.Greater(t, d, 0.0)
	d, err = DSSIM(transparent, opaque)
	require.NoError(t, err)
	assert.Greater(t, d, 0.0)
	a := dsAlphaDamage(transparent)
	d3, err := DSSIM(transparent, a)
	require.NoError(t, err)
	assert.Greater(t, d3, 0.0)
}

func TestDSSIMErrors(t *testing.T) {
	_, err := NewDSSIMReference(nil)
	require.Error(t, err)
	_, err = NewDSSIMReference(image.NewRGBA(image.Rect(0, 0, 0, 5)))
	require.Error(t, err)
	_, err = NewDSSIMReference(image.NewRGBA(image.Rect(3, 3, 3, 3)))
	require.Error(t, err)

	r, err := NewDSSIMReference(image.NewRGBA(image.Rect(0, 0, 10, 10)))
	require.NoError(t, err)
	_, err = r.Distance(nil)
	require.Error(t, err)
	_, err = r.Distance(image.NewRGBA(image.Rect(0, 0, 10, 11)))
	require.Error(t, err)
	_, err = r.Distance(image.NewRGBA(image.Rect(0, 0, 9, 10)))
	require.Error(t, err)
	_, err = r.Distance(image.NewRGBA(image.Rect(0, 0, 0, 0)))
	require.Error(t, err)
	_, err = DSSIM(image.NewRGBA(image.Rect(0, 0, 4, 4)), image.NewRGBA(image.Rect(0, 0, 4, 5)))
	require.Error(t, err)

	d, err := r.Distance(image.NewRGBA(image.Rect(5, 7, 15, 17)))
	require.NoError(t, err, "bounds with another origin but the same size are accepted")
	assert.Equal(t, 0.0, d)
	assert.Equal(t, image.Rect(0, 0, 10, 10), r.Bounds())
}

func TestDSSIMTinyAndOddSizes(t *testing.T) {
	sizes := [][2]int{
		{1, 1}, {1, 2}, {2, 1}, {2, 2}, {3, 5}, {5, 3}, {4, 4}, {7, 8}, {8, 7}, {8, 8}, {9, 9},
		{15, 17}, {16, 16}, {17, 31}, {33, 65}, {1, 500}, {500, 1}, {2, 301}, {7, 1000}, {1000, 3},
		{64, 9}, {9, 64}, {130, 8}, {3, 3},
	}
	rng := rand.New(rand.NewPCG(3, 4))
	for _, sz := range sizes {
		w, h := sz[0], sz[1]
		ref := image.NewNRGBA(image.Rect(0, 0, w, h))
		mod := image.NewNRGBA(image.Rect(0, 0, w, h))
		for i := range ref.Pix {
			ref.Pix[i] = uint8(rng.UintN(256))
			mod.Pix[i] = uint8(rng.UintN(256))
		}
		name := fmt.Sprintf("%dx%d", w, h)
		same, err := DSSIM(ref, ref)
		require.NoError(t, err, name)
		assert.Equal(t, 0.0, same, name)
		d, err := DSSIM(ref, mod)
		require.NoError(t, err, name)
		assert.False(t, math.IsNaN(d) || math.IsInf(d, 0), name)
		if w*h == 1 {
			assert.Equal(t, 0.0, d, name+": a single pixel has a constant map")
		} else if w*h > 4 {
			assert.Greater(t, d, 0.0, name)
		}

		flat := image.NewRGBA(image.Rect(0, 0, w, h))
		for i := range flat.Pix {
			flat.Pix[i] = 128
		}
		d, err = DSSIM(flat, flat)
		require.NoError(t, err, name)
		assert.Equal(t, 0.0, d, name)
		d, err = DSSIM(flat, image.NewRGBA(image.Rect(0, 0, w, h)))
		require.NoError(t, err, name)
		assert.False(t, math.IsNaN(d) || math.IsInf(d, 0), name)
	}
}

func TestDSSIMMaxPixelsReduction(t *testing.T) {
	ref := synthPhoto(400, 300, 31)
	mod := dsNoise(ref, 6)
	full, err := NewDSSIMReference(ref, Options{MaxPixels: 1 << 30})
	require.NoError(t, err)
	reduced, err := NewDSSIMReference(ref, Options{MaxPixels: 30_000})
	require.NoError(t, err)
	assert.Equal(t, 1, full.factor)
	assert.Equal(t, 2, reduced.factor)
	df, err := full.Distance(mod)
	require.NoError(t, err)
	dr, err := reduced.Distance(mod)
	require.NoError(t, err)
	assert.Greater(t, dr, 0.0)
	assert.Less(t, dr, df, "reduction hides fine noise")

	d0, err := reduced.Distance(ref)
	require.NoError(t, err)
	assert.Equal(t, 0.0, d0)

	odd := synthPhoto(101, 77, 32)
	r, err := NewDSSIMReference(odd, Options{MaxPixels: 500})
	require.NoError(t, err)
	d, err := r.Distance(dsNoise(odd, 20))
	require.NoError(t, err)
	assert.Greater(t, d, 0.0)

	nr, err := NewDSSIMReference(dsToNRGBA(dsAlphaDamage(ref)), Options{MaxPixels: 20_000})
	require.NoError(t, err)
	d, err = nr.Distance(dsAlphaDamage(ref))
	require.NoError(t, err)
	assert.Equal(t, 0.0, d)
}

func TestDSSIMConcurrent(t *testing.T) {
	ref := synthPhoto(160, 120, 41)
	r, err := NewDSSIMReference(ref)
	require.NoError(t, err)
	cands := []image.Image{dsNoise(ref, 3), dsNoise(ref, 12), dsBlur(ref, 2), dsJPEG(t, ref, 40)}
	want := make([]float64, len(cands))
	for i, c := range cands {
		want[i], err = r.Distance(c)
		require.NoError(t, err)
	}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range 6 {
				i := (g + k) % len(cands)
				got, err := r.Distance(cands[i])
				assert.NoError(t, err)
				assert.Equal(t, want[i], got)
			}
		}()
	}
	wg.Wait()
}

func TestDSSIMBlurMatchesDoubleThreeTap(t *testing.T) {
	rng := rand.New(rand.NewPCG(8, 9))
	three := func(src []float32) []float32 {
		out := make([]float32, len(src))
		for i := range src {
			l, r := src[max(i-1, 0)], src[min(i+1, len(src)-1)]
			out[i] = (l+r)*dsKSide + src[i]*dsKCenter
		}
		return out
	}
	for n := 1; n <= 40; n++ {
		src := make([]float32, n)
		for i := range src {
			src[i] = rng.Float32()
		}
		want := three(three(src))
		got := make([]float32, n)
		dsHRow(got, src)
		for i := range got {
			assert.InDelta(t, want[i], got[i], 1e-5, "n=%d i=%d", n, i)
		}
		// vertical: a w=1 plane is the same operation along y
		plane := make([]float32, n)
		tmp := make([]float32, 5)
		dsBlurPlane(plane, src, tmp, 1, n)
		for i := range plane {
			assert.InDelta(t, want[i], plane[i], 1e-5, "vertical n=%d i=%d", n, i)
		}
	}
}

func TestDSSIMLabAnchors(t *testing.T) {
	l := make([]float32, 3)
	a := make([]float32, 3)
	b := make([]float32, 3)
	r := []float32{0, 1, 0.5}
	g := []float32{0, 1, 0.5}
	bl := []float32{0, 1, 0.5}
	dsLabRow(l, a, b, r, g, bl, nil, 0)
	assert.InDelta(t, 0, l[0], 1e-6)
	assert.InDelta(t, 1.05*(1-16.0/116.0), l[1], 1e-3, "white")
	assert.InDelta(t, 86.2/220, a[1], 2e-3, "white is neutral")
	assert.InDelta(t, 107.9/220, b[1], 2e-3, "white is neutral")
	assert.InDelta(t, 1.05*math.Cbrt(0.5)-1.05*16.0/116.0, l[2], 2e-3)
}

func TestDSSIMCbrtAccuracy(t *testing.T) {
	var worst float64
	for i := 0; i <= 200000; i++ {
		x := float32(1.0/128 + float64(i)/200000*(1-1.0/128))
		got := float64(dsCbrt(x))
		want := math.Cbrt(float64(x))
		worst = max(worst, math.Abs(got-want)/want)
	}
	t.Logf("worst relative error of dsCbrt on [2^-7,1]: %.3e", worst)
	assert.Less(t, worst, 5e-7)
}

func TestDSSIMTinyImagesAgreeWithReference(t *testing.T) {
	bin := dsBinary(t)
	dir := t.TempDir()
	rng := rand.New(rand.NewPCG(77, 78))
	sizes := [][2]int{{1, 1}, {2, 2}, {3, 5}, {5, 3}, {7, 8}, {8, 8}, {9, 9}, {15, 17}, {16, 16}, {33, 65}, {1, 40}, {40, 1}, {64, 9}, {130, 8}, {31, 33}}
	for _, sz := range sizes {
		w, h := sz[0], sz[1]
		ref := image.NewNRGBA(image.Rect(0, 0, w, h))
		mod := image.NewNRGBA(image.Rect(0, 0, w, h))
		for i := 0; i < len(ref.Pix); i += 4 {
			for k := range 3 {
				v := rng.UintN(256)
				ref.Pix[i+k] = uint8(v)
				mod.Pix[i+k] = dsClamp(float64(v) + rng.NormFloat64()*30)
			}
			ref.Pix[i+3], mod.Pix[i+3] = 255, 255
		}
		a, b := filepath.Join(dir, "a.png"), filepath.Join(dir, "b.png")
		dsWritePNG(t, a, ref)
		dsWritePNG(t, b, mod)
		want := dsRunBinary(t, bin, a, b)
		got, err := DSSIM(ref, mod)
		require.NoError(t, err)
		abs, rel := dsRelErr(got, want)
		assert.True(t, rel <= 0.02 || abs <= 2e-5, "%dx%d: got %.8f want %.8f", w, h, got, want)
	}
}

func TestDSSIMFlatImagesAgreeWithReference(t *testing.T) {
	bin := dsBinary(t)
	dir := t.TempDir()
	a, b := image.NewNRGBA(image.Rect(0, 0, 20, 20)), image.NewNRGBA(image.Rect(0, 0, 20, 20))
	draw.Draw(a, a.Bounds(), image.NewUniform(color.NRGBA{R: 200, G: 30, B: 30, A: 255}), image.Point{}, draw.Src)
	draw.Draw(b, b.Bounds(), image.NewUniform(color.NRGBA{R: 20, G: 90, B: 220, A: 255}), image.Point{}, draw.Src)
	pa, pb := filepath.Join(dir, "a.png"), filepath.Join(dir, "b.png")
	dsWritePNG(t, pa, a)
	dsWritePNG(t, pb, b)
	got, err := DSSIM(a, b)
	require.NoError(t, err)
	want := dsRunBinary(t, bin, pa, pb)
	assert.InDelta(t, want, got, 0.02*want)
}
