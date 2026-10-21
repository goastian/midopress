package imgmeta

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func be32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }

func samePNGPixels(t testing.TB, a, b []byte) {
	t.Helper()
	ia, err := png.Decode(bytes.NewReader(a))
	require.NoError(t, err)
	ib, err := png.Decode(bytes.NewReader(b))
	require.NoError(t, err)
	require.Equal(t, ia, ib)
}

func testPNG(t testing.TB) []byte {
	return encodePNG(t, gradient(40, 30, 3, 10))
}

func TestParsePNGBasics(t *testing.T) {
	p, err := ParsePNG(testPNG(t))
	require.NoError(t, err)
	assert.Equal(t, 40, p.Width)
	assert.Equal(t, 30, p.Height)
	assert.Equal(t, 8, p.BitDepth)
	assert.Equal(t, 2, p.ColorType)
	assert.False(t, p.Interlaced || p.Animated || p.HasPalette || p.HasTransparency)
	assert.False(t, p.HasSRGB || p.HasGamma || p.HasChroma || p.HasEXIF || p.HasCICP)
	assert.Nil(t, p.ICC)
	assert.Zero(t, p.Gamma)
	assert.Equal(t, 1, p.Orientation)
	assert.Zero(t, p.AncillaryBytes)
	assert.False(t, p.NeedsColorManagement())

	pal := image.NewPaletted(image.Rect(0, 0, 8, 8), color.Palette{color.NRGBA{1, 2, 3, 255}, color.NRGBA{9, 9, 9, 0}})
	p, err = ParsePNG(encodePNG(t, pal))
	require.NoError(t, err)
	assert.Equal(t, 3, p.ColorType)
	assert.True(t, p.HasPalette)
	assert.True(t, p.HasTransparency)

	p, err = ParsePNG(encodePNG(t, image.NewGray16(image.Rect(0, 0, 4, 4))))
	require.NoError(t, err)
	assert.Equal(t, 16, p.BitDepth)
	assert.Equal(t, 0, p.ColorType)
}

func TestParseStripPNGMetadata(t *testing.T) {
	icc := adobeProfile()
	text := pngChunkBytes("tEXt", []byte("Comment\x00hello"))
	phys := pngChunkBytes("pHYs", []byte{0, 0, 0x0B, 0x13, 0, 0, 0x0B, 0x13, 1})
	tm := pngChunkBytes("tIME", []byte{0x07, 0xE9, 1, 2, 3, 4, 5})
	exif := pngChunkBytes("eXIf", exifPayload(binary.BigEndian, 6)[6:])
	bkgd := pngChunkBytes("bKGD", []byte{0, 0, 0, 0, 0, 0})
	ztxt := pngChunkBytes("zTXt", []byte("k\x00\x00xx"))
	private := pngChunkBytes("prVt", []byte("zzz"))
	gama := pngChunkBytes("gAMA", be32(45455))
	chrm := pngChunkBytes("cHRM", make([]byte, 32))
	sbit := pngChunkBytes("sBIT", []byte{8, 8, 8, 8})
	iccp := iccpChunk(icc)
	removed := len(text) + len(phys) + len(tm) + len(exif) + len(bkgd) + len(ztxt) + len(private)
	raw := injectPNG(testPNG(t), text, phys, tm, gama, chrm, iccp, exif, bkgd, ztxt, private, sbit)

	p, err := ParsePNG(raw)
	require.NoError(t, err)
	assert.Equal(t, removed, p.AncillaryBytes)
	assert.True(t, p.HasGamma && p.HasChroma && p.HasEXIF)
	assert.InDelta(t, 2.2, p.Gamma, 0.001)
	assert.Equal(t, icc, p.ICC)
	assert.Equal(t, 6, p.Orientation)
	assert.True(t, p.NeedsColorManagement())

	out, err := StripPNG(raw)
	require.NoError(t, err)
	assert.Len(t, out, len(raw)-removed)
	samePNGPixels(t, raw, out)
	q, err := ParsePNG(out)
	require.NoError(t, err)
	assert.Zero(t, q.AncillaryBytes)
	assert.Equal(t, icc, q.ICC)
	assert.True(t, q.HasGamma && q.HasChroma)
	assert.False(t, q.HasEXIF)
	assert.Contains(t, string(out), "sBIT")

	again, err := StripPNG(out)
	require.NoError(t, err)
	assert.Same(t, &out[0], &again[0])
}

func TestStripPNGTrailingData(t *testing.T) {
	base := testPNG(t)
	raw := append(append([]byte{}, base...), []byte("trailing garbage")...)
	p, err := ParsePNG(raw)
	require.NoError(t, err)
	assert.Equal(t, len("trailing garbage"), p.AncillaryBytes)
	out, err := StripPNG(raw)
	require.NoError(t, err)
	assert.Equal(t, base, out)
}

func TestPNGAnimated(t *testing.T) {
	actl := pngChunkBytes("acTL", append(be32(2), be32(0)...))
	raw := injectPNG(testPNG(t), actl)
	p, err := ParsePNG(raw)
	require.NoError(t, err)
	assert.True(t, p.Animated)
	out, err := StripPNG(raw)
	require.NoError(t, err)
	assert.Same(t, &raw[0], &out[0])

	text := pngChunkBytes("tEXt", []byte("a\x00b"))
	fctl := pngChunkBytes("fcTL", make([]byte, 26))
	fdat := pngChunkBytes("fdAT", make([]byte, 8))
	out, err = StripPNG(injectPNG(testPNG(t), actl, text, fctl, fdat))
	require.NoError(t, err)
	q, err := ParsePNG(out)
	require.NoError(t, err)
	assert.True(t, q.Animated)
	assert.Contains(t, string(out), "fcTL")
	assert.Contains(t, string(out), "fdAT")
	assert.NotContains(t, string(out), "tEXt")
}

