package img

import (
	"bytes"
	"encoding/binary"
	stdjpeg "image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func skipWithoutJXL(t testing.TB) {
	t.Helper()
	if !JPEGRecompressAvailable() {
		t.Skip("libjxl is not available")
	}
}

func baselineJPEG(t testing.TB, w, h int, seed uint64) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, stdjpeg.Encode(&b, photoLike(w, h, seed), &stdjpeg.Options{Quality: 88}))
	return b.Bytes()
}

// withEXIF splices an APP1 segment with EXIF-looking data after SOI.
func withEXIF(t testing.TB, jpg []byte) []byte {
	t.Helper()
	payload := append([]byte("Exif\x00\x00II*\x00\x08\x00\x00\x00"), make([]byte, 40)...)
	var seg bytes.Buffer
	seg.Write([]byte{0xff, 0xe1})
	require.NoError(t, binary.Write(&seg, binary.BigEndian, uint16(len(payload)+2)))
	seg.Write(payload)
	out := append([]byte{}, jpg[:2]...)
	out = append(out, seg.Bytes()...)
	return append(out, jpg[2:]...)
}

func jpegtran(t testing.TB, in []byte, args ...string) []byte {
	t.Helper()
	path, err := exec.LookPath("jpegtran")
	if err != nil {
		t.Skip("jpegtran is not installed")
	}
	cmd := exec.Command(path, args...)
	cmd.Stdin = bytes.NewReader(in)
	out, err := cmd.Output()
	require.NoError(t, err)
	return out
}

func cjpeg(t testing.TB, in []byte, args ...string) []byte {
	t.Helper()
	path, err := exec.LookPath("cjpeg")
	if err != nil {
		t.Skip("cjpeg is not installed")
	}
	var ppm bytes.Buffer
	m, err := stdjpeg.Decode(bytes.NewReader(in))
	require.NoError(t, err)
	b := m.Bounds()
	ppm.WriteString("P6\n" + itoa(b.Dx()) + " " + itoa(b.Dy()) + "\n255\n")
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := m.At(x, y).RGBA()
			ppm.Write([]byte{byte(r >> 8), byte(g >> 8), byte(bl >> 8)})
		}
	}
	cmd := exec.Command(path, args...)
	cmd.Stdin = &ppm
	out, err := cmd.Output()
	require.NoError(t, err)
	return out
}

func itoa(n int) string {
	var b [20]byte
	i := len(b)
	for ; n > 0; n /= 10 {
		i--
		b[i] = byte('0' + n%10)
	}
	return string(b[i:])
}

func TestRecompressRoundTrip(t *testing.T) {
	skipWithoutJXL(t)
	base := baselineJPEG(t, 321, 243, 1)
	cases := map[string]func(t *testing.T) []byte{
		"baseline420": func(t *testing.T) []byte { return base },
		"gray":        func(t *testing.T) []byte { return cjpeg(t, base, "-grayscale", "-quality", "85") },
		"exif":        func(t *testing.T) []byte { return withEXIF(t, base) },
		"progressive": func(t *testing.T) []byte { return jpegtran(t, base, "-progressive") },
		"restart":     func(t *testing.T) []byte { return jpegtran(t, base, "-restart", "3") },
		"optimized":   func(t *testing.T) []byte { return jpegtran(t, base, "-optimize") },
		"444":         func(t *testing.T) []byte { return cjpeg(t, base, "-sample", "1x1", "-quality", "90") },
		"422":         func(t *testing.T) []byte { return cjpeg(t, base, "-sample", "2x1", "-quality", "90") },
		"440":         func(t *testing.T) []byte { return cjpeg(t, base, "-sample", "1x2", "-quality", "90") },
		"progressive444": func(t *testing.T) []byte {
			return cjpeg(t, base, "-sample", "1x1", "-progressive", "-quality", "75")
		},
		"tiny": func(t *testing.T) []byte { return baselineJPEG(t, 1, 1, 2) },
		"odd":  func(t *testing.T) []byte { return baselineJPEG(t, 17, 33, 2) },
	}
	be := NewWASM()
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			jpg := mk(t)
			jxl, err := be.RecompressJPEG(jpg)
			require.NoError(t, err)
			back, err := ReconstructJPEG(jxl)
			require.NoError(t, err)
			assert.True(t, bytes.Equal(jpg, back))
			t.Logf("%s: %d -> %d bytes (%.1f%%)", name, len(jpg), len(jxl), 100*float64(len(jxl))/float64(len(jpg)))
		})
	}
}

