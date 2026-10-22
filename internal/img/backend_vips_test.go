//go:build vips

package img

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	stdjpeg "image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"text/tabwriter"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/quality"
)

// TestMain swaps the backend of the engine suite: with
// MIDOPRESS_TEST_BACKEND=vips every test that uses testEngine runs on vips. It
// can not be done in an init function, libvips deadlocks when it first runs
// before main has started.
func TestMain(m *testing.M) {
	if os.Getenv("MIDOPRESS_TEST_BACKEND") == "vips" {
		v, err := NewVips()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		testEngine = NewEngine(v)
		testEngine.Metric = MetricSSIM
	}
	os.Exit(m.Run())
}

func newTestVips(t testing.TB) *Vips {
	t.Helper()
	v, err := NewVips()
	require.NoError(t, err)
	return v
}

func skipUnless(t *testing.T, v *Vips, f Format) {
	t.Helper()
	if !v.Supports(f) {
		t.Skipf("the installed libvips can not encode and decode %s", f)
	}
}

func TestVipsRegistered(t *testing.T) {
	assert.Contains(t, Backends(), "vips")
	b, err := NewBackend("vips")
	require.NoError(t, err)
	assert.Equal(t, "vips", b.Name())
	v := b.(*Vips)
	t.Logf("supported: jpeg=%v webp=%v avif=%v jxl=%v", v.Supports(JPEG), v.Supports(WebP), v.Supports(AVIF), v.Supports(JXL))
	assert.False(t, v.Supports(PNG))
	assert.False(t, v.Supports(GIF))
	assert.True(t, v.Native(AVIF))
}

func TestVipsRoundTrip(t *testing.T) {
	v := newTestVips(t)
	src := photoLike(160, 120, 7)
	for _, f := range []Format{JPEG, WebP, AVIF, JXL} {
		t.Run(f.String(), func(t *testing.T) {
			skipUnless(t, v, f)
			data, err := v.Encode(f, src, EncodeOptions{Quality: 85, Effort: 2})
			require.NoError(t, err)
			assert.Equal(t, f, Sniff(data))
			dec, err := v.Decode(f, data)
			require.NoError(t, err)
			s := mustSimilar(t, src, dec)
			t.Logf("%s: %d bytes, similarity %.2f", f, len(data), s)
			assert.Greater(t, s, 0.6)
		})
	}
}

func TestVipsLossless(t *testing.T) {
	v := newTestVips(t)
	src := photoLike(64, 48, 3)
	for _, f := range []Format{WebP, JXL, AVIF} {
		t.Run(f.String(), func(t *testing.T) {
			skipUnless(t, v, f)
			data, err := v.Encode(f, src, EncodeOptions{Lossless: true, Effort: 1})
			require.NoError(t, err)
			dec, err := v.Decode(f, data)
			require.NoError(t, err)
			if f == AVIF {
				t.Skip("lossless AVIF is only lossless in the identity matrix, which is not checked here")
			}
			assertSamePixels(t, src, dec)
		})
	}
}

func TestVipsAlphaIsStraight(t *testing.T) {
	v := newTestVips(t)
	src := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for y := range 32 {
		for x := range 32 {
			a := uint8(255)
			if x < 16 {
				a = 128
			}
			src.SetNRGBA(x, y, color.NRGBA{R: 200, G: 40, B: 90, A: a})
		}
	}
	for _, f := range []Format{WebP, JXL, AVIF} {
		t.Run(f.String(), func(t *testing.T) {
			skipUnless(t, v, f)
			data, err := v.Encode(f, src, EncodeOptions{Lossless: f != AVIF, Quality: 95, Effort: 1})
			require.NoError(t, err)
			dec, err := v.Decode(f, data)
			require.NoError(t, err)
			n, ok := dec.(*image.NRGBA)
			require.True(t, ok, "%T", dec)
			c := n.NRGBAAt(4, 4)
			assert.InDelta(t, 128, int(c.A), 3)
			assert.InDelta(t, 200, int(c.R), 8, "straight, not premultiplied")
			assert.Equal(t, uint8(255), n.NRGBAAt(24, 4).A)
		})
	}
}

