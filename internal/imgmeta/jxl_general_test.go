package imgmeta

import (
	"encoding/binary"
	"image"
	"image/color"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/jxlcodec"
)

func TestParseGeneralJXLCodestreamAndFragmentedContainer(t *testing.T) {
	m := image.NewNRGBA64(image.Rect(0, 0, 34, 18))
	for y := range 18 {
		for x := range 34 {
			m.SetNRGBA64(x, y, color.NRGBA64{R: uint16(x * 1700), G: uint16(y * 3000), B: 30000, A: uint16(12000 + x*1000)})
		}
	}
	raw, err := jxlcodec.Encode([]image.Image{m}, nil, 0, jxlcodec.Options{Lossless: true, Effort: 1, Orientation: 6})
	require.NoError(t, err)
	meta, err := ParseJXL(raw)
	require.NoError(t, err)
	assert.Equal(t, [2]int{18, 34}, [2]int{meta.Width, meta.Height})
	assert.Equal(t, [2]int{34, 18}, [2]int{meta.RawWidth, meta.RawHeight})
	assert.Equal(t, 6, meta.Orientation)
	assert.True(t, meta.Alpha)
	assert.Equal(t, 16, meta.BitsPerSample)
	assert.True(t, meta.OriginalSRGB)
	stream, err := jxlCodestream(raw)
	require.NoError(t, err)
	raw = append([]byte{0xff, 0x0a}, stream...)
	sig := []byte{0, 0, 0, 12, 'J', 'X', 'L', ' ', 13, 10, 135, 10}
	mid := len(raw) / 2
	container := append([]byte{}, sig...)
	container = append(container, box("ftyp", []byte("jxl "), u32(0), []byte("jxl "))...)
	container = append(container, box("jxlp", binary.BigEndian.AppendUint32(nil, 0), raw[:mid])...)
	container = append(container, box("jxlp", binary.BigEndian.AppendUint32(nil, 0x80000001), raw[mid:])...)
	meta, err = ParseJXL(container)
	require.NoError(t, err)
	assert.Equal(t, 16, meta.BitsPerSample)
	decoded, err := jxlcodec.Decode(container, true)
	require.NoError(t, err)
	assert.Equal(t, image.Rect(0, 0, 18, 34), decoded.Frames[0].Bounds())
	encodedICC, err := jxlcodec.Encode([]image.Image{m}, nil, 0, jxlcodec.Options{Lossless: true, Effort: 1, ICC: meta.ICC})
	require.NoError(t, err)
	meta, err = ParseJXL(encodedICC)
	require.NoError(t, err)
	assert.NotEmpty(t, meta.ICC)
}
