package imgmeta

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"github.com/gen2brain/webp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func chunk(typ string, data []byte) []byte {
	b := append([]byte(typ), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(data)))
	b = append(b, data...)
	if len(data)%2 == 1 {
		b = append(b, 0)
	}
	return b
}

func riff(chunks ...[]byte) []byte {
	body := []byte("WEBP")
	for _, c := range chunks {
		body = append(body, c...)
	}
	b := append([]byte("RIFF"), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(body)))
	return append(b, body...)
}

func vp8x(flags byte, w, h int) []byte {
	d := make([]byte, 10)
	d[0] = flags
	w, h = w-1, h-1
	d[4], d[5], d[6] = byte(w), byte(w>>8), byte(w>>16)
	d[7], d[8], d[9] = byte(h), byte(h>>8), byte(h>>16)
	return chunk("VP8X", d)
}

func vp8(w, h int) []byte {
	d := make([]byte, 10)
	copy(d[3:], []byte{0x9d, 0x01, 0x2a})
	binary.LittleEndian.PutUint16(d[6:], uint16(w))
	binary.LittleEndian.PutUint16(d[8:], uint16(h))
	return chunk("VP8 ", d)
}

func vp8l(w, h int, alpha bool) []byte {
	d := make([]byte, 5)
	d[0] = 0x2f
	v := uint32(w-1) | uint32(h-1)<<14
	if alpha {
		v |= 1 << 28
	}
	binary.LittleEndian.PutUint32(d[1:], v)
	return chunk("VP8L", d)
}

func TestParseWebP(t *testing.T) {
	t.Run("lossy simple file", func(t *testing.T) {
		w, err := ParseWebP(riff(vp8(550, 368)))
		require.NoError(t, err)
		assert.Equal(t, WebP{Width: 550, Height: 368, Orientation: 1}, *w)
	})
	t.Run("lossless with alpha", func(t *testing.T) {
		w, err := ParseWebP(riff(vp8l(64, 32, true)))
		require.NoError(t, err)
		assert.True(t, w.Lossless)
		assert.True(t, w.Alpha)
		assert.Equal(t, [2]int{64, 32}, [2]int{w.Width, w.Height})
	})
	t.Run("extended: size comes from VP8X, flags and the profile are read", func(t *testing.T) {
		icc := bytes.Repeat([]byte{7}, 33) // odd length: the chunk is padded
		w, err := ParseWebP(riff(vp8x(0x20|0x10, 4000, 3000), chunk("ICCP", icc), vp8(4000, 3000)))
		require.NoError(t, err)
		assert.Equal(t, [2]int{4000, 3000}, [2]int{w.Width, w.Height})
		assert.True(t, w.Alpha)
		assert.False(t, w.Animated)
		assert.Equal(t, icc, w.ICC)
	})
	t.Run("animation, by flag or by chunk", func(t *testing.T) {
		w, err := ParseWebP(riff(vp8x(0x02, 100, 100), chunk("ANIM", make([]byte, 6)), chunk("ANMF", make([]byte, 16))))
		require.NoError(t, err)
		assert.True(t, w.Animated)
		w, err = ParseWebP(riff(vp8(10, 10), chunk("ANMF", make([]byte, 16))))
		require.NoError(t, err)
		assert.True(t, w.Animated)
	})
	t.Run("EXIF orientation", func(t *testing.T) {
		tiff := []byte{'M', 'M', 0, '*', 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, 6, 0, 0, 0, 0, 0, 0}
		w, err := ParseWebP(riff(vp8x(0x08, 10, 10), vp8(10, 10), chunk("EXIF", tiff)))
		require.NoError(t, err)
		assert.Equal(t, 6, w.Orientation)
		w, err = ParseWebP(riff(vp8x(0x08, 10, 10), vp8(10, 10), chunk("EXIF", append([]byte("Exif\x00\x00"), tiff...))))
		require.NoError(t, err)
		assert.Equal(t, 6, w.Orientation, "with the Exif prefix some writers add")
	})
	t.Run("what is not a WebP", func(t *testing.T) {
		for name, b := range map[string][]byte{
			"empty": nil, "short": []byte("RIFF"), "png": []byte("\x89PNG\r\n\x1a\n0123456789012"),
			"no image chunk": riff(chunk("ICCP", []byte{1, 2})),
			"truncated":      riff(vp8(5, 5))[:22],
		} {
			_, err := ParseWebP(b)
			assert.Error(t, err, name)
		}
	})
	t.Run("a file made by libwebp", func(t *testing.T) {
		raw := libwebpFile(t, 480, 360)
		w, err := ParseWebP(raw)
		require.NoError(t, err)
		assert.Equal(t, [2]int{480, 360}, [2]int{w.Width, w.Height})
		assert.False(t, w.Animated)
		assert.Nil(t, w.ICC)
	})
}

