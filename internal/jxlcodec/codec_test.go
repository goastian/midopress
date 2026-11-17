package jxlcodec

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func picture() *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, 40, 24))
	for y := range 24 {
		for x := range 40 {
			m.SetNRGBA(x, y, color.NRGBA{uint8(x * 6), uint8(y * 10), uint8((x + y) * 3), uint8(80 + (x+y)%170)})
		}
	}
	return m
}

func TestPortableJPEGReconstructionIsBitExact(t *testing.T) {
	var src bytes.Buffer
	require.NoError(t, jpeg.Encode(&src, picture(), &jpeg.Options{Quality: 88}))
	encoded, err := Recompress(src.Bytes())
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(encoded, []byte{0, 0, 0, 12, 'J', 'X', 'L', ' '}))
	back, err := Reconstruct(encoded)
	require.NoError(t, err)
	assert.Equal(t, src.Bytes(), back)
	_, err = Reconstruct([]byte{0xff, 0x0a, 0})
	assert.Error(t, err)
}

func TestPortableJXLAlphaOrientationAndFrames(t *testing.T) {
	src := picture()
	encoded, err := Encode([]image.Image{src}, nil, 0, Options{Lossless: true, Effort: 1, Orientation: 6})
	require.NoError(t, err)
	info, err := Inspect(encoded)
	require.NoError(t, err)
	assert.Equal(t, 6, info.Orientation)
	assert.Equal(t, 8, info.AlphaBits)
	assert.Equal(t, 24, info.Width)
	assert.Equal(t, 40, info.Height)
	a, err := Decode(encoded, true)
	require.NoError(t, err)
	require.Len(t, a.Frames, 1)
	assert.Equal(t, image.Rect(0, 0, 24, 40), a.Frames[0].Bounds())
	for y := range 24 {
		for x := range 40 {
			v := src.NRGBAAt(x, y)
			want := color.NRGBA64{R: uint16(v.R) * 257, G: uint16(v.G) * 257, B: uint16(v.B) * 257, A: uint16(v.A) * 257}
			got := color.NRGBA64Model.Convert(a.Frames[0].At(23-y, x)).(color.NRGBA64)
			require.Equal(t, want, got)
		}
	}
	second := picture()
	second.SetNRGBA(0, 0, color.NRGBA{20, 200, 70, 220})
	animation, err := Encode([]image.Image{src, second}, []int{40, 125}, 3, Options{Lossless: true, Effort: 1})
	require.NoError(t, err)
	info, err = Inspect(animation)
	require.NoError(t, err)
	assert.True(t, info.Animated)
	assert.Equal(t, 2, info.Frames)
	assert.Equal(t, 3, info.LoopCount)
	a, err = Decode(animation, true)
	require.NoError(t, err)
	require.Len(t, a.Frames, 2)
	assert.Equal(t, []int{40, 125}, a.DelaysMS)
	assert.Equal(t, 3, a.LoopCount)
	v := second.NRGBAAt(0, 0)
	assert.Equal(t, color.NRGBA64{R: uint16(v.R) * 257, G: uint16(v.G) * 257, B: uint16(v.B) * 257, A: uint16(v.A) * 257}, a.Frames[1].At(0, 0))
}

func TestPortableJXLWideGamutAndHDRNormalizeToSRGB(t *testing.T) {
	for name, o := range map[string]Options{
		"p3":  {Quality: 90, Effort: 1, Primaries: 11},
		"pq":  {Quality: 90, Effort: 1, Primaries: 9, TransferFunction: 16},
		"hlg": {Quality: 90, Effort: 1, Primaries: 9, TransferFunction: 18},
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := Encode([]image.Image{picture()}, nil, 0, o)
			require.NoError(t, err)
			info, err := Inspect(encoded)
			require.NoError(t, err)
			assert.NotEmpty(t, info.ICC)
			assert.Equal(t, o.Primaries, info.Primaries)
			got, err := Decode(encoded, false)
			require.NoError(t, err)
			assert.True(t, got.Metadata.OutputSRGB)
			assert.Equal(t, picture().Bounds(), got.Frames[0].Bounds())
		})
	}
}

func TestPortableCodecRejectsEmptyAndMalformedInput(t *testing.T) {
	for _, src := range [][]byte{nil, []byte("not JPEG XL"), {0xff, 0x0a}} {
		_, err := Inspect(src)
		assert.Error(t, err)
		_, err = Decode(src, true)
		assert.Error(t, err)
	}
}
