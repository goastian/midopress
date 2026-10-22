package quality

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func s2Binary(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("ssimulacra2_rs"); err == nil {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".cargo", "bin", "ssimulacra2_rs")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("ssimulacra2_rs is not installed")
	return ""
}

func s2WritePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	require.NoError(t, enc.Encode(f, img))
	require.NoError(t, f.Close())
}

func s2Run(t *testing.T, bin, a, b string) float64 {
	t.Helper()
	out, err := exec.Command(bin, "image", a, b).CombinedOutput()
	require.NoError(t, err, string(out))
	_, v, ok := strings.Cut(strings.TrimSpace(string(out)), "Score: ")
	require.True(t, ok, string(out))
	f, err := strconv.ParseFloat(v, 64)
	require.NoError(t, err)
	return f
}

func s2Load(t *testing.T, path string) *image.RGBA {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Skipf("missing test image: %v", err)
	}
	defer func() { _ = f.Close() }()
	img, _, err := image.Decode(f)
	require.NoError(t, err)
	out := image.NewRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
	draw.Draw(out, out.Bounds(), image.NewUniform(color.Gray{greyBackground}), image.Point{}, draw.Src)
	draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Over)
	return out
}

func s2ChannelDamage(src *image.RGBA) *image.RGBA {
	out := image.NewRGBA(src.Rect)
	for i := 0; i < len(src.Pix); i += 4 {
		out.Pix[i] = src.Pix[i] &^ 0x1f
		out.Pix[i+1] = src.Pix[i+1]
		out.Pix[i+2] = uint8(int(src.Pix[i+2])*3/4 + 20)
		out.Pix[i+3] = 255
	}
	return out
}

func s2Dim(src *image.RGBA) *image.RGBA {
	out := image.NewRGBA(src.Rect)
	for i := 0; i < len(src.Pix); i += 4 {
		for k := range 3 {
			out.Pix[i+k] = uint8(int(src.Pix[i+k]) * 7 / 8)
		}
		out.Pix[i+3] = 255
	}
	return out
}

func s2Sub(src *image.RGBA, r image.Rectangle) *image.RGBA {
	return toRGBA(src.SubImage(r.Add(src.Rect.Min)))
}

type s2Pair struct {
	name     string
	ref, mod *image.RGBA
}

func s2Pairs(t *testing.T) []s2Pair {
	t.Helper()
	var pairs []s2Pair
	add := func(name string, ref *image.RGBA, mod image.Image) {
		pairs = append(pairs, s2Pair{name, ref, toRGBA(mod)})
	}
	for name, qualities := range map[string][]int{
		"kodim01": {95, 85, 70, 50, 30},
		"kodim12": {95, 85, 70, 50, 30},
		"kodim05": {95, 50},
		"kodim19": {95, 50},
	} {
		ref := s2Load(t, "/tmp/opencode/img/"+name+".png")
		for _, q := range qualities {
			add(name+"-jpeg"+strconv.Itoa(q), ref, jpegRoundTrip(t, ref, q))
		}
	}
	k3 := s2Load(t, "/tmp/opencode/img/kodim03.png")
	add("kodim03-blur2", k3, boxBlur(k3, 2))
	add("kodim03-blur5", k3, boxBlur(k3, 5))
	add("kodim03-noise4", k3, noisy(k3, 4))
	add("kodim03-noise20", k3, noisy(k3, 20))
	add("kodim03-channels", k3, s2ChannelDamage(k3))
	add("kodim03-dim", k3, s2Dim(k3))
	for _, name := range []string{"photo-lion.jpg", "gallery-1.jpg", "photo-earth.jpg", "kodak-07.png", "graphic-logo.png"} {
		ref := s2Load(t, "/tmp/opencode/corpus/"+name)
		add(name+"-jpeg60", ref, jpegRoundTrip(t, ref, 60))
		add(name+"-noise8", ref, noisy(ref, 8))
	}
	photo := loadPhoto(t)
	add("testdata-photo-jpeg40", photo, jpegRoundTrip(t, photo, 40))
	for _, sz := range [][2]int{{8, 8}, {9, 13}, {33, 17}, {64, 8}, {101, 77}} {
		ref := s2Sub(k3, image.Rect(100, 100, 100+sz[0], 100+sz[1]))
		add("crop"+strconv.Itoa(sz[0])+"x"+strconv.Itoa(sz[1]), ref, noisy(ref, 10))
	}
	return pairs
}

func TestSSIMULACRA2AgreesWithReference(t *testing.T) {
	bin := s2Binary(t)
	pairs := s2Pairs(t)
	require.GreaterOrEqual(t, len(pairs), 12)
	diffs := make([]float64, len(pairs))
	t.Run("pairs", func(t *testing.T) {
		for i, p := range pairs {
			t.Run(p.name, func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				a, b := filepath.Join(dir, "a.png"), filepath.Join(dir, "b.png")
				s2WritePNG(t, a, p.ref)
				s2WritePNG(t, b, p.mod)
				want := s2Run(t, bin, a, b)
				got, err := SSIMULACRA2(p.ref, p.mod)
				require.NoError(t, err)
				diffs[i] = math.Abs(got - want)
				t.Logf("ref=%10.5f got=%10.5f diff=%.2e", want, got, diffs[i])
				assert.LessOrEqual(t, diffs[i], 0.05)
			})
		}
	})
	t.Logf("%d pairs, max abs error %.3e", len(pairs), slices.Max(diffs))
}

