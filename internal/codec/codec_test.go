package codec

import (
	"bytes"
	"image"
	"image/color"
	"image/color/palette"
	stdgif "image/gif"
	stdjpeg "image/jpeg"
	"image/png"
	"io"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"

	"github.com/gen2brain/webp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testLevels = Levels{Brotli: 5, Gzip: 6, Zstd: 3}

func sampleText(n int) []byte {
	return []byte(strings.Repeat("<p>Call me Ishmael. Some years ago, never mind how long.</p>\n", n))
}

func TestParseEncoding(t *testing.T) {
	tests := []struct {
		in   string
		want Encoding
		ok   bool
	}{
		{"", None, true},
		{"identity", None, true},
		{"gzip", Gzip, true},
		{" GZIP ", Gzip, true},
		{"x-gzip", Gzip, true},
		{"br", Brotli, true},
		{"zstd", Zstd, true},
		{"deflate", Deflate, true},
		{"gzip, br", None, false},
		{"compress", None, false},
		{"dcb", None, false},
	}
	for _, tt := range tests {
		got, ok := ParseEncoding(tt.in)
		assert.Equal(t, tt.ok, ok, tt.in)
		if ok {
			assert.Equal(t, tt.want, got, tt.in)
		}
	}
}

func TestCompressRoundTrip(t *testing.T) {
	c := NewText(testLevels)
	src := sampleText(500)

	for _, enc := range []Encoding{Gzip, Brotli, Zstd} {
		t.Run(enc.Token(), func(t *testing.T) {
			var comp bytes.Buffer
			require.NoError(t, c.Compress(enc, &comp, src))
			assert.Less(t, comp.Len(), len(src)/10)

			var plain bytes.Buffer
			require.NoError(t, c.Decode(enc, comp.Bytes(), &plain, int64(len(src))))
			assert.Equal(t, src, plain.Bytes())
		})
	}
}

func TestDecodeLimit(t *testing.T) {
	c := NewText(testLevels)
	src := sampleText(5000)
	for _, enc := range []Encoding{Gzip, Brotli, Zstd} {
		var comp, plain bytes.Buffer
		require.NoError(t, c.Compress(enc, &comp, src))
		err := c.Decode(enc, comp.Bytes(), &plain, 1000)
		assert.ErrorIs(t, err, ErrTooLarge, enc.Token())
	}
}

func TestDecodeGarbage(t *testing.T) {
	c := NewText(testLevels)
	for _, enc := range []Encoding{Gzip, Brotli, Zstd, Deflate} {
		var plain bytes.Buffer
		err := c.Decode(enc, []byte("this is definitely not compressed data"), &plain, 1<<20)
		assert.Error(t, err, enc.Token())
	}
}

func TestDecodeTruncated(t *testing.T) {
	c := NewText(testLevels)
	src := sampleText(500)
	for _, enc := range []Encoding{Gzip, Brotli, Zstd} {
		var comp, plain bytes.Buffer
		require.NoError(t, c.Compress(enc, &comp, src))
		cut := comp.Bytes()[:comp.Len()-6]
		assert.Error(t, c.Decode(enc, cut, &plain, 1<<20), enc.Token())
	}
}

func TestDeflateBothFlavours(t *testing.T) {
	c := NewText(testLevels)
	src := sampleText(100)

	var zl bytes.Buffer
	zw := newZlibWriter(&zl)
	_, _ = zw.Write(src)
	require.NoError(t, zw.Close())

	var raw bytes.Buffer
	fw := newFlateWriter(&raw)
	_, _ = fw.Write(src)
	require.NoError(t, fw.Close())

	for name, data := range map[string][]byte{"zlib": zl.Bytes(), "raw": raw.Bytes()} {
		var plain bytes.Buffer
		require.NoError(t, c.Decode(Deflate, data, &plain, 1<<20), name)
		assert.Equal(t, src, plain.Bytes(), name)
	}
}

func TestStreamingFlush(t *testing.T) {
	c := NewText(testLevels)
	for _, enc := range []Encoding{Gzip, Brotli, Zstd} {
		var comp bytes.Buffer
		w, err := c.NewWriter(enc, &comp, 0)
		require.NoError(t, err)
		_, _ = w.Write([]byte("hello "))
		require.NoError(t, w.Flush())
		flushed := comp.Len()
		assert.Positive(t, flushed, "flush must push data out for %s", enc.Token())
		_, _ = w.Write([]byte("world"))
		require.NoError(t, w.Close())

		var plain bytes.Buffer
		require.NoError(t, c.Decode(enc, comp.Bytes(), &plain, 100))
		assert.Equal(t, "hello world", plain.String())
	}
}

func TestPoolReuseIsClean(t *testing.T) {
	c := NewText(testLevels)
	for _, enc := range []Encoding{Gzip, Brotli, Zstd} {
		for i := range 20 {
			src := sampleText(10 + i)
			var comp, plain bytes.Buffer
			require.NoError(t, c.Compress(enc, &comp, src))
			require.NoError(t, c.Decode(enc, comp.Bytes(), &plain, 1<<20))
			require.Equal(t, src, plain.Bytes(), "%s round %d", enc.Token(), i)
		}
	}
}

func TestConcurrentCompression(t *testing.T) {
	c := NewText(testLevels)
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			enc := []Encoding{Gzip, Brotli, Zstd}[g%3]
			for i := range 50 {
				src := sampleText(5 + (g*i)%40)
				var comp, plain bytes.Buffer
				if !assert.NoError(t, c.Compress(enc, &comp, src)) {
					return
				}
				if !assert.NoError(t, c.Decode(enc, comp.Bytes(), &plain, 1<<20)) {
					return
				}
				if !assert.Equal(t, src, plain.Bytes()) {
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestLGWin(t *testing.T) {
	assert.Equal(t, 22, lgWin(0))
	assert.Equal(t, 22, lgWin(-1))
	assert.Equal(t, 10, lgWin(1))
	assert.Equal(t, 10, lgWin(1024))
	assert.Equal(t, 11, lgWin(1025))
	assert.Equal(t, 22, lgWin(1<<30))
}

func TestCompressUnsupported(t *testing.T) {
	c := NewText(testLevels)
	_, err := c.NewWriter(Deflate, io.Discard, 0)
	assert.Error(t, err)
	_, err = c.NewWriter(None, io.Discard, 0)
	assert.Error(t, err)
}

func noisyImage(w, h int) *image.RGBA {
	rng := rand.New(rand.NewPCG(7, 9))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			n := uint8(rng.IntN(48))
			img.SetRGBA(x, y, color.RGBA{R: uint8(x*255/w) ^ n, G: uint8(y*255/h) ^ n, B: n * 3, A: 255})
		}
	}
	return img
}

func encodeJPEG(t *testing.T, q int) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, stdjpeg.Encode(&b, noisyImage(200, 150), &stdjpeg.Options{Quality: q}))
	return b.Bytes()
}

func encodePNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, noisyImage(100, 100)))
	return b.Bytes()
}

func staticGIF(t *testing.T) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, 64, 64), palette.Plan9)
	for i := range img.Pix {
		img.Pix[i] = uint8(i / 64)
	}
	var b bytes.Buffer
	require.NoError(t, stdgif.Encode(&b, img, nil))
	return b.Bytes()
}

func animatedGIF(t *testing.T, frames int) []byte {
	t.Helper()
	g := &stdgif.GIF{}
	for f := range frames {
		img := image.NewPaletted(image.Rect(0, 0, 32, 32), palette.Plan9)
		for i := range img.Pix {
			img.Pix[i] = uint8(i + f*17)
		}
		g.Image = append(g.Image, img)
		g.Delay = append(g.Delay, 5)
	}
	var b bytes.Buffer
	require.NoError(t, stdgif.EncodeAll(&b, g))
	return b.Bytes()
}

func TestSniffImage(t *testing.T) {
	assert.Equal(t, KindJPEG, SniffImage(encodeJPEG(t, 80)))
	assert.Equal(t, KindPNG, SniffImage(encodePNG(t)))
	assert.Equal(t, KindGIF, SniffImage(staticGIF(t)))
	assert.Equal(t, KindUnknown, SniffImage([]byte("<html>")))
	assert.Equal(t, KindUnknown, SniffImage(nil))
	assert.Equal(t, KindWebP, SniffImage([]byte("RIFF....WEBPVP8 ")))
	assert.Equal(t, KindAVIF, SniffImage([]byte("\x00\x00\x00\x1cftypavif\x00\x00\x00\x00avifmif1")))
	assert.Equal(t, KindAVIF, SniffImage([]byte("\x00\x00\x00\x1cftypavis\x00\x00\x00\x00avismif1")))
	assert.Equal(t, KindUnknown, SniffImage([]byte("\x00\x00\x00\x1cftypheic\x00\x00\x00\x00mif1heic")))
	assert.Equal(t, KindUnknown, SniffImage([]byte("RIFF....WAVEfmt ")))
	assert.Equal(t, KindJXL, SniffImage([]byte{0xFF, 0x0A, 0x00}))
	assert.Equal(t, KindJXL, SniffImage([]byte("\x00\x00\x00\x0cJXL \x0d\x0a\x87\x0a\x00\x00")))
}

