package quality

import (
	"errors"
	"fmt"
	"image"
	"math"
	"sync"
)

// This file implements DSSIM, the multi-scale SSIM variant of Kornel
// Lesinski's dssim (dssim-core 3.x). Both images go through the same steps:
//
//  1. sRGB to linear light, premultiplied by alpha.
//  2. A pyramid of up to 5 scales, each a 2x2 box reduction of the previous
//     one in linear light (it stops early when a scale is smaller than 8
//     pixels in either dimension).
//  3. Each scale is converted to a Lab variant; the a and b planes are
//     blurred once.
//  4. Per scale, local means, variances and covariance are computed with a
//     Gaussian-like 5-tap blur. The SSIM map combines the three channels
//     (C1=0.01^2, C2=0.03^2) by averaging squared means, variances and
//     covariances before the SSIM formula.
//  5. The map is scored as 1 - mean|avg^(0.5^scale) - map|, which rewards
//     uniform quality; scales are combined with the weights below and the
//     final value is 1/ssim - 1.

var dsWeights = [5]float64{0.028, 0.197, 0.322, 0.298, 0.155}

const (
	dsC1   float32 = 0.01 * 0.01
	dsC2   float32 = 0.03 * 0.03
	dsInv3 float32 = 1.0 / 3.0
)

// DSSIMReference holds what is needed from the source image to measure many
// candidates: its Lab planes and, for every scale, the blurred means and
// variances. It is immutable after construction and safe for concurrent use.
// It keeps roughly 43 bytes per pixel of the (possibly reduced) image.
type DSSIMReference struct {
	bounds image.Rectangle
	factor int
	scales []dsScale
}

// dsScale is the precomputed state of one scale of the reference.
type dsScale struct {
	w, h int
	// img holds the (blurred for chroma) Lab planes, mu their local means.
	img, mu [3][]float32
	// mu1sq and sig1sq are the channel averaged squared mean and variance.
	mu1sq, sig1sq []float32
}

// The SSIM map only needs the variance and covariance averaged over the
// three channels. The blur is linear, so the channels are summed before
// blurring: sum_c blur(x_c^2) == blur(sum_c x_c^2). That takes five blurs per
// scale and image instead of nine.

// dsScratch is the per-call working memory recycled through dsPool.
type dsScratch struct {
	row  []uint8
	lin  [5]dsLinear
	lacc [4][]float32
	lab  [3][]float32
	tmp  []float32
	tmp2 []float32
	mapp []float32
	ring [5][]float32
	vrow [5][]float32
	xx   []float32
	xr   []float32
}

var dsPool = sync.Pool{New: func() any { return new(dsScratch) }}

// NewDSSIMReference prepares img for repeated DSSIM measurements. It returns
// an error for a nil or empty image.
//
// opts[0].MaxPixels, when the image has more pixels, makes both the reference
// and every candidate be reduced by the same integer factor (box filter in
// linear light) so that at most MaxPixels remain; 0 means 2_000_000. This is
// optimistic: the reduction averages away fine detail, so noise, blocking
// and ringing at the pixel scale are under-reported and the distance is
// usually lower than the one measured at full resolution.
func NewDSSIMReference(img image.Image, opts ...Options) (*DSSIMReference, error) {
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
	r := &DSSIMReference{bounds: b, factor: reductionFactor(w, h, maxPixels)}

	s := dsPool.Get().(*dsScratch)
	defer dsPool.Put(s)
	s.run(img, r.factor, func(_, w, h int, lab *[3][]float32) {
		r.scales = append(r.scales, s.prepare(w, h, lab))
	})
	return r, nil
}

// Bounds returns the bounds of the source image.
func (r *DSSIMReference) Bounds() image.Rectangle { return r.bounds }