func TestSSIMULACRA2IdenticalIsExactly100(t *testing.T) {
	for _, kind := range []string{"rgba", "nrgba", "gray", "paletted", "nycbcra", "ycc444", "ycc420"} {
		for _, r := range []image.Rectangle{image.Rect(0, 0, 64, 48), image.Rect(3, 5, 30, 41), image.Rect(0, 0, 8, 8)} {
			img := buildImage(t, kind, r, 5)
			got, err := SSIMULACRA2(img, img)
			require.NoError(t, err)
			assert.Equal(t, 100.0, got, kind+" "+r.String())
		}
	}
	photo := loadPhoto(t)
	got, err := SSIMULACRA2(photo, toRGBA(photo))
	require.NoError(t, err)
	assert.Equal(t, 100.0, got)
}

func TestSSIMULACRA2Monotonic(t *testing.T) {
	photo := loadPhoto(t)
	ref, err := NewSSIMULACRA2Reference(photo)
	require.NoError(t, err)
	score := func(img image.Image) float64 {
		v, err := ref.Score(img)
		require.NoError(t, err)
		return v
	}
	var jpegs, noises, blurs []float64
	for _, q := range []int{95, 85, 70, 50, 30, 10} {
		jpegs = append(jpegs, score(jpegRoundTrip(t, photo, q)))
	}
	for _, s := range []float64{2, 5, 10, 20, 40} {
		noises = append(noises, score(noisy(photo, s)))
	}
	for _, r := range []int{1, 2, 4, 8} {
		blurs = append(blurs, score(boxBlur(photo, r)))
	}
	for name, scores := range map[string][]float64{"jpeg": jpegs, "noise": noises, "blur": blurs} {
		for i := 1; i < len(scores); i++ {
			assert.Less(t, scores[i], scores[i-1], "%s %v", name, scores)
		}
		assert.Less(t, scores[0], 100.0, name)
	}
	assert.Less(t, noises[len(noises)-1], 50.0)
}

func TestSSIMULACRA2SizeAndErrors(t *testing.T) {
	_, err := NewSSIMULACRA2Reference(nil)
	require.Error(t, err)
	_, err = NewSSIMULACRA2Reference(image.NewRGBA(image.Rect(0, 0, 0, 0)))
	require.Error(t, err)
	for _, sz := range [][2]int{{7, 50}, {50, 7}, {1, 1}, {7, 7}} {
		_, err = NewSSIMULACRA2Reference(image.NewRGBA(image.Rect(0, 0, sz[0], sz[1])))
		require.ErrorIs(t, err, ErrSSIMULACRA2TooSmall)
	}

	ref, err := NewSSIMULACRA2Reference(randomRGBA(40, 30, 1))
	require.NoError(t, err)
	_, err = ref.Score(randomRGBA(40, 31, 1))
	require.Error(t, err)
	_, err = ref.Score(randomRGBA(30, 40, 1))
	require.Error(t, err)
	_, err = ref.Score(nil)
	require.Error(t, err)
	_, err = ref.Score(image.NewRGBA(image.Rectangle{}))
	require.Error(t, err)
	_, err = SSIMULACRA2(randomRGBA(40, 30, 1), randomRGBA(20, 30, 1))
	require.Error(t, err)

	a, b := randomRGBA(8, 8, 1), randomRGBA(8, 8, 2)
	got, err := SSIMULACRA2(a, b)
	require.NoError(t, err)
	assert.False(t, math.IsNaN(got) || math.IsInf(got, 0))
	assert.Less(t, got, 100.0)
}

