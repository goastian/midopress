package quality

import (
	"errors"
	"fmt"
	"image"
	"math"
	"sync"
)

const (
	s2NumScales = 6
	s2MinSize   = 8
)

// ErrSSIMULACRA2TooSmall is returned when an image is smaller than 8x8 pixels
// (after the MaxPixels reduction), the minimum size of the metric.
var ErrSSIMULACRA2TooSmall = errors.New("quality: ssimulacra2 needs images of at least 8x8 pixels")

// s2Lin8 maps an 8-bit sRGB value to linear light with the same transfer
// function (including its polynomial pow approximation) as the reference.
var s2Lin8 = func() (t [256]float32) {
	const (
		alpha float32 = 1.0550107
		beta  float32 = 0.0030412825
	)
	for i := range t {
		x := float32(i) / 255
		if x < 12.92*beta {
			t[i] = x / 12.92
		} else {
			t[i] = s2Powf((x+(alpha-1))/alpha, 2.4)
		}
	}
	return t
}()

// s2RefScale holds what is needed from the reference at one scale: its XYB
// planes, their blur and the local variance (blurred square minus squared
// blur).
type s2RefScale struct {
	img, mu, variance [3][]float32
}

// SSIMULACRA2Reference holds what is needed from a source image to score many
// candidates with SSIMULACRA2 (Cloudinary's perceptual metric: multi-scale
// blurred SSIM and edge difference maps computed in XYB space). It is
// immutable after construction and safe for concurrent use.
//
// The score is 100 for identical images and decreases with distortion, about
// 90 being visually lossless and 70 high quality; it can become negative for
// heavily damaged images.
type SSIMULACRA2Reference struct {
	bounds image.Rectangle
	factor int
	w, h   int
	scales []s2RefScale
}

type s2Scratch struct {
	row    []uint8
	acc    []float32
	pad    []float32
	lin    [3][]float32
	img    [3][]float32
	tmp    []float32
	s22    []float32
	s12    []float32
	mu2    []float32
	zeros  [s2Cols]float32
	scores [s2NumScales]s2ScaleScore
}

type s2ScaleScore struct {
	ssim [6]float64
	edge [12]float64
}

var s2Pool = sync.Pool{New: func() any { return new(s2Scratch) }}

// prepare sizes the scratch buffers for a w x h image.
func (s *s2Scratch) prepare(w, h int) {
	n := w * h
	for c := range 3 {
		s.lin[c] = grow(s.lin[c], n)
		s.img[c] = grow(s.img[c], n)
	}
	s.tmp = grow(s.tmp, n)
	s.s22 = grow(s.s22, n)
	s.s12 = grow(s.s12, n)
	s.mu2 = grow(s.mu2, n)
	s.pad = grow(s.pad, s2HLanes*(w+3*s2Radius))
}

// blur blurs a (or a*b when b is not nil) into dst for a w x h plane.
func (s *s2Scratch) blur(dst, a, b []float32, w, h int) {
	n := w * h
	s2HBlur(s.tmp[:n], a[:n], bOrNil(b, n), w, h, s.pad)
	s2VBlur(dst[:n], s.tmp[:n], w, h, &s.zeros)
}

func bOrNil(b []float32, n int) []float32 {
	if b == nil {
		return nil
	}
	return b[:n]
}

// readLinear fills s.lin with the linear light planes of img, box averaged by
// factor f, and returns the reduced size.
func (s *s2Scratch) readLinear(img image.Image, f int) (int, int) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	rw, rh := ceilDiv(w, f), ceilDiv(h, f)
	for c := range 3 {
		s.lin[c] = grow(s.lin[c], rw*rh)
	}
	src := newSource(img)
	s.row = grow(s.row, w*4)
	if f == 1 {
		for y := range h {
			row := src.row(y, s.row)
			o := y * w
			lr, lg, lb := s.lin[0][o:o+w], s.lin[1][o:o+w], s.lin[2][o:o+w]
			for x := range w {
				px := row[4*x : 4*x+3 : 4*x+3]
				lr[x], lg[x], lb[x] = s2Lin8[px[0]], s2Lin8[px[1]], s2Lin8[px[2]]
			}
		}
		return w, h
	}
	s.acc = grow(s.acc, rw*3)
	for oy := range rh {
		clear(s.acc)
		y0, y1 := oy*f, min(oy*f+f, h)
		for y := y0; y < y1; y++ {
			row := src.row(y, s.row)
			for ox := range rw {
				x0, x1 := ox*f, min(ox*f+f, w)
				var sr, sg, sb float32
				for x := x0; x < x1; x++ {
					sr += s2Lin8[row[4*x]]
					sg += s2Lin8[row[4*x+1]]
					sb += s2Lin8[row[4*x+2]]
				}
				a := s.acc[3*ox : 3*ox+3 : 3*ox+3]
				a[0] += sr
				a[1] += sg
				a[2] += sb
			}
		}
		for ox := range rw {
			inv := 1 / float32((y1-y0)*(min(ox*f+f, w)-ox*f))
			for c := range 3 {
				s.lin[c][oy*rw+ox] = s.acc[3*ox+c] * inv
			}
		}
	}
	return rw, rh
}

func (s *s2Scratch) toXYB(n int) {
	s2ToXYB(s.lin[0][:n], s.lin[1][:n], s.lin[2][:n], s.img[0][:n], s.img[1][:n], s.img[2][:n])
}

