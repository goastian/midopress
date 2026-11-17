package img

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExplicitEffectsPreserveAlphaAndIdentifyLoss(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 192, 128))
	for y := 0; y < 128; y++ {
		for x := 0; x < 192; x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: 200, G: 40, B: 90, A: 128})
		}
	}
	var raw bytes.Buffer
	require.NoError(t, png.Encode(&raw, src))
	p, _ := Lookup(ProfileBalanced)
	p.Grayscale = true
	e := NewEngine(NewWASM())
	r, err := e.Optimize(context.Background(), Input{Raw: raw.Bytes(), Format: PNG, Profile: p, VerifyQuality: true})
	require.NoError(t, err)
	require.True(t, r.Intentional)
	assert.False(t, r.Lossless)
	assert.Empty(t, r.Metric, "a metric against altered pixels is not a fidelity score against the original")
	decoded, err := png.Decode(bytes.NewReader(r.Data))
	require.NoError(t, err)
	c := color.NRGBAModel.Convert(decoded.At(0, 0)).(color.NRGBA)
	assert.Equal(t, c.R, c.G)
	assert.Equal(t, c.R, c.B)
	assert.Equal(t, uint8(128), c.A)
	assert.Greater(t, c.R, uint8(70), "semi-transparent pixels must not be darkened twice")
	p.Grayscale, p.PlaceholderWidth = false, 24
	r, err = e.Optimize(context.Background(), Input{Raw: raw.Bytes(), Format: PNG, Profile: p})
	require.NoError(t, err)
	decoded, err = png.Decode(bytes.NewReader(r.Data))
	require.NoError(t, err)
	assert.Equal(t, image.Pt(24, 16), decoded.Bounds().Size())
	assert.True(t, r.Resized)
	p.Lossless = true
	_, err = e.Optimize(context.Background(), Input{Raw: raw.Bytes(), Format: PNG, Profile: p})
	assert.ErrorIs(t, err, ErrUnsupported)
}

func TestVerifiedFixedQualityCannotBypassTarget(t *testing.T) {
	raw := encodeJPEG(t, photoLike(200, 150, 8), 95)
	p := profile(t, ProfileBalanced)
	r, err := testEngine.Optimize(context.Background(), Input{Raw: raw, Format: JPEG, Profile: p, Fixed: 1, VerifyQuality: true})
	require.NoError(t, err)
	assert.True(t, r.Lossless, "a fixed quality below the target must preserve the original")
}

func TestFullResolutionVerificationDetectsPixelDamage(t *testing.T) {
	a := image.NewGray(image.Rect(0, 0, 2048, 1024))
	b := image.NewGray(a.Bounds())
	for y := 0; y < 1024; y++ {
		for x := 0; x < 2048; x++ {
			v := uint8((x % 2) * 255)
			a.SetGray(x, y, color.Gray{Y: v})
			b.SetGray(x, y, color.Gray{Y: 255 - v})
		}
	}
	p := profile(t, ProfileBalanced)
	sampled, err := newScorer(MetricSSIM, a, p)
	require.NoError(t, err)
	full, err := newScorer(MetricSSIM, a, p, true)
	require.NoError(t, err)
	smallScore, err := sampled.eval(b)
	require.NoError(t, err)
	fullScore, err := full.eval(b)
	require.NoError(t, err)
	assert.Greater(t, smallScore, .99)
	assert.Less(t, fullScore, .5)
}

func TestVerifiedAnimationKeepsEveryFrame(t *testing.T) {
	raw := animatedGIF(t, 3, 64, 64)
	p := profile(t, ProfileExtreme)
	r, err := NewEngine(NewWASM()).Optimize(context.Background(), Input{
		Raw: raw, Format: GIF, Profile: p, Caps: Caps{WebP: true}, VerifyQuality: true,
	})
	require.NoError(t, err)
	assert.Equal(t, WebP, r.Format)
	assert.Equal(t, 3, riffFrames(r.Data))
	assert.True(t, r.Lossless)
}

func TestRequestedFormatDoesNotSelectAnUnrequestedFallback(t *testing.T) {
	e := NewEngine(NewWASM())
	p := profile(t, ProfileBalanced)
	in := Input{Format: JPEG, Caps: Caps{WebP: true, AVIF: true}, Profile: p, OutputFormat: WebP}
	assert.Equal(t, []Format{WebP}, e.formatsFor(in, false))
	in.OutputFormat = PNG
	assert.Empty(t, e.formatsFor(in, false))
	in.Format, in.OutputFormat = WebP, JPEG
	assert.Equal(t, []Format{JPEG}, e.formatsFor(in, false))
}
