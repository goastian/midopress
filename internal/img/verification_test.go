package img

import (
	"context"
	"image"
	"image/color"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type incorrectLosslessBackend struct{ Backend }

func (incorrectLosslessBackend) Supports(f Format) bool { return f == WebP }
func (incorrectLosslessBackend) Encode(Format, image.Image, EncodeOptions) ([]byte, error) {
	return []byte{1}, nil
}
func (incorrectLosslessBackend) Decode(Format, []byte) (image.Image, error) {
	return image.NewNRGBA(image.Rect(0, 0, 16, 16)), nil
}

func TestClaimedLosslessEncoderMustRoundTripEveryChannel(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	// Hidden straight-alpha color is also part of the decoded pixel contract.
	src.SetNRGBA(15, 15, color.NRGBA{R: 123})
	e := NewEngine(incorrectLosslessBackend{})
	in := Input{Profile: profile(t, ProfileLossless), Caps: Caps{WebP: true}}
	assert.Nil(t, e.losslessModern(context.Background(), in, src))
}

type incorrectAnimationBackend struct {
	Backend
	source *Animation
	fault  string
}

func (*incorrectAnimationBackend) Supports(f Format) bool { return f == WebP }
func (*incorrectAnimationBackend) Native(Format) bool     { return true }
func (b *incorrectAnimationBackend) EncodeAnimation(_ Format, a *Animation, _ EncodeOptions) ([]byte, error) {
	b.source = a
	return []byte{1}, nil
}
func (b *incorrectAnimationBackend) DecodeAnimation(Format, []byte) (*Animation, error) {
	a := *b.source
	a.Frames = append([]image.Image(nil), a.Frames...)
	a.DelaysMS = append([]int(nil), a.DelaysMS...)
	switch b.fault {
	case "last frame":
		a.Frames[len(a.Frames)-1] = image.NewNRGBA(a.Frames[0].Bounds())
	case "timing":
		a.DelaysMS[len(a.DelaysMS)-1]++
	case "loop":
		a.LoopCount++
	case "missing frame":
		a.Frames = a.Frames[:len(a.Frames)-1]
	}
	return &a, nil
}

func TestDefaultAnimationVerificationRejectsLastFrameTimingAndLoopDamage(t *testing.T) {
	raw := animatedGIF(t, 3, 32, 32)
	for _, fault := range []string{"last frame", "timing", "loop", "missing frame"} {
		t.Run(fault, func(t *testing.T) {
			be := &incorrectAnimationBackend{fault: fault}
			_, err := NewEngine(be).Optimize(context.Background(), Input{
				Raw: raw, Format: GIF, Profile: profile(t, ProfileBalanced), Caps: Caps{WebP: true},
				VerifyQuality: false, // callers cannot switch mandatory verification off
			})
			require.ErrorIs(t, err, ErrBudget)
		})
	}
}

func TestPixelsPolicyIgnoresFixedLossyQualityForAnimation(t *testing.T) {
	raw := animatedGIF(t, 3, 32, 32)
	p := profile(t, ProfileExtreme)
	p.QualityPolicy = "pixels"
	be := &incorrectAnimationBackend{}
	result, err := NewEngine(be).Optimize(context.Background(), Input{
		Raw: raw, Format: GIF, Profile: p, Caps: Caps{WebP: true}, Fixed: 1,
	})
	require.NoError(t, err)
	assert.True(t, result.Lossless)
	assert.Zero(t, result.Quality)
}
