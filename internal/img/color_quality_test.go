package img

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	stdjpeg "image/jpeg"
	"image/png"
	"os"
	"testing"

	"github.com/goastian/midopress/internal/colorcms"
	"github.com/goastian/midopress/internal/imgmeta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pngColorChunk(kind string, data []byte) []byte {
	b := make([]byte, len(data)+12)
	binary.BigEndian.PutUint32(b, uint32(len(data)))
	copy(b[4:], kind)
	copy(b[8:], data)
	binary.BigEndian.PutUint32(b[len(b)-4:], crc32.ChecksumIEEE(b[4:len(b)-4]))
	return b
}

func TestICCAndOrientationAreConvertedBeforeFullResolutionQuality(t *testing.T) {
	icc, err := os.ReadFile("../colorcms/testdata/display-p3.icc")
	require.NoError(t, err)
	src := photoLike(192, 128, 19)
	var encoded bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.NoCompression}
	require.NoError(t, enc.Encode(&encoded, src))
	var zipped bytes.Buffer
	zw := zlib.NewWriter(&zipped)
	_, err = zw.Write(icc)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	chunks := pngColorChunk("iCCP", append([]byte("test\x00\x00"), zipped.Bytes()...))
	chunks = append(chunks, pngColorChunk("eXIf", exifOrientation(6))...)
	raw := append([]byte(nil), encoded.Bytes()[:33]...)
	raw = append(raw, chunks...)
	raw = append(raw, encoded.Bytes()[33:]...)
	p := profile(t, ProfileExtreme)
	p.ColorPolicy = "srgb"
	p.Formats = []Format{WebP}
	p.ResizeToViewport = false
	e := NewEngine(NewWASM())
	e.Metric = MetricSSIM
	r, err := e.Optimize(context.Background(), Input{Raw: raw, Format: PNG, Caps: Caps{WebP: true}, Profile: p})
	require.NoError(t, err)
	require.Equal(t, WebP, r.Format, "a supported wide-gamut image is actually recoded")
	decoded, err := e.be.Decode(r.Format, r.Data)
	require.NoError(t, err)
	assert.Equal(t, image.Pt(128, 192), decoded.Bounds().Size())
	ref, err := colorcms.ToSRGB(context.Background(), src, icc)
	require.NoError(t, err)
	sc, err := newScorer(MetricSSIM, Orient(ref, 6), p)
	require.NoError(t, err)
	score, err := sc.eval(decoded)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, score, sc.target)
}

func TestPixelsPolicyDisablesLossyFixedAndAppearanceChanges(t *testing.T) {
	raw := encodeJPEG(t, photoLike(160, 120, 3), 95)
	p := profile(t, ProfileExtreme)
	p.QualityPolicy = "pixels"
	r, err := NewEngine(NewWASM()).Optimize(context.Background(), Input{Raw: raw, Format: JPEG, Caps: Caps{WebP: true}, Profile: p, Fixed: 1})
	require.NoError(t, err)
	assert.True(t, r.Lossless)
	a, err := NewWASM().Decode(JPEG, raw)
	require.NoError(t, err)
	b, err := NewWASM().Decode(r.Format, r.Data)
	require.NoError(t, err)
	assert.True(t, samePixels(a, b))
	p.Grayscale = true
	_, err = NewEngine(NewWASM()).Optimize(context.Background(), Input{Raw: raw, Format: JPEG, Profile: p})
	assert.ErrorIs(t, err, ErrUnsupported)
}

func TestCMYKJPEGWithLUTProfileIsActuallyConverted(t *testing.T) {
	raw, err := os.ReadFile("testdata/cmyk-lut.jpg")
	require.NoError(t, err)
	meta, err := imgmeta.ParseJPEG(raw)
	require.NoError(t, err)
	require.Equal(t, 4, meta.Components)
	ref, err := stdjpeg.Decode(bytes.NewReader(raw))
	require.NoError(t, err)
	ref, err = colorcms.ToSRGB(context.Background(), ref, meta.ICC)
	require.NoError(t, err)
	p := profile(t, ProfileExtreme)
	p.ColorPolicy = "srgb"
	p.Formats, p.ResizeToViewport = []Format{WebP}, false
	e := NewEngine(NewWASM())
	e.Metric = MetricSSIM
	got, err := e.Optimize(context.Background(), Input{Raw: raw, Format: JPEG, Caps: Caps{WebP: true}, Profile: p})
	require.NoError(t, err)
	require.Equal(t, WebP, got.Format, "CMYK LUT no longer forces an original-only fallback")
	decoded, err := e.be.Decode(got.Format, got.Data)
	require.NoError(t, err)
	scorer, err := newScorer(MetricSSIM, ref, p)
	require.NoError(t, err)
	score, err := scorer.eval(decoded)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, score, scorer.target)
}