// Distance measures candidate, which must have the same width and height as
// the reference, and returns its DSSIM distance: 0 means identical, about
// 0.001 is imperceptible and about 0.01 is noticeable. The value is not
// bounded above.
//
// Alpha is handled like the reference implementation: colors are linearized
// and premultiplied, and the missing coverage is added to a pseudo random
// subset of the color channels so that the transparency pattern itself is
// compared. Images smaller than 8 pixels in a dimension are measured at a
// single scale; the blur clamps at the borders for any size.
//
// Like the reference, each scale is scored by the deviation of its SSIM map
// from a power of the map average, so a 1x1 image (one scale, one map value)
// always scores 0, however different the two pixels are.
func (r *DSSIMReference) Distance(candidate image.Image) (float64, error) {
	if candidate == nil {
		return 0, errors.New("quality: nil candidate")
	}
	b := candidate.Bounds()
	if b.Dx() != r.bounds.Dx() || b.Dy() != r.bounds.Dy() {
		return 0, fmt.Errorf("quality: candidate is %dx%d, reference is %dx%d",
			b.Dx(), b.Dy(), r.bounds.Dx(), r.bounds.Dy())
	}
	s := dsPool.Get().(*dsScratch)
	defer dsPool.Put(s)

	var ssimSum, weightSum float64
	n := 0
	var mismatch bool
	s.run(candidate, r.factor, func(sc, w, h int, lab *[3][]float32) {
		if sc >= len(r.scales) || r.scales[sc].w != w || r.scales[sc].h != h {
			mismatch = true
			return
		}
		score := s.score(&r.scales[sc], sc, lab)
		ssimSum += score * dsWeights[sc]
		weightSum += dsWeights[sc]
		n++
	})
	if mismatch || n != len(r.scales) {
		return 0, errors.New("quality: internal scale mismatch")
	}
	return max(dsDistance(ssimSum/weightSum), 0), nil
}

// DSSIM is a convenience for one-off measurements.
func DSSIM(ref, got image.Image) (float64, error) {
	r, err := NewDSSIMReference(ref)
	if err != nil {
		return 0, err
	}
	return r.Distance(got)
}

func dsDistance(ssim float64) float64 {
	return 1/max(ssim, 2.220446049250313e-16) - 1
}

// run builds the pyramid of img and calls visit for every scale with its Lab
// planes (chroma already blurred). The planes are only valid during the call.
func (s *dsScratch) run(img image.Image, f int, visit func(scale, w, h int, lab *[3][]float32)) {
	s.load(img, f, &s.lin[0])
	for sc := range dsWeights {
		cur := &s.lin[sc]
		w, h := cur.w, cur.h
		for i := range s.lab {
			s.lab[i] = grow(s.lab[i], w*h)
		}
		var al []float32
		for y := range h {
			if cur.hasAlph {
				al = cur.p[3][y*w : (y+1)*w]
			}
			dsLabRow(s.lab[0][y*w:(y+1)*w], s.lab[1][y*w:(y+1)*w], s.lab[2][y*w:(y+1)*w],
				cur.p[0][y*w:(y+1)*w], cur.p[1][y*w:(y+1)*w], cur.p[2][y*w:(y+1)*w], al, y)
		}
		more := sc+1 < len(dsWeights) && dsDownsample(&s.lin[sc+1], cur)
		s.tmp = grow(s.tmp, 5*w)
		dsBlurPlane(s.lab[1], s.lab[1], s.tmp, w, h)
		dsBlurPlane(s.lab[2], s.lab[2], s.tmp, w, h)
		visit(sc, w, h, &s.lab)
		if !more {
			return
		}
	}
}

// prepare computes the reference side of one scale.
func (s *dsScratch) prepare(w, h int, lab *[3][]float32) dsScale {
	n := w * h
	sc := dsScale{w: w, h: h}
	s.tmp = grow(s.tmp, 5*w)
	s.tmp2 = grow(s.tmp2, n)
	sqSum := s.tmp2
	clear(sqSum)
	for c := range 3 {
		sc.img[c] = append([]float32(nil), lab[c][:n]...)
		sc.mu[c] = make([]float32, n)
		dsBlurPlane(sc.mu[c], sc.img[c], s.tmp, w, h)
	}
	for i := range n {
		a, b, c := sc.img[0][i], sc.img[1][i], sc.img[2][i]
		sqSum[i] = a*a + b*b + c*c
	}
	sq := make([]float32, n)
	dsBlurPlane(sq, sqSum, s.tmp, w, h)
	sc.mu1sq = make([]float32, n)
	sc.sig1sq = sq
	for i := range n {
		msum := sc.mu[0][i]*sc.mu[0][i] + sc.mu[1][i]*sc.mu[1][i] + sc.mu[2][i]*sc.mu[2][i]
		sc.mu1sq[i] = msum * dsInv3
		sq[i] = (sq[i] - msum) * dsInv3
	}
	return sc
}