func TestVipsDecodeDoesNotRotate(t *testing.T) {
	v := newTestVips(t)
	var b bytes.Buffer
	require.NoError(t, stdjpeg.Encode(&b, photoLike(80, 40, 1), nil))
	jpg := withSegment(b.Bytes(), 0xE1, exifOrientation(6))
	dec, err := v.Decode(JPEG, jpg)
	require.NoError(t, err)
	assert.Equal(t, 80, dec.Bounds().Dx())
	assert.Equal(t, 40, dec.Bounds().Dy())
}

func TestVipsDecodeKeepsPixelsWithoutOffset(t *testing.T) {
	v := newTestVips(t)
	data, err := v.Encode(JPEG, photoLike(33, 17, 2), EncodeOptions{Quality: 90})
	require.NoError(t, err)
	dec, err := v.Decode(JPEG, data)
	require.NoError(t, err)
	assert.Equal(t, image.Rect(0, 0, 33, 17), dec.Bounds())
	norm, alpha := Normalize(dec)
	assert.False(t, alpha)
	assert.Equal(t, image.Rect(0, 0, 33, 17), norm.Bounds())
}

func TestVipsJPEGOptions(t *testing.T) {
	v := newTestVips(t)
	src := photoLike(256, 192, 5)
	prog, err := v.Encode(JPEG, src, EncodeOptions{Quality: 80, Progressive: true})
	require.NoError(t, err)
	base, err := v.Encode(JPEG, src, EncodeOptions{Quality: 80})
	require.NoError(t, err)
	assert.Contains(t, string(prog), "\xff\xc2", "progressive SOF2 marker")
	assert.NotContains(t, string(base), "\xff\xc2")
	full, err := v.Encode(JPEG, src, EncodeOptions{Quality: 80, Subsampling: image.YCbCrSubsampleRatio422})
	require.NoError(t, err)
	cfg, err := stdjpeg.DecodeConfig(bytes.NewReader(full))
	require.NoError(t, err)
	assert.Equal(t, 256, cfg.Width)
}

func TestVipsQualityIsMonotonic(t *testing.T) {
	v := newTestVips(t)
	src := photoLike(200, 150, 9)
	for _, f := range []Format{JPEG, WebP, AVIF, JXL} {
		t.Run(f.String(), func(t *testing.T) {
			skipUnless(t, v, f)
			lo, err := v.Encode(f, src, EncodeOptions{Quality: 30, Effort: 1})
			require.NoError(t, err)
			hi, err := v.Encode(f, src, EncodeOptions{Quality: 90, Effort: 1})
			require.NoError(t, err)
			assert.Less(t, len(lo), len(hi))
		})
	}
}

func TestVipsAnimatedWebP(t *testing.T) {
	v := newTestVips(t)
	skipUnless(t, v, WebP)
	a := &Animation{DelaysMS: []int{40, 80, 120}, LoopCount: 3}
	for i := range 3 {
		a.Frames = append(a.Frames, photoLike(64, 48, uint64(i+1)))
	}
	data, err := v.EncodeAnimation(WebP, a, EncodeOptions{Quality: 70, Effort: 1})
	require.NoError(t, err)
	assert.Equal(t, 3, riffFrames(data))
	assert.Contains(t, string(data), "ANIM")

	_, err = v.EncodeAnimation(AVIF, a, EncodeOptions{Quality: 70})
	assert.Error(t, err)
}