func TestImageSizeOfModernFormats(t *testing.T) {
	m := image.NewRGBA(image.Rect(0, 0, 480, 360))
	var enc bytes.Buffer
	require.NoError(t, webp.Encode(&enc, m, webp.Options{Quality: 80}))
	w, h, err := ImageSize(KindWebP, enc.Bytes())
	require.NoError(t, err)
	assert.Equal(t, [2]int{480, 360}, [2]int{w, h})
	_, _, err = ImageSize(KindWebP, []byte("RIFF....WEBPnothing"))
	assert.Error(t, err)
	_, _, err = ImageSize(KindAVIF, []byte("not an avif"))
	assert.Error(t, err)
	_, _, err = ImageSize(KindJXL, []byte("\xff\x0a"))
	assert.Error(t, err)
}

func TestImageSize(t *testing.T) {
	w, h, err := ImageSize(KindJPEG, encodeJPEG(t, 80))
	require.NoError(t, err)
	assert.Equal(t, [2]int{200, 150}, [2]int{w, h})

	w, h, err = ImageSize(KindPNG, encodePNG(t))
	require.NoError(t, err)
	assert.Equal(t, [2]int{100, 100}, [2]int{w, h})

	w, h, err = ImageSize(KindGIF, staticGIF(t))
	require.NoError(t, err)
	assert.Equal(t, [2]int{64, 64}, [2]int{w, h})

	_, _, err = ImageSize(KindJPEG, []byte("garbage"))
	assert.Error(t, err)
}

func TestGIFFrames(t *testing.T) {
	n, err := GIFFrames(staticGIF(t), 2)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	n, err = GIFFrames(animatedGIF(t, 5), 2)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "stops at the limit")

	n, err = GIFFrames(animatedGIF(t, 5), 100)
	require.NoError(t, err)
	assert.Equal(t, 5, n)
}

func TestGIFFramesMalformed(t *testing.T) {
	good := animatedGIF(t, 3)
	for cut := 0; cut < len(good)-1; cut += 7 {
		_, _ = GIFFrames(good[:cut], 100) // must never panic or read out of range
	}
	_, err := GIFFrames([]byte("not a gif at all"), 2)
	assert.Error(t, err)
	_, err = GIFFrames(good[:len(good)-1], 100)
	assert.Error(t, err, "missing trailer")
}

func BenchmarkCompressBrotli(b *testing.B) {
	c := NewText(testLevels)
	src := sampleText(800)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	var buf bytes.Buffer
	for b.Loop() {
		buf.Reset()
		_ = c.Compress(Brotli, &buf, src)
	}
}

func BenchmarkCompressGzip(b *testing.B) {
	c := NewText(testLevels)
	src := sampleText(800)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	var buf bytes.Buffer
	for b.Loop() {
		buf.Reset()
		_ = c.Compress(Gzip, &buf, src)
	}
}

func BenchmarkCompressZstd(b *testing.B) {
	c := NewText(testLevels)
	src := sampleText(800)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	var buf bytes.Buffer
	for b.Loop() {
		buf.Reset()
		_ = c.Compress(Zstd, &buf, src)
	}
}