func TestRecompressRejects(t *testing.T) {
	skipWithoutJXL(t)
	be := NewWASM()
	base := baselineJPEG(t, 64, 64, 1)

	_, err := be.RecompressJPEG(nil)
	require.Error(t, err)
	_, err = be.RecompressJPEG([]byte("not a jpeg at all"))
	require.Error(t, err)
	_, err = be.RecompressJPEG(base[:len(base)/2])
	require.Error(t, err)

	if _, lerr := exec.LookPath("jpegtran"); lerr == nil {
		ar := jpegtran(t, base, "-arithmetic")
		_, err = be.RecompressJPEG(ar)
		require.Error(t, err, "arithmetic coded jpeg must be refused or reproduced exactly")
	}

	_, err = ReconstructJPEG([]byte("garbage"))
	require.Error(t, err)

	jxl, err := be.RecompressJPEG(base)
	require.NoError(t, err)
	_, err = ReconstructJPEG(jxl[:len(jxl)/2])
	require.Error(t, err)
	corrupt := append([]byte{}, jxl...)
	for i := len(corrupt) / 2; i < len(corrupt); i += 7 {
		corrupt[i] ^= 0x5a
	}
	if back, err := ReconstructJPEG(corrupt); err == nil {
		assert.NotEqual(t, base, back)
	}

	pixels, err := be.Encode(JXL, photoLike(32, 32, 1), EncodeOptions{Quality: 80})
	require.NoError(t, err)
	_, err = ReconstructJPEG(pixels)
	require.Error(t, err, "a pixel jxl has no jpeg to rebuild")
}

func TestRecompressUnavailable(t *testing.T) {
	if JPEGRecompressAvailable() {
		t.Skip("libjxl is available")
	}
	_, err := NewWASM().RecompressJPEG(baselineJPEG(t, 16, 16, 1))
	assert.ErrorIs(t, err, ErrUnsupported)
	_, err = ReconstructJPEG([]byte("x"))
	assert.ErrorIs(t, err, ErrUnsupported)
}

func TestRecompressEngine(t *testing.T) {
	skipWithoutJXL(t)
	jpg := baselineJPEG(t, 400, 300, 5)
	p, _ := Lookup(ProfileLossless)
	res, err := testEngine.Optimize(t.Context(), Input{Raw: jpg, Format: JPEG, Profile: p, Caps: Caps{JXL: true}})
	require.NoError(t, err)
	if res.Format != JXL {
		t.Skipf("engine kept %v", res.Format)
	}
	back, err := ReconstructJPEG(res.Data)
	require.NoError(t, err)
	assert.Equal(t, jpg, back)
}

func TestRecompressTooLarge(t *testing.T) {
	skipWithoutJXL(t)
	_, err := NewWASM().RecompressJPEG(make([]byte, maxJPEGRecompressBytes+1))
	assert.ErrorIs(t, err, ErrUnsupported)
}

func corpusJPEGs(t testing.TB) [][]byte {
	t.Helper()
	dir := os.Getenv("JXL_CORPUS")
	if dir == "" {
		dir = "/tmp/opencode/corpus"
	}
	var all [][]byte
	for _, pat := range []string{"photo-*.jpg", "gallery-*.jpg"} {
		files, _ := filepath.Glob(filepath.Join(dir, pat))
		for _, f := range files {
			b, err := os.ReadFile(f)
			require.NoError(t, err)
			all = append(all, b)
		}
	}
	if len(all) == 0 {
		t.Skip("no JPEG corpus")
	}
	return all
}

func TestRecompressCorpus(t *testing.T) {
	skipWithoutJXL(t)
	corpus := corpusJPEGs(t)
	be := NewWASM()
	var in, out, encBytes, recBytes int
	var encTime, recTime, rawEncTime time.Duration
	for _, jpg := range corpus {
		start := time.Now()
		_, _ = jxlNativeEncodeJPEG(jpg)
		rawEncTime += time.Since(start)
		start = time.Now()
		jxl, err := be.RecompressJPEG(jpg)
		d := time.Since(start)
		if err != nil {
			t.Logf("skipped one jpeg: %v", err)
			continue
		}
		encTime += d
		encBytes += len(jpg)
		start = time.Now()
		back, err := ReconstructJPEG(jxl)
		recTime += time.Since(start)
		recBytes += len(jpg)
		require.NoError(t, err)
		require.True(t, bytes.Equal(jpg, back))
		in += len(jpg)
		out += len(jxl)
	}
	require.NotZero(t, in)
	t.Logf("%d jpegs: %d -> %d bytes, %.2f%% reduction", len(corpus), in, out, 100*(1-float64(out)/float64(in)))
	t.Logf("encode alone: %.1f MB/s, recompress incl. verification: %.1f MB/s, reconstruct alone: %.1f MB/s",
		float64(encBytes)/1e6/rawEncTime.Seconds(), float64(encBytes)/1e6/encTime.Seconds(), float64(recBytes)/1e6/recTime.Seconds())
	assert.Less(t, out, in)
}

