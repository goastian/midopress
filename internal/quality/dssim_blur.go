package quality

// The DSSIM blur is two successive clamped 3-tap passes of [s c s] per axis,
// fused into one 5-tap pass. The border coefficients reproduce the double
// clamping exactly, so the result is the same for every image size.
const (
	dsKSide   float32 = 0.308_758_86
	dsKCenter float32 = 0.382_482_8

	dsOuter = dsKSide * dsKSide
	dsInner = 2 * dsKSide * dsKCenter
	dsMid   = 2*dsKSide*dsKSide + dsKCenter*dsKCenter

	dsEdgeCenter = dsMid + dsInner
	dsEdgeNear   = dsOuter + dsInner
	dsEdgeFar    = dsOuter
)

// dsTaps returns, for output index i of an axis of n samples, the five source
// indexes and weights (in the order -2, -1, 0, +1, +2) of the blur. Indexes
// are always in [0,n).
func dsTaps(i, n int) (idx [5]int, wt [5]float32) {
	last := n - 1
	switch i {
	case 0:
		return [5]int{0, 0, 0, min(1, last), min(2, last)},
			[5]float32{0, 0, dsEdgeCenter, dsEdgeNear, dsEdgeFar}
	case last:
		return [5]int{max(last-2, 0), max(last-1, 0), last, last, last},
			[5]float32{dsEdgeFar, dsEdgeNear, dsEdgeCenter, 0, 0}
	}
	for k := range idx {
		idx[k] = min(max(i-2+k, 0), last)
	}
	return idx, [5]float32{dsOuter, dsInner, dsMid, dsInner, dsOuter}
}

func dsTapSum(src []float32, i int) float32 {
	idx, wt := dsTaps(i, len(src))
	return src[idx[0]]*wt[0] + src[idx[1]]*wt[1] + src[idx[2]]*wt[2] + src[idx[3]]*wt[3] + src[idx[4]]*wt[4]
}

// The kernels copy the weights into locals so that they stay in registers.
var dsOuterV, dsInnerV, dsMidV float32 = dsOuter, dsInner, dsMid

// dsHRow blurs one row horizontally. dst must have at least len(src) elements.
func dsHRow(dst, src []float32) {
	n := len(src)
	dst = dst[:n]
	if n < 5 {
		for j := range n {
			dst[j] = dsTapSum(src, j)
		}
		return
	}
	o, in, mid := dsOuterV, dsInnerV, dsMidV
	d := dst[2 : n-2]
	m := len(d)
	s0, s1, s2, s3, s4 := src[:m], src[1:1+m], src[2:2+m], src[3:3+m], src[4:4+m]
	for j := range d {
		d[j] = (s0[j]+s4[j])*o + (s1[j]+s3[j])*in + s2[j]*mid
	}
	dst[0], dst[1] = dsTapSum(src, 0), dsTapSum(src, 1)
	dst[n-2], dst[n-1] = dsTapSum(src, n-2), dsTapSum(src, n-1)
}

// dsVRow combines five rows with the plain interior weights.
func dsVRow(dst, a, b, c, d, e []float32) {
	n := len(dst)
	o, in, mid := dsOuterV, dsInnerV, dsMidV
	a, b, c, d, e = a[:n], b[:n], c[:n], d[:n], e[:n]
	for i := range dst {
		dst[i] = (a[i]+e[i])*o + (b[i]+d[i])*in + c[i]*mid
	}
}

// dsVRowEdge combines five rows with arbitrary weights (border rows).
func dsVRowEdge(dst, a, b, c, d, e []float32, w [5]float32) {
	n := len(dst)
	a, b, c, d, e = a[:n], b[:n], c[:n], d[:n], e[:n]
	for i := range dst {
		dst[i] = a[i]*w[0] + b[i]*w[1] + c[i]*w[2] + d[i]*w[3] + e[i]*w[4]
	}
}

// dsBlurPlane blurs the w*h plane src into dst, which may be src itself.
// ring must hold 5*w elements; it keeps the horizontally blurred rows needed
// by the vertical pass so that the working set stays small.
func dsBlurPlane(dst, src, ring []float32, w, h int) {
	row := func(k int) []float32 { return ring[(k%5)*w : (k%5+1)*w] }
	next := 0
	for y := range h {
		for last := min(y+2, h-1); next <= last; next++ {
			dsHRow(row(next), src[next*w:(next+1)*w])
		}
		idx, wt := dsTaps(y, h)
		out := dst[y*w : (y+1)*w]
		if y == 0 || y == h-1 {
			dsVRowEdge(out, row(idx[0]), row(idx[1]), row(idx[2]), row(idx[3]), row(idx[4]), wt)
		} else {
			dsVRow(out, row(idx[0]), row(idx[1]), row(idx[2]), row(idx[3]), row(idx[4]))
		}
	}
}