// score returns the SSIM score of one scale of the candidate against ref.
func (s *dsScratch) score(ref *dsScale, sc int, lab *[3][]float32) float64 {
	w, h := ref.w, ref.h
	s.mapp = grow(s.mapp, w*h)
	for q := range s.ring {
		s.ring[q] = grow(s.ring[q], 5*w)
		s.vrow[q] = grow(s.vrow[q], w)
	}
	s.xx = grow(s.xx, w)
	s.xr = grow(s.xr, w)

	var sum float64
	next := 0
	for y := range h {
		for last := min(y+2, h-1); next <= last; next++ {
			s.hStep(ref, lab, next)
		}
		idx, wt := dsTaps(y, h)
		edge := y == 0 || y == h-1
		for q := range s.ring {
			ring := s.ring[q]
			r := func(k int) []float32 { return ring[(idx[k]%5)*w : (idx[k]%5+1)*w] }
			if edge {
				dsVRowEdge(s.vrow[q], r(0), r(1), r(2), r(3), r(4), wt)
			} else {
				dsVRow(s.vrow[q], r(0), r(1), r(2), r(3), r(4))
			}
		}
		o := y * w
		in := dsMapIn{mu1sq: ref.mu1sq[o : o+w], sig1sq: ref.sig1sq[o : o+w], sq2: s.vrow[3], xy: s.vrow[4]}
		for c := range 3 {
			in.mu1[c] = ref.mu[c][o : o+w]
			in.mu2[c] = s.vrow[c]
		}
		sum += dsMapRow(s.mapp[o:o+w], &in)
	}

	count := float64(w * h)
	avg := math.Pow(max(sum/count, 0), math.Pow(0.5, float64(sc)))
	var dev0, dev1, dev2, dev3 float64
	m := s.mapp[:w*h]
	for len(m) >= 4 {
		dev0 += math.Abs(avg - float64(m[0]))
		dev1 += math.Abs(avg - float64(m[1]))
		dev2 += math.Abs(avg - float64(m[2]))
		dev3 += math.Abs(avg - float64(m[3]))
		m = m[4:]
	}
	for _, v := range m {
		dev0 += math.Abs(avg - float64(v))
	}
	return 1 - (dev0+dev1+dev2+dev3)/count
}

// hStep computes the horizontally blurred rows of row y for the five
// quantities (mean of each channel, sum over channels of the squares, sum
// over channels of the product with the reference) into the ring buffers.
func (s *dsScratch) hStep(ref *dsScale, lab *[3][]float32, y int) {
	w := ref.w
	slot := (y % 5) * w
	x0, x1, x2 := lab[0][y*w:(y+1)*w], lab[1][y*w:(y+1)*w], lab[2][y*w:(y+1)*w]
	r0, r1, r2 := ref.img[0][y*w:(y+1)*w], ref.img[1][y*w:(y+1)*w], ref.img[2][y*w:(y+1)*w]
	xx, xr := s.xx[:w], s.xr[:w]
	x1, x2, r0, r1, r2 = x1[:w], x2[:w], r0[:w], r1[:w], r2[:w]
	for i, v := range x0[:w] {
		xx[i] = v*v + x1[i]*x1[i] + x2[i]*x2[i]
		xr[i] = v*r0[i] + x1[i]*r1[i] + x2[i]*r2[i]
	}
	dsHRow(s.ring[0][slot:slot+w], x0)
	dsHRow(s.ring[1][slot:slot+w], x1)
	dsHRow(s.ring[2][slot:slot+w], x2)
	dsHRow(s.ring[3][slot:slot+w], xx)
	dsHRow(s.ring[4][slot:slot+w], xr)
}

// dsMapIn gathers the inputs of one row of the SSIM map. The "1" side comes
// from the reference, the "2" side from the candidate; xy is the blurred
// sum over channels of the products of both. sq2 and xy are raw second
// moments: the means are subtracted in dsMapRow.
type dsMapIn struct {
	mu1, mu2      [3][]float32
	mu1sq, sig1sq []float32
	sq2, xy       []float32
}

// dsMapRow evaluates the SSIM map for one row and returns the sum of its
// values.
func dsMapRow(out []float32, in *dsMapIn) float64 {
	n := len(out)
	a0, a1, a2 := in.mu1[0][:n], in.mu1[1][:n], in.mu1[2][:n]
	b0, b1, b2 := in.mu2[0][:n], in.mu2[1][:n], in.mu2[2][:n]
	sq, xy := in.sq2[:n], in.xy[:n]
	m1sq, sg1 := in.mu1sq[:n], in.sig1sq[:n]
	var sum float64
	for i := range out {
		msum2 := b0[i]*b0[i] + b1[i]*b1[i] + b2[i]*b2[i]
		mu12s := a0[i]*b0[i] + a1[i]*b1[i] + a2[i]*b2[i]
		mu2sq := msum2 * dsInv3
		mu12 := mu12s * dsInv3
		sig2 := (sq[i] - msum2) * dsInv3
		sig12 := (xy[i] - mu12s) * dsInv3

		v := (2*mu12 + dsC1) * (2*sig12 + dsC2) / ((m1sq[i] + mu2sq + dsC1) * (sg1[i] + sig2 + dsC2))
		out[i] = v
		sum += float64(v)
	}
	return sum
}