func TestWideGamutSaturatedColorsRequireExplicitSRGBMapping(t *testing.T) {
	icc, err := os.ReadFile("../colorcms/testdata/display-p3.icc")
	require.NoError(t, err)
	src := image.NewNRGBA(image.Rect(0, 0, 128, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 128; x++ {
			c := color.NRGBA{R: 255, A: 255}
			if x >= 64 {
				c = color.NRGBA{G: 255, A: 255}
			}
			src.SetNRGBA(x, y, c)
		}
	}
	var buf, zipped bytes.Buffer
	require.NoError(t, (&png.Encoder{CompressionLevel: png.NoCompression}).Encode(&buf, src))
	zw := zlib.NewWriter(&zipped)
	_, err = zw.Write(icc)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	raw := append([]byte(nil), buf.Bytes()[:33]...)
	raw = append(raw, pngColorChunk("iCCP", append([]byte("P3\x00\x00"), zipped.Bytes()...))...)
	raw = append(raw, buf.Bytes()[33:]...)
	e := NewEngine(NewWASM())
	e.Metric = MetricSSIM
	p := profile(t, ProfileBalanced)
	input := Input{Raw: raw, Format: PNG, Profile: p, Caps: Caps{WebP: true}}
	result, err := e.Optimize(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, raw, result.Data, "preserve keeps saturated P3 pixels and the P3 profile; no gamut clipping")
	assert.True(t, result.Lossless)
	assert.False(t, result.ColorConverted)
	assert.False(t, result.Intentional)
	p.ColorPolicy = "srgb"
	result, err = e.Optimize(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, WebP, result.Format)
	assert.True(t, result.ColorConverted)
	assert.True(t, result.Intentional)
	assert.False(t, result.Lossless)
	assert.Empty(t, result.Metric, "a score against clipped sRGB is not source-wide-gamut fidelity")
	assert.Zero(t, result.Measure)
	meta, err := imgmeta.ParseWebP(result.Data)
	require.NoError(t, err)
	assert.Empty(t, meta.ICC, "the explicit result is sRGB, not an incorrectly retained P3 label")
}

func TestOrientedPNGOriginalFallbackKeepsOrientation(t *testing.T) {
	var raw bytes.Buffer
	require.NoError(t, png.Encode(&raw, photoLike(24, 16, 3)))
	oriented := append([]byte(nil), raw.Bytes()[:33]...)
	oriented = append(oriented, pngColorChunk("eXIf", exifOrientation(6))...)
	oriented = append(oriented, raw.Bytes()[33:]...)
	p := profile(t, ProfileBalanced)
	p.QualityPolicy = "pixels"
	r, err := NewEngine(NewWASM()).Optimize(context.Background(), Input{Raw: oriented, Format: PNG, Profile: p})
	require.NoError(t, err)
	meta, err := imgmeta.ParsePNG(r.Data)
	require.NoError(t, err)
	assert.Equal(t, 6, meta.Orientation)
	assert.True(t, r.Lossless)
}

func TestIncompleteJPEGProfileIsPreservedAndNeverGuessedAsSRGB(t *testing.T) {
	base := encodeJPEG(t, photoLike(32, 24, 3), 90)
	raw := withSegment(base, 0xE2, append([]byte("ICC_PROFILE\x00\x01\x02"), []byte("one of two missing fragments")...))
	meta, err := imgmeta.ParseJPEG(raw)
	require.NoError(t, err)
	require.True(t, meta.HasICC)
	require.Empty(t, meta.ICC)
	require.True(t, NeedsColorConversion(raw, JPEG))
	p := profile(t, ProfileBalanced)
	in := Input{Raw: raw, Format: JPEG, Profile: p, Caps: Caps{WebP: true}}
	e := NewEngine(NewWASM())
	result, err := e.Optimize(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, raw, result.Data)
	p.ColorPolicy = "srgb"
	_, err = e.Optimize(context.Background(), in)
	assert.ErrorIs(t, err, ErrUnsupported)
	_, err = DecodeDisplay(context.Background(), e.be, JPEG, raw)
	assert.ErrorIs(t, err, ErrUnsupported)
}

func TestAVIFPresentationAppliesCropRotationAndMirror(t *testing.T) {
	src := photoLike(6, 4, 4)
	transforms := []imgmeta.AVIFTransform{
		{Kind: "clap", Aperture: [8]int64{4, 1, 2, 1, 0, 1, 0, 1}},
		{Kind: "irot", Value: 1}, {Kind: "imir", Value: 1},
	}
	got, err := avifPresentation(src, transforms)
	require.NoError(t, err)
	assert.Equal(t, image.Pt(2, 4), got.Bounds().Size())
	want := image.NewNRGBA64(image.Rect(0, 0, 4, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			want.Set(x, y, src.At(x+1, y+1))
		}
	}
	assert.True(t, samePixels(Orient(Orient(want, 8), 2), got))
	_, err = avifPresentation(src, []imgmeta.AVIFTransform{{Kind: "clap", Aperture: [8]int64{99, 1, 2, 1, 0, 1, 0, 1}}})
	assert.Error(t, err)
}