// libwebpFile is a lossy WebP made by the encoder of libwebp.
func libwebpFile(t testing.TB, w, h int) []byte {
	t.Helper()
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			m.SetRGBA(x, y, color.RGBA{R: uint8(x * 255 / w), G: uint8(y * 255 / h), B: uint8(x ^ y), A: 255})
		}
	}
	var b bytes.Buffer
	require.NoError(t, webp.Encode(&b, m, webp.Options{Quality: 80}))
	return b.Bytes()
}

func box(typ string, parts ...[]byte) []byte {
	var body []byte
	for _, p := range parts {
		body = append(body, p...)
	}
	b := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(b, uint32(8+len(body)))
	copy(b[4:], typ)
	return append(b, body...)
}

func fullBox(typ string, parts ...[]byte) []byte {
	return box(typ, append([][]byte{{0, 0, 0, 0}}, parts...)...)
}

func u32(v ...uint32) []byte {
	var b []byte
	for _, x := range v {
		b = binary.BigEndian.AppendUint32(b, x)
	}
	return b
}

func ftyp(brand string) []byte { return box("ftyp", []byte(brand), u32(0), []byte("mif1"+brand)) }

func avif(brand string, props ...[]byte) []byte {
	ipco := box("ipco", props...)
	return append(ftyp(brand), fullBox("meta", box("iprp", ipco))...)
}

func ispe(w, h uint32) []byte { return fullBox("ispe", u32(w, h)) }

func colrNCLX(primaries, transfer, matrix uint16, full bool) []byte {
	b := []byte("nclx")
	b = binary.BigEndian.AppendUint16(b, primaries)
	b = binary.BigEndian.AppendUint16(b, transfer)
	b = binary.BigEndian.AppendUint16(b, matrix)
	if full {
		b = append(b, 0x80)
	} else {
		b = append(b, 0)
	}
	return box("colr", b)
}

