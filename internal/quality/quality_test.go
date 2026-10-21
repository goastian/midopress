package quality

import (
	"image"
	"image/color"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func similarity(t *testing.T, ref, got image.Image) float64 {
	t.Helper()
	s, err := Similarity(ref, got)
	require.NoError(t, err)
	return s
}

func requireStrictlyDecreasing(t *testing.T, scores []float64) {
	t.Helper()
	require.Less(t, scores[len(scores)-1], scores[0])
	for i := 1; i < len(scores); i++ {
		require.Lessf(t, scores[i], scores[i-1], "scores: %v", scores)
	}
	for _, s := range scores {
		require.GreaterOrEqual(t, s, 0.0)
		require.LessOrEqual(t, s, 1.0)
	}
}

func TestIdenticalIsExactlyOne(t *testing.T) {
	for _, img := range []image.Image{
		loadPhoto(t),
		synthPhoto(768, 512, 1),
		synthPhoto(37, 53, 2),
		synthPhoto(5, 5, 3),
		image.NewRGBA(image.Rect(0, 0, 64, 64)),
	} {
		ref, err := NewReference(img)
		require.NoError(t, err)
		d, err := ref.Detail(img)
		require.NoError(t, err)
		assert.Equal(t, 1.0, d.Score)
		assert.Equal(t, 1.0, d.Luma)
		assert.Equal(t, 1.0, d.Cb)
		assert.Equal(t, 1.0, d.Cr)
	}
}

func TestMonotonicWithNoise(t *testing.T) {
	for name, src := range map[string]*image.RGBA{"photo": loadPhoto(t), "synthetic": synthPhoto(320, 240, 1)} {
		t.Run(name, func(t *testing.T) {
			ref, err := NewReference(src)
			require.NoError(t, err)
			var scores []float64
			for _, sigma := range []float64{0, 2, 4, 8, 16, 32, 64} {
				s, err := ref.Similarity(noisy(src, sigma))
				require.NoError(t, err)
				scores = append(scores, s)
			}
			requireStrictlyDecreasing(t, scores)
		})
	}
}

func TestMonotonicWithBlur(t *testing.T) {
	for name, src := range map[string]*image.RGBA{"photo": loadPhoto(t), "synthetic": synthPhoto(320, 240, 1)} {
		t.Run(name, func(t *testing.T) {
			ref, err := NewReference(src)
			require.NoError(t, err)
			var scores []float64
			for r := 0; r <= 6; r++ {
				var cand image.Image = src
				if r > 0 {
					cand = boxBlur(src, r)
				}
				s, err := ref.Similarity(cand)
				require.NoError(t, err)
				scores = append(scores, s)
			}
			requireStrictlyDecreasing(t, scores)
		})
	}
}

func TestMonotonicWithJPEGQuality(t *testing.T) {
	for name, src := range map[string]*image.RGBA{"photo": loadPhoto(t), "synthetic": synthPhoto(320, 240, 1)} {
		t.Run(name, func(t *testing.T) {
			ref, err := NewReference(src)
			require.NoError(t, err)
			var scores []float64
			for _, q := range []int{95, 85, 75, 65, 55, 45, 35, 25, 15, 10} {
				s, err := ref.Similarity(jpegRoundTrip(t, src, q))
				require.NoError(t, err)
				scores = append(scores, s)
			}
			requireStrictlyDecreasing(t, scores)
			assert.Greater(t, scores[0], 0.97, "q95 should be near-lossless: %v", scores)
		})
	}
}

func TestSymmetry(t *testing.T) {
	a := loadPhoto(t)
	b := noisy(boxBlur(a, 2), 10)
	ra, err := NewReference(a)
	require.NoError(t, err)
	rb, err := NewReference(b)
	require.NoError(t, err)
	d1, err := ra.Detail(b)
	require.NoError(t, err)
	d2, err := rb.Detail(a)
	require.NoError(t, err)
	assert.InDelta(t, d1.Luma, d2.Luma, 1e-12)
	assert.InDelta(t, d1.Cb, d2.Cb, 1e-12)
	assert.InDelta(t, d1.Cr, d2.Cr, 1e-12)
	assert.InDelta(t, d1.Score, d2.Score, 1e-12)
}

func newYCbCr444(src *image.RGBA) *image.YCbCr {
	b := src.Bounds()
	out := image.NewYCbCr(b, image.YCbCrSubsampleRatio444)
	for y := range b.Dy() {
		for x := range b.Dx() {
			c := src.RGBAAt(x, y)
			yy, cb, cr := color.RGBToYCbCr(c.R, c.G, c.B)
			out.Y[y*out.YStride+x] = yy
			out.Cb[y*out.CStride+x] = cb
			out.Cr[y*out.CStride+x] = cr
		}
	}
	return out
}

func damagePlanes(src *image.YCbCr, luma, chroma bool, amp int) *image.YCbCr {
	out := *src
	out.Y = append([]uint8(nil), src.Y...)
	out.Cb = append([]uint8(nil), src.Cb...)
	out.Cr = append([]uint8(nil), src.Cr...)
	rng := rand.New(rand.NewPCG(5, 5))
	shift := func(v uint8) uint8 {
		return uint8(min(max(int(v)+rng.IntN(2*amp+1)-amp, 0), 255))
	}
	if luma {
		for i := range out.Y {
			out.Y[i] = shift(out.Y[i])
		}
	}
	if chroma {
		for i := range out.Cb {
			out.Cb[i] = shift(out.Cb[i])
			out.Cr[i] = shift(out.Cr[i])
		}
	}
	return &out
}

func TestChromaDamageCostsLessThanLumaDamage(t *testing.T) {
	src := newYCbCr444(loadPhoto(t))
	ref, err := NewReference(src)
	require.NoError(t, err)
	for _, amp := range []int{6, 12, 24} {
		lumaOnly, err := ref.Detail(damagePlanes(src, true, false, amp))
		require.NoError(t, err)
		chromaOnly, err := ref.Detail(damagePlanes(src, false, true, amp))
		require.NoError(t, err)
		assert.Less(t, chromaOnly.Score, 1.0)
		assert.Greater(t, chromaOnly.Score, lumaOnly.Score)
		assert.Less(t, chromaOnly.Cb, 1.0)
		assert.Less(t, chromaOnly.Cr, 1.0)
		assert.Greater(t, lumaOnly.Luma, 0.0)
		assert.Less(t, lumaOnly.Luma, 1.0)
	}
}

func TestSizeMismatchAndEmpty(t *testing.T) {
	ref, err := NewReference(synthPhoto(64, 48, 1))
	require.NoError(t, err)
	assert.Equal(t, image.Rect(0, 0, 64, 48), ref.Bounds())
	_, err = ref.Similarity(synthPhoto(48, 64, 1))
	assert.Error(t, err)
	_, err = ref.Similarity(synthPhoto(64, 49, 1))
	assert.Error(t, err)
	_, err = ref.Similarity(nil)
	assert.Error(t, err)
	_, err = Similarity(synthPhoto(8, 8, 1), synthPhoto(9, 8, 1))
	assert.Error(t, err)

	_, err = NewReference(image.NewRGBA(image.Rect(0, 0, 0, 0)))
	assert.Error(t, err)
	_, err = NewReference(image.NewRGBA(image.Rect(5, 5, 5, 10)))
	assert.Error(t, err)
	_, err = NewReference(nil)
	assert.Error(t, err)
	_, err = Similarity(image.NewGray(image.Rect(0, 0, 0, 0)), image.NewGray(image.Rect(0, 0, 0, 0)))
	assert.Error(t, err)
}

func TestOriginDoesNotMatter(t *testing.T) {
	base := synthPhoto(100, 80, 1)
	cand := noisy(base, 10)
	want := similarity(t, base, cand)
	sub := base.SubImage(image.Rect(0, 0, 100, 80))
	moved := image.NewRGBA(image.Rect(30, 20, 130, 100))
	copy(moved.Pix, cand.Pix)
	assert.InDelta(t, want, similarity(t, sub, moved), 1e-12)
}

func buildImage(tb testing.TB, kind string, r image.Rectangle, seed uint64) image.Image {
	tb.Helper()
	rng := rand.New(rand.NewPCG(seed, 3))
	fill := func(b []uint8) {
		for i := range b {
			b[i] = uint8(rng.UintN(256))
		}
	}
	switch kind {
	case "rgba":
		m := image.NewRGBA(r)
		fill(m.Pix)
		return m
	case "nrgba":
		m := image.NewNRGBA(r)
		fill(m.Pix)
		for i := 3; i < len(m.Pix); i += 4 {
			switch rng.UintN(4) {
			case 0:
				m.Pix[i] = 255
			case 1:
				m.Pix[i] = 0
			}
		}
		return m
	case "gray":
		m := image.NewGray(r)
		fill(m.Pix)
		return m
	case "paletted":
		pal := make(color.Palette, 40)
		for i := range pal {
			pal[i] = color.NRGBA{uint8(rng.UintN(256)), uint8(rng.UintN(256)), uint8(rng.UintN(256)), uint8(255 - 85*rng.UintN(4)%256)}
		}
		m := image.NewPaletted(r, pal)
		for i := range m.Pix {
			m.Pix[i] = uint8(rng.UintN(uint(len(pal))))
		}
		return m
	case "nycbcra":
		m := image.NewNYCbCrA(r, image.YCbCrSubsampleRatio420)
		fill(m.Y)
		fill(m.Cb)
		fill(m.Cr)
		fill(m.A)
		for i := range m.A {
			if rng.UintN(2) == 0 {
				m.A[i] = 255
			}
		}
		return m
	}
	ratios := map[string]image.YCbCrSubsampleRatio{
		"ycc444": image.YCbCrSubsampleRatio444,
		"ycc422": image.YCbCrSubsampleRatio422,
		"ycc420": image.YCbCrSubsampleRatio420,
		"ycc440": image.YCbCrSubsampleRatio440,
		"ycc411": image.YCbCrSubsampleRatio411,
		"ycc410": image.YCbCrSubsampleRatio410,
	}
	ratio, ok := ratios[kind]
	require.True(tb, ok, kind)
	m := image.NewYCbCr(r, ratio)
	fill(m.Y)
	fill(m.Cb)
	fill(m.Cr)
	return m
}

// mutate perturbs about half of the samples of img in place.
func mutate(img image.Image, seed uint64) {
	rng := rand.New(rand.NewPCG(seed, 11))
	jitter := func(b []uint8) {
		for i := range b {
			if rng.UintN(2) == 0 {
				b[i] = uint8(min(max(int(b[i])+int(rng.UintN(41))-20, 0), 255))
			}
		}
	}
	switch m := img.(type) {
	case *image.RGBA:
		jitter(m.Pix)
	case *image.NRGBA:
		jitter(m.Pix)
	case *image.Gray:
		jitter(m.Pix)
	case *image.YCbCr:
		jitter(m.Y)
		jitter(m.Cb)
		jitter(m.Cr)
	case *image.NYCbCrA:
		jitter(m.Y)
		jitter(m.Cb)
		jitter(m.Cr)
		jitter(m.A)
	case *image.Paletted:
		for i := range m.Pix {
			if rng.UintN(3) == 0 {
				m.Pix[i] = uint8(rng.UintN(uint(len(m.Palette))))
			}
		}
	default:
		panic("unsupported")
	}
}

func TestFastPathsMatchGenericPath(t *testing.T) {
	kinds := []string{"rgba", "nrgba", "gray", "paletted", "nycbcra", "ycc444", "ycc422", "ycc420", "ycc440", "ycc411", "ycc410"}
	rects := []image.Rectangle{
		image.Rect(0, 0, 45, 37),
		image.Rect(0, 0, 64, 64),
		image.Rect(3, 5, 52, 44),
		image.Rect(7, 9, 20, 18),
		image.Rect(1, 1, 6, 11),
	}
	for _, kind := range kinds {
		for _, r := range rects {
			a := buildImage(t, kind, r, 1)
			b := buildImage(t, kind, r, 1)
			mutate(b, 2)

			generic, err := NewReference(opaqueImage{a})
			require.NoError(t, err)
			want, err := generic.Detail(opaqueImage{b})
			require.NoError(t, err)

			fast, err := NewReference(a)
			require.NoError(t, err)
			variants := map[string]image.Image{"fast": b, "generic": opaqueImage{b}}
			for name, cand := range variants {
				got, err := fast.Detail(cand)
				require.NoError(t, err, kind)
				msg := kind + " " + r.String() + " " + name
				assert.InDelta(t, want.Luma, got.Luma, 1e-9, msg)
				assert.InDelta(t, want.Cb, got.Cb, 1e-9, msg)
				assert.InDelta(t, want.Cr, got.Cr, 1e-9, msg)
				assert.InDelta(t, want.Score, got.Score, 1e-9, msg)
			}
			got, err := generic.Detail(b)
			require.NoError(t, err)
			assert.InDelta(t, want.Score, got.Score, 1e-9, kind)
			assert.Less(t, want.Score, 1.0, kind)
		}
	}
}

func TestFastPathsMatchWithReduction(t *testing.T) {
	for _, kind := range []string{"rgba", "nrgba", "ycc420", "ycc411", "nycbcra", "gray", "paletted"} {
		r := image.Rect(2, 3, 103, 78)
		a := buildImage(t, kind, r, 1)
		b := buildImage(t, kind, r, 1)
		mutate(b, 2)
		opt := Options{MaxPixels: 700}
		generic, err := NewReference(opaqueImage{a}, opt)
		require.NoError(t, err)
		want, err := generic.Detail(opaqueImage{b})
		require.NoError(t, err)
		fast, err := NewReference(a, opt)
		require.NoError(t, err)
		require.Greater(t, fast.factor, 1)
		got, err := fast.Detail(b)
		require.NoError(t, err)
		assert.InDelta(t, want.Score, got.Score, 1e-9, kind)
	}
}

func TestLumaMatchesStandardLibrary(t *testing.T) {
	for _, w := range []int{64, 63} {
		src := randomRGBA(w, 4, 9)
		dst := make([]uint8, w)
		pairs := make([]uint16, 3*((w+1)/2))
		lumaAndPairs(dst, pairs, src.Pix[:4*w])
		for i, v := range dst {
			y, _, _ := color.RGBToYCbCr(src.Pix[4*i], src.Pix[4*i+1], src.Pix[4*i+2])
			require.Equal(t, y, v)
		}
		for cx := range w / 2 {
			for k := range 3 {
				require.Equal(t, uint16(src.Pix[8*cx+k])+uint16(src.Pix[8*cx+4+k]), pairs[3*cx+k])
			}
		}
	}
}

func TestReductionFactor(t *testing.T) {
	assert.Equal(t, 1, reductionFactor(1000, 1000, 1_000_000))
	assert.Equal(t, 2, reductionFactor(1000, 1001, 1_000_000))
	for _, c := range [][3]int{{4000, 3000, 2_000_000}, {4001, 3001, 2_000_000}, {101, 77, 100}, {101, 77, 1}, {10, 10, 3}, {5000, 7, 1000}} {
		f := reductionFactor(c[0], c[1], c[2])
		assert.LessOrEqual(t, ceilDiv(c[0], f)*ceilDiv(c[1], f), max(c[2], 1), "%v f=%d", c, f)
		if f > 1 {
			assert.Greater(t, ceilDiv(c[0], f-1)*ceilDiv(c[1], f-1), c[2], "%v f=%d not minimal", c, f)
		}
	}
}

// reductionTolerancePerStep documents how far the score computed on a reduced
// image may drift from the full-size score on a photograph, per unit of
// reduction factor. Reduction low-passes both images and hides blocking and
// ringing, so the reduced score is optimistic: on the test photo the gap is
// about 0.004 at q90 and 0.04 at q15 for factor 2, and 0.07 at q15 for factor 4.
const reductionTolerancePerStep = 0.025

func TestMaxPixelsKeepsScoresClose(t *testing.T) {
	src := loadPhoto(t)
	full, err := NewReference(src)
	require.NoError(t, err)
	require.Equal(t, 1, full.factor)
	for _, maxPixels := range []int{30000, 12288, 5000} {
		reduced, err := NewReference(src, Options{MaxPixels: maxPixels})
		require.NoError(t, err)
		require.Greater(t, reduced.factor, 1)
		for _, q := range []int{90, 70, 50, 30, 15} {
			cand := jpegRoundTrip(t, src, q)
			a, err := full.Similarity(cand)
			require.NoError(t, err)
			b, err := reduced.Similarity(cand)
			require.NoError(t, err)
			assert.GreaterOrEqual(t, b, a-0.002, "reduction must not be pessimistic")
			assert.InDelta(t, a, b, reductionTolerancePerStep*float64(reduced.factor), "maxPixels=%d q=%d full=%f reduced=%f", maxPixels, q, a, b)
			t.Logf("maxPixels=%d factor=%d q=%d full=%.4f reduced=%.4f", maxPixels, reduced.factor, q, a, b)
		}
		s, err := reduced.Similarity(src)
		require.NoError(t, err)
		assert.Equal(t, 1.0, s)
	}
}

func TestExtremeReductionDoesNotPanic(t *testing.T) {
	for _, mp := range []int{1, 2, 7, 64, 100} {
		for _, sz := range [][2]int{{101, 77}, {3, 400}, {1, 1}, {9, 9}} {
			a := synthPhoto(sz[0], sz[1], 1)
			ref, err := NewReference(a, Options{MaxPixels: mp})
			require.NoError(t, err)
			s, err := ref.Similarity(noisy(a, 20))
			require.NoError(t, err)
			assert.GreaterOrEqual(t, s, 0.0)
			assert.LessOrEqual(t, s, 1.0)
		}
	}
}

func TestConcurrentUse(t *testing.T) {
	src := synthPhoto(200, 150, 1)
	ref, err := NewReference(src)
	require.NoError(t, err)
	cands := make([]image.Image, 6)
	want := make([]float64, len(cands))
	for i := range cands {
		if i%2 == 0 {
			cands[i] = noisy(src, float64(3+5*i))
		} else {
			cands[i] = jpegRoundTrip(t, src, 90-12*i)
		}
		want[i], err = ref.Similarity(cands[i])
		require.NoError(t, err)
	}
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range 40 {
				i := (g + n) % len(cands)
				got, err := ref.Similarity(cands[i])
				if !assert.NoError(t, err) || !assert.Equal(t, want[i], got) {
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestAlpha(t *testing.T) {
	const w, h = 40, 40
	opaque := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			opaque.SetNRGBA(x, y, color.NRGBA{uint8(x * 6), uint8(y * 6), uint8((x + y) * 3), 255})
		}
	}

	t.Run("transparent pixels ignore color", func(t *testing.T) {
		a := image.NewNRGBA(opaque.Rect)
		b := image.NewNRGBA(opaque.Rect)
		copy(a.Pix, opaque.Pix)
		copy(b.Pix, opaque.Pix)
		for y := 10; y < 30; y++ {
			for x := 10; x < 30; x++ {
				a.SetNRGBA(x, y, color.NRGBA{255, 0, 0, 0})
				b.SetNRGBA(x, y, color.NRGBA{0, 255, 40, 0})
			}
		}
		assert.Equal(t, 1.0, similarity(t, a, b))
	})

	t.Run("losing the content of a transparent area is penalized only when visible", func(t *testing.T) {
		clear := image.NewNRGBA(opaque.Rect)
		copy(clear.Pix, opaque.Pix)
		for y := 10; y < 30; y++ {
			for x := 10; x < 30; x++ {
				clear.SetNRGBA(x, y, color.NRGBA{200, 10, 10, 0})
			}
		}
		assert.Less(t, similarity(t, opaque, clear), 0.95)
	})

	t.Run("more opacity loss scores lower", func(t *testing.T) {
		var scores []float64
		for _, a := range []uint8{255, 230, 180, 120, 60} {
			c := image.NewNRGBA(opaque.Rect)
			copy(c.Pix, opaque.Pix)
			for i := 3; i < len(c.Pix); i += 4 {
				c.Pix[i] = a
			}
			scores = append(scores, similarity(t, opaque, c))
		}
		requireStrictlyDecreasing(t, scores)
	})

	t.Run("premultiplied and straight representations agree", func(t *testing.T) {
		straight := image.NewNRGBA(opaque.Rect)
		for i := 0; i < len(straight.Pix); i += 4 {
			copy(straight.Pix[i:i+4], opaque.Pix[i:i+4])
			straight.Pix[i+3] = uint8(60 + (i/4)%190)
		}
		pre := toRGBA(straight)
		cand := noisy(toRGBA(opaque), 10)
		candStraight := image.NewNRGBA(cand.Rect)
		copy(candStraight.Pix, cand.Pix)
		a := similarity(t, straight, candStraight)
		b := similarity(t, opaqueImage{straight}, opaqueImage{candStraight})
		assert.InDelta(t, a, b, 1e-9)
		assert.Less(t, similarity(t, pre, cand), 1.0)
	})

	t.Run("opaque image over fully transparent grey equals grey", func(t *testing.T) {
		grey := image.NewRGBA(opaque.Rect)
		for i := 0; i < len(grey.Pix); i += 4 {
			grey.Pix[i], grey.Pix[i+1], grey.Pix[i+2], grey.Pix[i+3] = 128, 128, 128, 255
		}
		transparent := image.NewNRGBA(opaque.Rect)
		assert.Equal(t, 1.0, similarity(t, grey, transparent))
	})
}

func TestChromaMatchesStandardLibrary(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	check := func(r, g, b uint8) {
		_, wantCb, wantCr := color.RGBToYCbCr(r, g, b)
		cb, cr := chroma(int32(r), int32(g), int32(b))
		require.Equal(t, wantCb, cb)
		require.Equal(t, wantCr, cr)
	}
	for range 200000 {
		check(uint8(rng.UintN(256)), uint8(rng.UintN(256)), uint8(rng.UintN(256)))
	}
	for _, c := range [][3]uint8{{0, 0, 0}, {255, 255, 255}, {255, 0, 0}, {0, 255, 0}, {0, 0, 255}} {
		check(c[0], c[1], c[2])
	}
}