func TestSSIMULACRA2OriginDoesNotMatter(t *testing.T) {
	base := synthPhoto(80, 60, 3)
	cand := noisy(base, 6)
	want, err := SSIMULACRA2(base, cand)
	require.NoError(t, err)
	moved := image.NewRGBA(image.Rect(30, 20, 110, 80))
	copy(moved.Pix, cand.Pix)
	got, err := SSIMULACRA2(base, moved)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestSSIMULACRA2FastPathsMatchGenericPath(t *testing.T) {
	kinds := []string{"rgba", "nrgba", "gray", "paletted", "nycbcra", "ycc444", "ycc422", "ycc420", "ycc440", "ycc411", "ycc410"}
	for _, kind := range kinds {
		for _, r := range []image.Rectangle{image.Rect(0, 0, 45, 37), image.Rect(3, 5, 52, 44), image.Rect(7, 9, 20, 18)} {
			a := buildImage(t, kind, r, 1)
			b := buildImage(t, kind, r, 1)
			mutate(b, 2)
			generic, err := NewSSIMULACRA2Reference(opaqueImage{a})
			require.NoError(t, err)
			want, err := generic.Score(opaqueImage{b})
			require.NoError(t, err)
			fast, err := NewSSIMULACRA2Reference(a)
			require.NoError(t, err)
			got, err := fast.Score(b)
			require.NoError(t, err)
			assert.Equal(t, want, got, kind+" "+r.String())
			assert.Less(t, want, 100.0, kind)
		}
	}
}

func TestSSIMULACRA2Alpha(t *testing.T) {
	const w, h = 40, 40
	opaqueImg := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			opaqueImg.SetNRGBA(x, y, color.NRGBA{uint8(x * 6), uint8(y * 6), uint8((x + y) * 3), 255})
		}
	}
	t.Run("transparent pixels ignore colour", func(t *testing.T) {
		a := image.NewNRGBA(opaqueImg.Rect)
		b := image.NewNRGBA(opaqueImg.Rect)
		copy(a.Pix, opaqueImg.Pix)
		copy(b.Pix, opaqueImg.Pix)
		for y := 10; y < 30; y++ {
			for x := 10; x < 30; x++ {
				a.SetNRGBA(x, y, color.NRGBA{255, 0, 0, 0})
				b.SetNRGBA(x, y, color.NRGBA{0, 0, 255, 0})
			}
		}
		got, err := SSIMULACRA2(a, b)
		require.NoError(t, err)
		assert.Equal(t, 100.0, got)
	})
	t.Run("composited over mid grey", func(t *testing.T) {
		translucent := image.NewNRGBA(opaqueImg.Rect)
		flat := image.NewRGBA(opaqueImg.Rect)
		for i := 0; i < len(translucent.Pix); i += 4 {
			translucent.Pix[i], translucent.Pix[i+1], translucent.Pix[i+2], translucent.Pix[i+3] = 200, 60, 20, 128
		}
		for i := 0; i < len(flat.Pix); i += 4 {
			c := colorToRGB(translucent.At((i/4)%w, (i/4)/w))
			flat.Pix[i], flat.Pix[i+1], flat.Pix[i+2], flat.Pix[i+3] = c[0], c[1], c[2], 255
		}
		got, err := SSIMULACRA2(opaqueImg, translucent)
		require.NoError(t, err)
		want, err := SSIMULACRA2(opaqueImg, flat)
		require.NoError(t, err)
		assert.Equal(t, want, got)
		assert.Less(t, got, 100.0)
	})
}

func TestSSIMULACRA2MaxPixels(t *testing.T) {
	photo := synthPhoto(400, 300, 4)
	cand := jpegRoundTrip(t, photo, 40)
	full, err := NewSSIMULACRA2Reference(photo)
	require.NoError(t, err)
	reduced, err := NewSSIMULACRA2Reference(photo, Options{MaxPixels: 30_000})
	require.NoError(t, err)
	assert.Equal(t, 100, int(math.Round(mustScore(t, reduced, photo))))
	f, err := full.Score(cand)
	require.NoError(t, err)
	r, err := reduced.Score(cand)
	require.NoError(t, err)
	assert.Greater(t, r, f, "reduction is documented as optimistic")
	assert.Less(t, r, 100.0)
	_, err = NewSSIMULACRA2Reference(image.NewRGBA(image.Rect(0, 0, 4000, 10)), Options{MaxPixels: 1000})
	require.ErrorIs(t, err, ErrSSIMULACRA2TooSmall)
}

func mustScore(t *testing.T, r *SSIMULACRA2Reference, img image.Image) float64 {
	t.Helper()
	v, err := r.Score(img)
	require.NoError(t, err)
	return v
}

func TestSSIMULACRA2Concurrent(t *testing.T) {
	photo := synthPhoto(160, 120, 6)
	ref, err := NewSSIMULACRA2Reference(photo)
	require.NoError(t, err)
	cands := []image.Image{jpegRoundTrip(t, photo, 30), noisy(photo, 9), boxBlur(photo, 2), photo}
	want := make([]float64, len(cands))
	for i, c := range cands {
		want[i] = mustScore(t, ref, c)
	}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				i := g % len(cands)
				v, err := ref.Score(cands[i])
				assert.NoError(t, err)
				assert.Equal(t, want[i], v)
			}
		}()
	}
	wg.Wait()
}

func TestSSIMULACRA2Cbrt(t *testing.T) {
	worst := 0.0
	for i := 1; i < 200000; i++ {
		x := float32(i) * 1e-5
		want := math.Cbrt(float64(x))
		worst = max(worst, math.Abs(float64(s2Cbrt(x))-want)/want)
	}
	assert.Less(t, worst, 2e-7)
}

func TestSSIMULACRA2BlurPreservesConstants(t *testing.T) {
	const w, h = 40, 33
	src := make([]float32, w*h)
	for i := range src {
		src[i] = 0.5
	}
	tmp := make([]float32, w*h)
	dst := make([]float32, w*h)
	s2HBlur(tmp, src, nil, w, h, make([]float32, s2HLanes*(w+3*s2Radius)))
	var zeros [s2Cols]float32
	s2VBlur(dst, tmp, w, h, &zeros)
	assert.InDelta(t, 0.5, dst[16*w+20], 1e-4)
	assert.Less(t, dst[0], float32(0.5), "zero boundary loses energy at the corner")
}
