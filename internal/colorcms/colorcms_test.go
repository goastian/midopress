package colorcms

import (
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedCMSMatchesNativeCorpus(t *testing.T) {
	for _, name := range []string{"adobe-rgb", "display-p3", "prophoto", "gray18", "rgb-lut", "cmyk-lut"} {
		t.Run(name, func(t *testing.T) {
			icc, err := os.ReadFile("testdata/" + name + ".icc")
			require.NoError(t, err)
			in, err := os.ReadFile("testdata/" + name + ".input16")
			require.NoError(t, err)
			want, err := os.ReadFile("testdata/" + name + ".srgb16")
			require.NoError(t, err)
			n := 8
			channels := len(in) / n / 2
			var src image.Image
			rgb := image.NewNRGBA64(image.Rect(0, 0, n, 1))
			gray := image.NewGray16(rgb.Bounds())
			cmyk := image.NewCMYK(rgb.Bounds())
			for i := 0; i < n; i++ {
				v := func(c int) uint16 { return binary.LittleEndian.Uint16(in[(i*channels+c)*2:]) }
				switch channels {
				case 1:
					gray.SetGray16(i, 0, color.Gray16{Y: v(0)})
					src = gray
				case 3:
					rgb.SetNRGBA64(i, 0, color.NRGBA64{R: v(0), G: v(1), B: v(2), A: uint16(i * 8000)})
					src = rgb
				case 4:
					cmyk.SetCMYK(i, 0, color.CMYK{C: uint8(v(0) / 257), M: uint8(v(1) / 257), Y: uint8(v(2) / 257), K: uint8(v(3) / 257)})
					src = cmyk
				}
			}
			got, err := ToSRGB(context.Background(), src, icc)
			require.NoError(t, err)
			for i := 0; i < n; i++ {
				c := color.NRGBA64Model.Convert(got.At(i, 0)).(color.NRGBA64)
				for j, actual := range []uint16{c.R, c.G, c.B} {
					expected := binary.BigEndian.Uint16(want[i*6+j*2:])
					assert.InDelta(t, expected, actual, 8, "pixel %d channel %d", i, j)
				}
				_, _, _, alpha := src.At(i, 0).RGBA()
				assert.Equal(t, uint16(alpha), c.A)
			}
		})
	}
}

func TestEmbeddedCMSRejectsMalformedProfilesAndCancellation(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	_, err := ToSRGB(context.Background(), src, []byte("bad"))
	require.Error(t, err)
	icc, err := os.ReadFile("testdata/display-p3.icc")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = ToSRGB(ctx, src, icc)
	require.Error(t, err)
	bad := append([]byte(nil), icc...)
	for i := 132; i < len(bad); i++ {
		bad[i] = 255
	}
	_, err = ToSRGB(context.Background(), src, bad)
	require.Error(t, err)
}

func TestVirtualRGBProfilesAndTransferCurves(t *testing.T) {
	chroma := [8]float64{.3127, .3290, .64, .33, .30, .60, .15, .06}
	src := image.NewNRGBA64(image.Rect(0, 0, 1, 1))
	src.SetNRGBA64(0, 0, color.NRGBA64{R: 32768, G: 32768, B: 32768, A: 12345})
	for _, transfer := range []int{0, 1, 6, 8, 13} {
		profile, err := RGBProfile(context.Background(), chroma, transfer, 1.8)
		require.NoError(t, err)
		got, err := ToSRGB(context.Background(), src, profile)
		require.NoError(t, err)
		c := color.NRGBA64Model.Convert(got.At(0, 0)).(color.NRGBA64)
		assert.Equal(t, uint16(12345), c.A)
		if transfer == 13 {
			assert.InDelta(t, 32768, c.R, 10, "sRGB matrix and transfer is identity")
		}
		if transfer == 8 {
			assert.InDelta(t, 48192, c.R, 20, "linear 0.5 is encoded with sRGB transfer")
		}
	}
	_, err := RGBProfile(context.Background(), chroma, 16, 0)
	assert.Error(t, err, "HDR needs an explicit tone-mapping policy")
	chroma[2] = 2
	_, err = RGBProfile(context.Background(), chroma, 13, 0)
	assert.Error(t, err)
}
