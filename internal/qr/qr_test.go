package qr

import (
	"bytes"
	"image"
	"image/png"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/makiuchi-d/gozxing"
	zqr "github.com/makiuchi-d/gozxing/qrcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decode(t *testing.T, img image.Image) string {
	t.Helper()
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	require.NoError(t, err)
	res, err := zqr.NewQRCodeReader().Decode(bmp, nil)
	require.NoError(t, err)
	return res.GetText()
}

func samples() map[string]string {
	return map[string]string{
		"short":   "http://midopress.it/ca.crt",
		"20":      strings.Repeat("a", 20),
		"120":     "http://midopress.it/" + strings.Repeat("x", 100),
		"500":     "http://midopress.it/?" + strings.Repeat("q", 479),
		"unicode": "midopress: caf\u00e9 \u65e5\u672c\u8a9e \u00fc\u00f1",
	}
}

func TestPNGRoundTrip(t *testing.T) {
	for name, s := range samples() {
		t.Run(name, func(t *testing.T) {
			data, err := PNG(s, 512)
			require.NoError(t, err)
			img, err := png.Decode(bytes.NewReader(data))
			require.NoError(t, err)
			assert.Equal(t, s, decode(t, img))
		})
	}
}

var pathRe = regexp.MustCompile(`M(\d+) (\d+)h(\d+)v1h-\d+z`)

func rasterise(t *testing.T, svg string) image.Image {
	t.Helper()
	vb := regexp.MustCompile(`viewBox="0 0 (\d+) (\d+)"`).FindStringSubmatch(svg)
	require.NotNil(t, vb)
	total, _ := strconv.Atoi(vb[1])
	const scale = 8
	img := image.NewGray(image.Rect(0, 0, total*scale, total*scale))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	for _, m := range pathRe.FindAllStringSubmatch(svg, -1) {
		x, _ := strconv.Atoi(m[1])
		y, _ := strconv.Atoi(m[2])
		w, _ := strconv.Atoi(m[3])
		for py := y * scale; py < (y+1)*scale; py++ {
			for px := x * scale; px < (x+w)*scale; px++ {
				img.Pix[py*img.Stride+px] = 0
			}
		}
	}
	return img
}

func TestSVGRoundTrip(t *testing.T) {
	for name, s := range samples() {
		t.Run(name, func(t *testing.T) {
			svg, err := SVG(s)
			require.NoError(t, err)
			assert.Contains(t, svg, `shape-rendering="crispEdges"`)
			assert.Contains(t, svg, `fill="currentColor"`)
			assert.Equal(t, s, decode(t, rasterise(t, svg)))
		})
	}
}

func TestSVGMatchesMatrixAndQuietZone(t *testing.T) {
	s := "http://midopress.it/ca.crt"
	m, err := Matrix(s, LevelM)
	require.NoError(t, err)
	svg, err := SVG(s)
	require.NoError(t, err)
	assert.Contains(t, svg, `viewBox="0 0 `+strconv.Itoa(len(m)+8)+" "+strconv.Itoa(len(m)+8)+`"`)
	dark := 0
	for _, r := range m {
		for _, v := range r {
			if v {
				dark++
			}
		}
	}
	area := 0
	for _, p := range pathRe.FindAllStringSubmatch(svg, -1) {
		x, _ := strconv.Atoi(p[1])
		y, _ := strconv.Atoi(p[2])
		assert.GreaterOrEqual(t, x, 4)
		assert.GreaterOrEqual(t, y, 4)
		w, _ := strconv.Atoi(p[3])
		area += w
	}
	assert.Equal(t, dark, area)
}

func TestSVGOptions(t *testing.T) {
	svg, err := SVG("hello", WithColors("#0f3d2e", "#ffffff"), WithLevel(LevelH))
	require.NoError(t, err)
	assert.Contains(t, svg, `fill="#0f3d2e"`)
	assert.Contains(t, svg, `<rect`)
	m1, _ := Matrix("hello", LevelL)
	m2, _ := Matrix("hello", LevelH)
	assert.LessOrEqual(t, len(m1), len(m2))
}

func TestErrors(t *testing.T) {
	_, err := SVG(strings.Repeat("a", 5000))
	require.Error(t, err)
	_, err = PNG(strings.Repeat("a", 5000), 256)
	require.Error(t, err)
	_, err = SVG("")
	require.Error(t, err)
}
