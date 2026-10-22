package quality

import "math"

const (
	dsD65x float32 = 0.9505
	dsD65y float32 = 1.0
	dsD65z float32 = 1.089

	dsLabEpsilon float32 = 216.0 / 24389.0
	dsLabK       float32 = 24389.0 / (27.0 * 116.0)

	dsXr = 0.4124 / dsD65x
	dsXg = 0.3576 / dsD65x
	dsXb = 0.1805 / dsD65x
	dsYr = 0.2126 / dsD65y
	dsYg = 0.7152 / dsD65y
	dsYb = 0.0722 / dsD65y
	dsZr = 0.0193 / dsD65z
	dsZg = 0.1192 / dsD65z
	dsZb = 0.9505 / dsD65z
)

// The cube root is approximated by a quadratic per bucket of the exponent
// and the top 7 mantissa bits of x, for x in [2^-7, 2). The relative error is
// below 1e-8, an order of magnitude better than the quadratic plus two
// Halley iterations of the reference (5e-7), without any division. Linear
// light values never exceed 1 here.
const (
	dsCbrtMantBits = 7
	dsCbrtExpMin   = 120 // biased exponent of 2^-7
	dsCbrtBuckets  = 8 << dsCbrtMantBits
)

var dsCbrtPoly = makeCbrtPoly()

// makeCbrtPoly interpolates the cube root at the Chebyshev nodes of each
// bucket, which is close to the minimax quadratic.
func makeCbrtPoly() (t [dsCbrtBuckets][4]float32) {
	nodes := [3]float64{-math.Sqrt(3) / 2, 0, math.Sqrt(3) / 2}
	for i := range t {
		e := i>>dsCbrtMantBits + dsCbrtExpMin - 127
		m := i & (1<<dsCbrtMantBits - 1)
		x0 := math.Ldexp(1+float64(m)/(1<<dsCbrtMantBits), e)
		x1 := math.Ldexp(1+float64(m+1)/(1<<dsCbrtMantBits), e)
		var xs, ys [3]float64
		for k, n := range nodes {
			xs[k] = (x0+x1)/2 + n*(x1-x0)/2
			ys[k] = math.Cbrt(xs[k])
		}
		// Newton form through the three nodes, expanded to powers of x.
		d01 := (ys[1] - ys[0]) / (xs[1] - xs[0])
		d12 := (ys[2] - ys[1]) / (xs[2] - xs[1])
		d012 := (d12 - d01) / (xs[2] - xs[0])
		c2 := d012
		c1 := d01 - d012*(xs[0]+xs[1])
		c0 := ys[0] - d01*xs[0] + d012*xs[0]*xs[1]
		t[i] = [4]float32{float32(c0), float32(c1), float32(c2)}
	}
	return t
}

// dsCbrt returns the cube root of x for 2^-7 <= x < 2.
func dsCbrt(x float32) float32 {
	idx := int(math.Float32bits(x)>>(23-dsCbrtMantBits)) - dsCbrtExpMin<<dsCbrtMantBits
	c := &dsCbrtPoly[idx&(dsCbrtBuckets-1)]
	return c[0] + x*(c[1]+x*c[2])
}

func dsF(v float32) float32 {
	if v > dsLabEpsilon {
		return dsCbrt(v) - 16.0/116.0
	}
	return dsLabK * v
}

// dsLabRow converts one row of linear RGB to the shifted and scaled Lab used
// by DSSIM. When al is not nil the pixels are premultiplied by alpha and the
// missing coverage (1-alpha) is added to a pseudo random subset of the
// channels, like the reference does, so that transparent regions get a noisy
// background instead of a flat color. y is the row index within the scale.
func dsLabRow(l, a, b, r, g, bl, al []float32, y int) {
	n := len(l)
	a, b, r, g, bl = a[:n], b[:n], r[:n], g[:n], bl[:n]
	if al == nil {
		for x := range l {
			l[x], a[x], b[x] = dsLab(r[x], g[x], bl[x])
		}
		return
	}
	al = al[:n]
	for x := range l {
		rr, gg, bb := r[x], g[x], bl[x]
		m := 1 - al[x]
		k := (x + 11) ^ (y + 11)
		if k&16 != 0 {
			rr += m
		}
		if k&8 != 0 {
			gg += m
		}
		if k&32 != 0 {
			bb += m
		}
		l[x], a[x], b[x] = dsLab(rr, gg, bb)
	}
}

func dsLab(r, g, b float32) (l, a, bb float32) {
	fx := r*dsXr + g*dsXg + b*dsXb
	fy := r*dsYr + g*dsYg + b*dsYb
	fz := r*dsZr + g*dsZg + b*dsZb
	px, py, pz := dsF(fx), dsF(fy), dsF(fz)
	return py * 1.05, (500.0/220.0)*(px-py) + 86.2/220.0, (200.0/220.0)*(py-pz) + 107.9/220.0
}