func TestVipsAnimatedWebPWithAlpha(t *testing.T) {
	v := newTestVips(t)
	skipUnless(t, v, WebP)
	a := &Animation{DelaysMS: []int{50, 50}}
	opaque := photoLike(40, 30, 1)
	clear := image.NewNRGBA(image.Rect(0, 0, 40, 30))
	a.Frames = []image.Image{opaque, clear}
	data, err := v.EncodeAnimation(WebP, a, EncodeOptions{Lossless: true, Effort: 1})
	require.NoError(t, err)
	assert.Equal(t, 2, riffFrames(data))
}

func TestVipsConcurrentUse(t *testing.T) {
	v := newTestVips(t)
	src := photoLike(128, 96, 4)
	errs := make(chan error, 8)
	for range 8 {
		go func() {
			data, err := v.Encode(JPEG, src, EncodeOptions{Quality: 80})
			if err == nil {
				_, err = v.Decode(JPEG, data)
			}
			errs <- err
		}()
	}
	for range 8 {
		require.NoError(t, <-errs)
	}
}

// TestVipsCompare prints a table of size, time and quality of the wasm and vips
// backends on real photos. Every output is decoded by libvips, so that the
// score depends on the encoder only. It runs when MIDOPRESS_COMPARE_DIR points at a
// directory of JPEG files.
func TestVipsCompare(t *testing.T) {
	dir := os.Getenv("MIDOPRESS_COMPARE_DIR")
	if dir == "" {
		t.Skip("set MIDOPRESS_COMPARE_DIR to a directory of JPEG photos")
	}
	v := newTestVips(t)
	w := NewWASM()
	names := []string{"photo-colosseum", "photo-hopetoun-falls", "photo-matterhorn", "photo-monarch", "photo-sunflower"}
	levels := map[Format]int{JPEG: 80, WebP: 80, AVIF: 60, JXL: 80}
	var table bytes.Buffer
	tw := tabwriter.NewWriter(&table, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "photo\tformat\tq\twasm bytes\twasm ms\twasm ssim2\tvips bytes\tvips ms\tvips ssim2\tvips bytes at wasm ssim2")
	for _, n := range names {
		raw, err := os.ReadFile(filepath.Join(dir, n+".jpg"))
		require.NoError(t, err)
		src, err := stdjpeg.Decode(bytes.NewReader(raw))
		require.NoError(t, err)
		ref, _ := Normalize(src)
		ss, err := quality.NewSSIMULACRA2Reference(ref)
		require.NoError(t, err)
		for _, f := range []Format{JPEG, WebP, AVIF, JXL} {
			if !v.Supports(f) {
				continue
			}
			o := EncodeOptions{Quality: levels[f], Effort: 5, Progressive: true}
			type res struct {
				n  int
				d  time.Duration
				sc float64
			}
			run := func(b Backend, o EncodeOptions) res {
				start := time.Now()
				data, err := b.Encode(f, ref, o)
				d := time.Since(start)
				require.NoError(t, err)
				dec, err := v.Decode(f, data)
				require.NoError(t, err)
				sc, err := ss.Score(dec)
				require.NoError(t, err)
				return res{len(data), d, sc}
			}
			rw, rv := run(w, o), run(v, o)
			lo, hi := 1, 100
			best := run(v, EncodeOptions{Quality: 100, Effort: 5, Progressive: true})
			for lo < hi {
				mid := (lo + hi) / 2
				oo := o
				oo.Quality = mid
				r := run(v, oo)
				if r.sc >= rw.sc {
					best, hi = r, mid
				} else {
					lo = mid + 1
				}
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%.1f\t%d\t%d\t%.1f\t%d (q%d)\n", n, f, levels[f], rw.n, rw.d.Milliseconds(), rw.sc, rv.n, rv.d.Milliseconds(), rv.sc, best.n, lo)
		}
	}
	require.NoError(t, tw.Flush())
	fmt.Print(table.String())
}

func TestVipsMozjpegProbe(t *testing.T) {
	v := newTestVips(t)
	t.Logf("mozjpeg detected: %v", v.mozjpeg)
}