func TestParseAVIF(t *testing.T) {
	t.Run("a still", func(t *testing.T) {
		a, err := ParseAVIF(avif("avif", ispe(722, 1024), fullBox("pixi", []byte{3, 8, 8, 8})))
		require.NoError(t, err)
		assert.Equal(t, [2]int{722, 1024}, [2]int{a.Width, a.Height})
		assert.False(t, a.Animated)
		assert.False(t, a.Transformed)
		assert.Equal(t, 8, a.Depth)
		assert.Empty(t, a.NCLX)
	})
	t.Run("the largest picture is the canvas, not the alpha plane or a tile", func(t *testing.T) {
		a, err := ParseAVIF(avif("avif", ispe(256, 256), ispe(1024, 768), ispe(1024, 768)))
		require.NoError(t, err)
		assert.Equal(t, [2]int{1024, 768}, [2]int{a.Width, a.Height})
	})
	t.Run("a sequence is an animation", func(t *testing.T) {
		a, err := ParseAVIF(avif("avis", ispe(10, 10)))
		require.NoError(t, err)
		assert.True(t, a.Animated)
	})
	t.Run("rotation, mirror and crop are transformations", func(t *testing.T) {
		for _, typ := range []string{"irot", "imir", "clap"} {
			a, err := ParseAVIF(avif("avif", ispe(10, 10), box(typ, []byte{1})))
			require.NoError(t, err, typ)
			assert.True(t, a.Transformed, typ)
		}
	})
	t.Run("colour by code points", func(t *testing.T) {
		a, err := ParseAVIF(avif("avif", ispe(10, 10), colrNCLX(1, 13, 1, true), fullBox("pixi", []byte{3, 10, 10, 10})))
		require.NoError(t, err)
		require.Len(t, a.NCLX, 1)
		assert.Equal(t, AVIFColour{Primaries: 1, Transfer: 13, Matrix: 1, FullRange: true}, a.NCLX[0])
		assert.True(t, a.NCLX[0].SDRsRGB())
		assert.Equal(t, 10, a.Depth)
	})
	t.Run("which colour descriptions are plain SDR", func(t *testing.T) {
		for c, want := range map[AVIFColour]bool{
			{Primaries: 1, Transfer: 13}: true, {Primaries: 1, Transfer: 1}: true, {Primaries: 2, Transfer: 2}: true,
			{Primaries: 1, Transfer: 6}:   true,
			{Primaries: 12, Transfer: 13}: false, // Display P3
			{Primaries: 9, Transfer: 1}:   false, // BT.2020
			{Primaries: 9, Transfer: 16}:  false, // PQ
			{Primaries: 1, Transfer: 18}:  false, // HLG
			{Primaries: 1, Transfer: 8}:   false, // linear
		} {
			assert.Equal(t, want, c.SDRsRGB(), "%+v", c)
		}
	})
	t.Run("an embedded profile", func(t *testing.T) {
		prof := bytes.Repeat([]byte{9}, 40)
		a, err := ParseAVIF(avif("avif", ispe(10, 10), box("colr", []byte("prof"), prof)))
		require.NoError(t, err)
		assert.Equal(t, prof, a.ICC)
	})
	t.Run("what is not an AVIF", func(t *testing.T) {
		for name, b := range map[string][]byte{
			"empty": nil, "png": []byte("\x89PNG\r\n\x1a\n0123456789012"),
			"heic":      avif("heic", ispe(10, 10)),
			"no size":   avif("avif"),
			"no ftyp":   fullBox("meta", box("iprp", box("ipco", ispe(10, 10)))),
			"truncated": avif("avif", ispe(10, 10))[:30],
		} {
			_, err := ParseAVIF(b)
			assert.Error(t, err, name)
		}
	})
}

// TestRealModernFiles checks the parsers against files that were downloaded from
// sites and from the libavif sample collection, when they are there. The
// expected values are the ones file(1) and a decoder report for those files.
func TestRealModernFiles(t *testing.T) {
	dir := "../../artifacts/corpus-modern"
	if _, err := os.Stat(dir); err != nil {
		t.Skip("no corpus of real WebP and AVIF files in " + dir)
	}
	type want struct {
		w, h        int
		transformed bool
		hasICC      bool
	}
	for name, e := range map[string]want{
		"gstatic-1.webp": {550, 368, false, false}, "gstatic-4.webp": {1024, 772, false, false},
		"ytimg-max.webp": {1278, 720, false, false}, "unsplash-1600.webp": {1600, 1068, false, true},
		"linku-kimono.avif": {722, 1024, false, false}, "linku-kimono-rot90.avif": {1024, 722, true, false},
		"linku-kimono-mirror.avif": {722, 1024, true, false}, "linku-kimono-crop.avif": {722, 1024, true, false},
		"linku-hato.avif": {3082, 2048, false, false}, "unsplash-1600.avif": {1600, 1068, false, true},
	} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		if filepath.Ext(name) == ".webp" {
			w, err := ParseWebP(raw)
			require.NoError(t, err, name)
			assert.Equal(t, [2]int{e.w, e.h}, [2]int{w.Width, w.Height}, name)
			assert.Equal(t, e.hasICC, w.ICC != nil, name)
			continue
		}
		a, err := ParseAVIF(raw)
		require.NoError(t, err, name)
		assert.Equal(t, [2]int{e.w, e.h}, [2]int{a.Width, a.Height}, name)
		assert.Equal(t, e.transformed, a.Transformed, name)
		assert.Equal(t, e.hasICC, a.ICC != nil, name)
	}
}

func FuzzParseModern(f *testing.F) {
	f.Add(riff(vp8(5, 5)))
	f.Add(riff(vp8x(0x22, 9, 9), chunk("ICCP", []byte{1}), vp8l(9, 9, true)))
	f.Add(avif("avif", ispe(8, 8), colrNCLX(1, 13, 1, false), box("irot", []byte{1})))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = ParseWebP(b)
		_, _ = ParseAVIF(b)
	})
}