func TestPNGNeedsColorManagement(t *testing.T) {
	cases := []struct {
		name   string
		chunks [][]byte
		want   bool
	}{
		{"none", nil, false},
		{"gamma sRGB", [][]byte{pngChunkBytes("gAMA", be32(45455))}, false},
		{"gamma within 2%", [][]byte{pngChunkBytes("gAMA", be32(45000))}, false},
		{"gamma 1.0", [][]byte{pngChunkBytes("gAMA", be32(100000))}, true},
		{"gamma 2.0", [][]byte{pngChunkBytes("gAMA", be32(50000))}, true},
		{"gamma 1.8", [][]byte{pngChunkBytes("gAMA", be32(55556))}, true},
		{"sRGB chunk overrides gamma", [][]byte{pngChunkBytes("sRGB", []byte{0}), pngChunkBytes("gAMA", be32(100000))}, false},
		{"zero gamma ignored", [][]byte{pngChunkBytes("gAMA", be32(0))}, false},
		{"srgb profile", [][]byte{iccpChunk(srgbProfile())}, false},
		{"srgb profile beats gamma", [][]byte{iccpChunk(srgbProfile()), pngChunkBytes("gAMA", be32(100000))}, false},
		{"adobe profile", [][]byte{iccpChunk(adobeProfile())}, true},
		{"cicp", [][]byte{pngChunkBytes("cICP", []byte{1, 13, 0, 1})}, true},
		{"invalid profile falls back to gamma", [][]byte{iccpChunk(make([]byte, 200)), pngChunkBytes("gAMA", be32(100000))}, true},
		{"corrupt iccp ignored", [][]byte{pngChunkBytes("iCCP", []byte("x\x00\x00notzlib"))}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParsePNG(injectPNG(testPNG(t), tc.chunks...))
			require.NoError(t, err)
			assert.Equal(t, tc.want, p.NeedsColorManagement())
		})
	}
}

func TestPNGICCBomb(t *testing.T) {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	_, _ = zw.Write(make([]byte, 10<<20))
	_ = zw.Close()
	chunk := pngChunkBytes("iCCP", append([]byte("b\x00\x00"), z.Bytes()...))
	p, err := ParsePNG(injectPNG(testPNG(t), chunk))
	require.NoError(t, err)
	assert.Nil(t, p.ICC)
}

func TestParsePNGErrors(t *testing.T) {
	raw := injectPNG(testPNG(t), pngChunkBytes("gAMA", be32(45455)))
	for n := range len(raw) {
		_, err := ParsePNG(raw[:n])
		assert.Error(t, err, "prefix %d", n)
		_, err = StripPNG(raw[:n])
		assert.Error(t, err, "prefix %d", n)
	}

	_, err := ParsePNG([]byte("not a png at all, really not"))
	assert.Error(t, err)

	badCRC := append([]byte{}, raw...)
	badCRC[8+25+8] ^= 0xFF
	_, err = ParsePNG(badCRC)
	assert.ErrorIs(t, err, errPNGCRC)

	badIHDR := append([]byte{}, raw...)
	badIHDR[8+8+8] = 7
	_, err = ParsePNG(badIHDR)
	assert.Error(t, err)

	huge := append([]byte{}, raw...)
	copy(huge[8+25:], []byte{0xFF, 0xFF, 0xFF, 0xFF})
	_, err = ParsePNG(huge)
	assert.ErrorIs(t, err, errPNGChunk)
	_, err = StripPNG(huge)
	assert.ErrorIs(t, err, errPNGChunk)

	ihdrOnly := append(append([]byte{}, raw[:8+25]...), pngChunkBytes("IEND", nil)...)
	_, err = ParsePNG(ihdrOnly)
	assert.ErrorIs(t, err, errPNGNoDat)
}

func TestPNGRealFiles(t *testing.T) {
	files := readOptional(t, "/tmp/opencode/img/kodim0[1-3].png")
	for name, raw := range files {
		p, err := ParsePNG(raw)
		require.NoError(t, err, name)
		assert.Equal(t, 768+512, p.Width+p.Height, name)
		out, err := StripPNG(raw)
		require.NoError(t, err)
		assert.Len(t, out, len(raw)-p.AncillaryBytes)
		samePNGPixels(t, raw, out)
		t.Logf("%s: %d -> %d, color mgmt %v", name, len(raw), len(out), p.NeedsColorManagement())
	}
}

func TestPNGCorpus(t *testing.T) {
	n := 0
	for _, im := range harImages(t) {
		if im.mime != "image/png" && im.mime != "image/png; qs=0.7" {
			continue
		}
		n++
		p, err := ParsePNG(im.body)
		require.NoError(t, err)
		out, err := StripPNG(im.body)
		require.NoError(t, err)
		assert.Len(t, out, len(im.body)-p.AncillaryBytes)
		samePNGPixels(t, im.body, out)
		t.Logf("%dx%d depth=%d type=%d anc=%d icc=%d gamma=%.3f srgb=%v mgmt=%v", p.Width, p.Height, p.BitDepth, p.ColorType, p.AncillaryBytes, len(p.ICC), p.Gamma, p.HasSRGB, p.NeedsColorManagement())
	}
	assert.Positive(t, n)
}