// TestRecompressCrossCheck compares with libjxl's own tools when installed.
func TestRecompressCrossCheck(t *testing.T) {
	skipWithoutJXL(t)
	cjxl, err1 := exec.LookPath("cjxl")
	djxl, err2 := exec.LookPath("djxl")
	if err1 != nil || err2 != nil {
		t.Skip("cjxl/djxl are not installed")
	}
	corpus := corpusJPEGs(t)
	dir := t.TempDir()
	be := NewWASM()
	var ours, theirs int
	for i, jpg := range corpus {
		src := filepath.Join(dir, "in.jpg")
		require.NoError(t, os.WriteFile(src, jpg, 0o600))

		mine, err := be.RecompressJPEG(jpg)
		if err != nil {
			continue
		}
		mineFile := filepath.Join(dir, "mine.jxl")
		require.NoError(t, os.WriteFile(mineFile, mine, 0o600))
		back := filepath.Join(dir, "back.jpg")
		require.NoError(t, exec.Command(djxl, mineFile, back).Run(), "djxl on our file, image %d", i)
		got, err := os.ReadFile(back)
		require.NoError(t, err)
		require.True(t, bytes.Equal(jpg, got), "djxl did not rebuild image %d", i)

		ref := filepath.Join(dir, "ref.jxl")
		require.NoError(t, exec.Command(cjxl, "--lossless_jpeg=1", "-e", "7", src, ref).Run())
		refData, err := os.ReadFile(ref)
		require.NoError(t, err)
		rec, err := ReconstructJPEG(refData)
		require.NoError(t, err)
		require.True(t, bytes.Equal(jpg, rec), "we did not rebuild cjxl output of image %d", i)
		ours += len(mine)
		theirs += len(refData)
	}
	t.Logf("our total %d bytes, cjxl total %d bytes", ours, theirs)
	assert.InDelta(t, float64(theirs), float64(ours), 0.03*float64(theirs))
}

// TestRecompressGCStress runs many concurrent round trips while the collector
// is forced to run, to catch native code holding pointers the runtime freed or
// moved. Run it with -race, GOGC=1 and GODEBUG=gccheckmark=1.
func TestRecompressGCStress(t *testing.T) {
	skipWithoutJXL(t)
	iters := 300
	if testing.Short() {
		iters = 20
	}
	inputs := [][]byte{baselineJPEG(t, 96, 64, 1), baselineJPEG(t, 200, 150, 2), withEXIF(t, baselineJPEG(t, 64, 48, 3)), baselineJPEG(t, 33, 77, 4)}
	be := NewWASM()
	workers := 2 * runtime.GOMAXPROCS(0)
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				runtime.GC()
				time.Sleep(time.Millisecond)
			}
		}
	}()
	for w := range workers {
		wg.Go(func() {
			for i := range iters {
				jpg := append([]byte(nil), inputs[(w+i)%len(inputs)]...)
				jxl, err := be.RecompressJPEG(jpg)
				if err != nil {
					errs <- err
					return
				}
				back, err := ReconstructJPEG(jxl)
				if err != nil || !bytes.Equal(back, jpg) {
					errs <- assert.AnError
					return
				}
				_ = make([]byte, 1<<16)
			}
		})
	}
	wg.Wait()
	close(stop)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

func BenchmarkRecompressJPEG(b *testing.B) {
	skipWithoutJXL(b)
	jpg := baselineJPEG(b, 1024, 768, 1)
	be := NewWASM()
	b.SetBytes(int64(len(jpg)))
	for b.Loop() {
		if _, err := be.RecompressJPEG(jpg); err != nil {
			b.Fatal(err)
		}
	}
}
