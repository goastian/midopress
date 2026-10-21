// Package quality measures how close a recoded image is to its source with a
// fast, allocation-light SSIM based metric.
//
// Both images are converted to 8-bit RGB (alpha is composited over mid-grey),
// then to BT.601 full-range YCbCr. The chroma planes are reduced 2x2. Each
// plane is scored with mean SSIM (Wang et al.: K1=0.01, K2=0.03, L=255,
// uniform 8x8 window, population variances, stride 4) and the planes are
// combined as 0.80*Y + 0.10*Cb + 0.10*Cr.
package quality

import (
	"errors"
	"fmt"
	"image"
	"math"
	"sync"
)

const (
	defaultMaxPixels = 2_000_000
	blockSize        = 4
	windowSize       = 2 * blockSize

	weightLuma   = 0.80
	weightChroma = 0.10
)

// Options tunes the metric.
type Options struct {
	// MaxPixels: when the image has more pixels, both images are compared
	// after being reduced by an integer factor (box filter) so that at most
	// MaxPixels remain. 0 means 2_000_000.
	MaxPixels int
}

// Detail holds the per-plane SSIM values and the combined score.
//
// Luma, Cb and Cr are raw mean SSIM values (they may be slightly negative for
// anti-correlated content); Score is their weighted mean clamped to [0,1].
type Detail struct{ Luma, Cb, Cr, Score float64 }

// Reference holds what is needed from the source image to compare many
// candidates against it. It is immutable after construction and safe for
// concurrent use.
type Reference struct {
	bounds image.Rectangle
	factor int
	planes [3]refPlane
}

type refPlane struct {
	w, h int
	pix  []uint8
	// sa and saa hold the sum of values and of squared values of each 4x4
	// block; they are only present when the plane is at least 8x8.
	sa, saa []int32
}

// scratch is the per-call working memory recycled through scratchPool.
type scratch struct {
	row    []uint8
	out    []uint8
	hsum   []uint16
	acc    []uint32
	planes [3][]uint8
	blocks []int32
}

var scratchPool = sync.Pool{New: func() any { return new(scratch) }}

func grow[T any](b []T, n int) []T {
	if cap(b) < n {
		return make([]T, n)
	}
	return b[:n]
}

// NewReference prepares img for repeated comparisons. It returns an error for
// a nil or empty image.
func NewReference(img image.Image, opts ...Options) (*Reference, error) {
	if img == nil {
		return nil, errors.New("quality: nil image")
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, errors.New("quality: empty image")
	}
	maxPixels := defaultMaxPixels
	if len(opts) > 0 && opts[0].MaxPixels > 0 {
		maxPixels = opts[0].MaxPixels
	}
	r := &Reference{bounds: b, factor: reductionFactor(w, h, maxPixels)}

	s := scratchPool.Get().(*scratch)
	defer scratchPool.Put(s)
	dims := s.extract(img, r.factor)
	for i := range r.planes {
		p := &r.planes[i]
		p.w, p.h = dims[i][0], dims[i][1]
		p.pix = append([]uint8(nil), s.planes[i][:p.w*p.h]...)
		if p.w >= windowSize && p.h >= windowSize {
			p.sa, p.saa = blockSums(p.pix, p.w, p.h)
		}
	}
	return r, nil
}

// reductionFactor returns the smallest integer factor f such that the image
// reduced by f has at most maxPixels pixels.
func reductionFactor(w, h, maxPixels int) int {
	if w*h <= maxPixels {
		return 1
	}
	f := max(int(math.Sqrt(float64(w)*float64(h)/float64(maxPixels))), 1)
	for f < max(w, h) && ceilDiv(w, f)*ceilDiv(h, f) > maxPixels {
		f++
	}
	return f
}

func ceilDiv(a, b int) int { return (a + b - 1) / b }

// Bounds returns the bounds of the source image.
func (r *Reference) Bounds() image.Rectangle { return r.bounds }

// Similarity compares candidate, which must have the same width and height as
// the reference, and returns a score in [0,1], 1 meaning identical.
//
// Alpha is handled by compositing both images over mid-grey (128) before the
// comparison: fully transparent pixels all become the same grey whatever
// their color channels hold, and partially transparent pixels contribute in
// proportion to their opacity. Images smaller than 8 pixels in a dimension
// are scored with a single window covering that whole dimension.
func (r *Reference) Similarity(candidate image.Image) (float64, error) {
	d, err := r.Detail(candidate)
	return d.Score, err
}

// Detail is like Similarity but also returns the per-plane values.
func (r *Reference) Detail(candidate image.Image) (Detail, error) {
	if candidate == nil {
		return Detail{}, errors.New("quality: nil candidate")
	}
	b := candidate.Bounds()
	if b.Dx() != r.bounds.Dx() || b.Dy() != r.bounds.Dy() {
		return Detail{}, fmt.Errorf("quality: candidate is %dx%d, reference is %dx%d",
			b.Dx(), b.Dy(), r.bounds.Dx(), r.bounds.Dy())
	}
	s := scratchPool.Get().(*scratch)
	defer scratchPool.Put(s)
	dims := s.extract(candidate, r.factor)

	var v [3]float64
	for i := range v {
		p := &r.planes[i]
		if dims[i][0] != p.w || dims[i][1] != p.h {
			return Detail{}, errors.New("quality: internal plane size mismatch")
		}
		v[i] = s.ssim(p, s.planes[i][:p.w*p.h])
	}
	score := weightLuma*v[0] + weightChroma*v[1] + weightChroma*v[2]
	score = min(max(score, 0), 1)
	return Detail{Luma: v[0], Cb: v[1], Cr: v[2], Score: score}, nil
}

// Similarity is a convenience for one-off comparisons.
func Similarity(ref, got image.Image) (float64, error) {
	r, err := NewReference(ref)
	if err != nil {
		return 0, err
	}
	return r.Similarity(got)
}