// NewSSIMULACRA2Reference prepares img for repeated comparisons. When img has
// more than opts.MaxPixels pixels (default 2_000_000) it and every candidate
// are reduced by the same integer box factor (averaging in linear light)
// before being compared. This makes the score optimistic, since fine
// distortion is averaged away, but it bounds the time and memory used.
//
// It returns an error for a nil or empty image and ErrSSIMULACRA2TooSmall
// when the (reduced) image is smaller than 8x8 pixels.
func NewSSIMULACRA2Reference(img image.Image, opts ...Options) (*SSIMULACRA2Reference, error) {
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
	f := reductionFactor(w, h, maxPixels)
	rw, rh := ceilDiv(w, f), ceilDiv(h, f)
	if rw < s2MinSize || rh < s2MinSize {
		return nil, ErrSSIMULACRA2TooSmall
	}
	r := &SSIMULACRA2Reference{bounds: b, factor: f, w: rw, h: rh}

	s := s2Pool.Get().(*s2Scratch)
	defer s2Pool.Put(s)
	s.prepare(rw, rh)
	s.readLinear(img, f)
	cw, ch := rw, rh
	for scale := range s2NumScales {
		if cw < s2MinSize || ch < s2MinSize {
			break
		}
		if scale > 0 {
			for c := range 3 {
				s2Downscale(s.lin[c][:cw*ch], cw, ch)
			}
			cw, ch = (cw+1)/2, (ch+1)/2
		}
		n := cw * ch
		s.toXYB(n)
		var rs s2RefScale
		for c := range 3 {
			rs.img[c] = append([]float32(nil), s.img[c][:n]...)
			rs.mu[c] = make([]float32, n)
			rs.variance[c] = make([]float32, n)
			s.blur(rs.mu[c], rs.img[c], nil, cw, ch)
			s.blur(rs.variance[c], rs.img[c], rs.img[c], cw, ch)
			for i, m := range rs.mu[c] {
				rs.variance[c][i] -= float32(m * m)
			}
		}
		r.scales = append(r.scales, rs)
	}
	return r, nil
}

// Bounds returns the bounds of the source image.
func (r *SSIMULACRA2Reference) Bounds() image.Rectangle { return r.bounds }

// Score compares candidate, which must have the same width and height as the
// reference, and returns its SSIMULACRA2 score: 100 for identical images,
// lower for worse ones, possibly negative.
//
// Alpha is handled by compositing both images over mid-grey before the
// comparison, like Similarity. Scale k is computed on the image halved k
// times, so the number of scales is limited by the image size; the weights are
// applied to the scales that exist, as the reference implementation does.
func (r *SSIMULACRA2Reference) Score(candidate image.Image) (float64, error) {
	if candidate == nil {
		return 0, errors.New("quality: nil candidate")
	}
	b := candidate.Bounds()
	if b.Dx() != r.bounds.Dx() || b.Dy() != r.bounds.Dy() {
		return 0, fmt.Errorf("quality: candidate is %dx%d, reference is %dx%d",
			b.Dx(), b.Dy(), r.bounds.Dx(), r.bounds.Dy())
	}
	s := s2Pool.Get().(*s2Scratch)
	defer s2Pool.Put(s)
	s.prepare(r.w, r.h)
	w, h := s.readLinear(candidate, r.factor)
	if w != r.w || h != r.h {
		return 0, errors.New("quality: internal size mismatch")
	}
	for i := range r.scales {
		rs := &r.scales[i]
		if i > 0 {
			for c := range 3 {
				s2Downscale(s.lin[c][:w*h], w, h)
			}
			w, h = (w+1)/2, (h+1)/2
		}
		n := w * h
		s.toXYB(n)
		inv := 1 / float64(n)
		sc := &s.scores[i]
		for c := range 3 {
			img2 := s.img[c][:n]
			s.blur(s.s22, img2, img2, w, h)
			s.blur(s.s12, rs.img[c], img2, w, h)
			s.blur(s.mu2, img2, nil, w, h)
			s1, s4 := s2SSIMMap(rs.mu[c], rs.variance[c], s.mu2[:n], s.s22[:n], s.s12[:n])
			sc.ssim[2*c] = inv * s1
			sc.ssim[2*c+1] = math.Sqrt(math.Sqrt(inv * s4))
			a1, a4, l1, l4 := s2EdgeMap(rs.img[c], rs.mu[c], img2, s.mu2[:n])
			sc.edge[4*c] = inv * a1
			sc.edge[4*c+1] = math.Sqrt(math.Sqrt(inv * a4))
			sc.edge[4*c+2] = inv * l1
			sc.edge[4*c+3] = math.Sqrt(math.Sqrt(inv * l4))
		}
	}
	return s2Combine(s.scores[:len(r.scales)]), nil
}

func s2Combine(scales []s2ScaleScore) float64 {
	var ssim float64
	i := 0
	for c := range 3 {
		for k := range scales {
			sc := &scales[k]
			for n := range 2 {
				ssim += s2Weights[i] * math.Abs(sc.ssim[c*2+n])
				i++
				ssim += s2Weights[i] * math.Abs(sc.edge[c*4+n])
				i++
				ssim += s2Weights[i] * math.Abs(sc.edge[c*4+n+2])
				i++
			}
		}
	}
	ssim *= 0.9562382616834844
	ssim = 6.248496625763138e-5*ssim*ssim*ssim + (2.326765642916932*ssim - 0.020884521182843837*ssim*ssim)
	if ssim > 0 {
		return 100 - 10*math.Pow(ssim, 0.6276336467831387)
	}
	return 100
}

// SSIMULACRA2 is a convenience for one-off comparisons.
func SSIMULACRA2(ref, got image.Image) (float64, error) {
	r, err := NewSSIMULACRA2Reference(ref)
	if err != nil {
		return 0, err
	}
	return r.Score(got)
}
