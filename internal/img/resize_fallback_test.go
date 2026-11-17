package img

import (
	"context"
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResizePreflightSkipsDamagingGeometryAndStillCompresses(t *testing.T) {
	raw := encodeJPEG(t, photoLike(480, 320, 7), 98)
	p := profile(t, ProfileSaver)
	p.Formats = []Format{WebP}
	e := NewEngine(NewWASM())
	e.Metric = MetricSSIM
	e.unreachable.counter(WebP, p, MetricSSIM).Store(unreachableAfter - 1)
	var pixels []int
	e.OnEncode = func(_ Format, n int, _ time.Duration) { pixels = append(pixels, n) }
	result, err := e.Optimize(context.Background(), Input{Raw: raw, Format: JPEG, Profile: p,
		Caps: Caps{WebP: true}, OutputFormat: WebP, Hints: Hints{ViewportWidth: 120}})
	require.NoError(t, err)
	require.NotEmpty(t, pixels)
	assert.Equal(t, 480*320, pixels[0], "geometrically invalid resizing must not spend any encoder attempt")
	assert.NotContains(t, pixels, 120*80)
	require.Equal(t, WebP, result.Format)
	assert.Less(t, len(result.Data), len(raw))
	assert.False(t, result.Resized)
	decoded, err := e.be.Decode(result.Format, result.Data)
	require.NoError(t, err)
	assert.Equal(t, image.Pt(480, 320), decoded.Bounds().Size())
	assert.GreaterOrEqual(t, result.Measure, p.TargetSSIM)
	t.Logf("geometry preflight, full-resolution encode: %d -> %d bytes (%.1f%% saved), SSIM %.5f >= %.5f, canvas %dx%d", len(raw), len(result.Data), 100*(1-float64(len(result.Data))/float64(len(raw))), result.Measure, p.TargetSSIM, decoded.Bounds().Dx(), decoded.Bounds().Dy())
	assert.Zero(t, e.unreachable.counter(WebP, p, MetricSSIM).Load(), "a resize failure must not poison the full-resolution format")
	fullAttempts := 0
	for _, n := range pixels {
		if n == 480*320 {
			fullAttempts++
		}
	}
	assert.LessOrEqual(t, fullAttempts, p.Attempts, "only one full-resolution search is allowed")
}

type damagingResizeBackend struct {
	Backend
	last   image.Image
	sizes  []image.Point
	delay  time.Duration
	cancel context.CancelFunc
}

func (*damagingResizeBackend) Supports(f Format) bool { return f == WebP }
func (*damagingResizeBackend) Native(Format) bool     { return true }
func (b *damagingResizeBackend) Encode(_ Format, pic image.Image, _ EncodeOptions) ([]byte, error) {
	b.last = pic
	b.sizes = append(b.sizes, pic.Bounds().Size())
	if b.cancel != nil {
		b.cancel()
	}
	if b.delay > 0 {
		time.Sleep(b.delay)
	}
	return []byte{1}, nil
}
func (b *damagingResizeBackend) Decode(Format, []byte) (image.Image, error) {
	if b.last.Bounds().Dx() == 480 {
		return b.last, nil
	}
	damaged := image.NewRGBA(b.last.Bounds())
	for y := 0; y < damaged.Rect.Dy(); y++ {
		for x := 0; x < damaged.Rect.Dx(); x++ {
			damaged.SetRGBA(x, y, color.RGBA{R: 255, B: 255, A: 255})
		}
	}
	return damaged, nil
}

func resizeFriendlyJPEG(t *testing.T) []byte {
	source := image.NewRGBA(image.Rect(0, 0, 480, 320))
	for y := 0; y < 320; y++ {
		for x := 0; x < 480; x++ {
			source.SetRGBA(x, y, color.RGBA{R: uint8(x * 255 / 479), G: uint8(y * 255 / 319), B: 128, A: 255})
		}
	}
	return encodeJPEG(t, source, 98)
}

func TestResizeStillFallsBackWhenOnlyTheEncoderDamagesItsCandidate(t *testing.T) {
	raw := resizeFriendlyJPEG(t)
	p := profile(t, ProfileSaver)
	p.Formats = []Format{WebP}
	be := &damagingResizeBackend{}
	e := NewEngine(be)
	e.Metric = MetricSSIM
	e.unreachable.counter(WebP, p, MetricSSIM).Store(unreachableAfter - 1)
	result, err := e.Optimize(context.Background(), Input{Raw: raw, Format: JPEG, Profile: p,
		Caps: Caps{WebP: true}, OutputFormat: WebP, Hints: Hints{ViewportWidth: 120}, Fixed: 100})
	require.NoError(t, err)
	assert.Equal(t, []image.Point{image.Pt(120, 80), image.Pt(480, 320)}, be.sizes)
	assert.Equal(t, WebP, result.Format)
	assert.False(t, result.Resized)
	assert.GreaterOrEqual(t, result.Measure, p.TargetSSIM)
	assert.Zero(t, e.unreachable.counter(WebP, p, MetricSSIM).Load())
}

func TestFailedResizeDoesNotRetryAfterBudgetIsSpent(t *testing.T) {
	raw := resizeFriendlyJPEG(t)
	p := profile(t, ProfileSaver)
	p.Formats = []Format{WebP}
	be := &damagingResizeBackend{delay: 200 * time.Millisecond}
	e := NewEngine(be)
	e.Metric = MetricSSIM
	result, err := e.Optimize(context.Background(), Input{Raw: raw, Format: JPEG, Profile: p,
		Caps: Caps{WebP: true}, OutputFormat: WebP, Hints: Hints{ViewportWidth: 120}, Fixed: 100, Budget: 100 * time.Millisecond})
	require.NoError(t, err)
	assert.Equal(t, JPEG, result.Format)
	assert.True(t, result.Lossless)
	assert.True(t, result.Limited)
	assert.Equal(t, []image.Point{image.Pt(120, 80)}, be.sizes, "no source-resolution retry may outlive the budget")
}

func TestFailedResizeDoesNotRetryAfterCancellation(t *testing.T) {
	raw := resizeFriendlyJPEG(t)
	p := profile(t, ProfileSaver)
	p.Formats = []Format{WebP}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	be := &damagingResizeBackend{cancel: cancel}
	e := NewEngine(be)
	e.Metric = MetricSSIM
	_, err := e.Optimize(ctx, Input{Raw: raw, Format: JPEG, Profile: p, Caps: Caps{WebP: true}, OutputFormat: WebP,
		Hints: Hints{ViewportWidth: 120}, Fixed: 100})
	require.NoError(t, err)
	assert.Equal(t, []image.Point{image.Pt(120, 80)}, be.sizes)
}
