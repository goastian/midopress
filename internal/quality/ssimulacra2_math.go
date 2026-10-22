package quality

import (
	"math"
)

const (
	s2Radius = 5
	s2Cols   = 128

	s2C2 = 0.0009
)

// s2Kernel holds the coefficients of the three second order recurrences of
// the truncated cosine recursive Gaussian (Charalampidis 2016, sigma 1.5).
type s2Kernel struct {
	in, prev [3]float32
}

var s2K = s2NewKernel()

func s2NewKernel() s2Kernel {
	const sigma = 1.5
	radius := math.Round(3.2795*sigma + 0.2546)
	if int(radius) != s2Radius {
		panic("quality: unexpected SSIMULACRA2 blur radius")
	}
	piDiv2R := math.Pi / (2 * radius)
	omega := [3]float64{piDiv2R, 3 * piDiv2R, 5 * piDiv2R}
	p1 := 1 / math.Tan(0.5*omega[0])
	p3 := -1 / math.Tan(0.5*omega[1])
	p5 := 1 / math.Tan(0.5*omega[2])
	r1 := p1 * p1 / math.Sin(omega[0])
	r3 := -p3 * p3 / math.Sin(omega[1])
	r5 := p5 * p5 / math.Sin(omega[2])

	var rho [3]float64
	for i := range rho {
		rho[i] = math.Exp(-0.5*sigma*sigma*omega[i]*omega[i]) / radius
	}
	d13 := p1*r3 - r1*p3
	d35 := p3*r5 - r3*p5
	d51 := p5*r1 - r5*p1
	zeta15 := d35 / d13
	zeta35 := d51 / d13

	a := [3][3]float64{{p1, p3, p5}, {r1, r3, r5}, {zeta15, zeta35, 1}}
	gamma := [3]float64{1, radius*radius - sigma*sigma, zeta15*rho[0] + zeta35*rho[1] + rho[2]}
	beta := s2Solve3(a, gamma)

	var k s2Kernel
	for i := range 3 {
		n2 := -beta[i] * math.Cos(omega[i]*(radius+1))
		d1 := -2 * math.Cos(omega[i])
		k.in[i] = float32(n2)
		k.prev[i] = float32(d1)
	}
	return k
}

func s2Det3(m [3][3]float64) float64 {
	return m[0][0]*(m[1][1]*m[2][2]-m[1][2]*m[2][1]) -
		m[0][1]*(m[1][0]*m[2][2]-m[1][2]*m[2][0]) +
		m[0][2]*(m[1][0]*m[2][1]-m[1][1]*m[2][0])
}

// s2Solve3 solves a*x = b with Cramer's rule.
func s2Solve3(a [3][3]float64, b [3]float64) [3]float64 {
	det := s2Det3(a)
	var x [3]float64
	for c := range 3 {
		m := a
		for r := range 3 {
			m[r][c] = b[r]
		}
		x[c] = s2Det3(m) / det
	}
	return x
}

// s2HLanes is the number of rows the horizontal pass interleaves: the
// recurrence of a single row is a long dependency chain, independent rows
// keep the FMA units busy.
const s2HLanes = 4

type s2HState struct{ p1, p3, p5, q1, q3, q5 float32 }

// s2HBlur blurs every row of a (or of a*b when b is not nil) into dst. pad
// must hold s2HLanes*(w+3*s2Radius) floats; its zero margins implement the
// zero boundary condition of the reference.
func s2HBlur(dst, a, b []float32, w, h int, pad []float32) {
	const n = s2Radius
	stride := w + 3*n
	var pads [s2HLanes][]float32
	for l := range pads {
		pads[l] = pad[l*stride : (l+1)*stride]
		clear(pads[l][:2*n])
		clear(pads[l][2*n+w:])
	}
	in1, in3, in5 := s2K.in[0], s2K.in[1], s2K.in[2]
	d1, d3, d5 := -s2K.prev[0], -s2K.prev[1], -s2K.prev[2]
	for y0 := 0; y0 < h; y0 += s2HLanes {
		for l := range pads {
			y := min(y0+l, h-1)
			mid := pads[l][2*n : 2*n+w]
			ra := a[y*w : (y+1)*w]
			if b == nil {
				copy(mid, ra)
			} else {
				rb := b[y*w : (y+1)*w]
				for i, v := range ra {
					mid[i] = v * rb[i]
				}
			}
		}
		var st [s2HLanes]s2HState
		for i := -n + 1; i < w; i++ {
			for l := range st {
				s := &st[l]
				sum := pads[l][i+n-1] + pads[l][i+3*n-1]
				o1 := float32(sum*in1) - s.q1
				o3 := float32(sum*in3) - s.q3
				o5 := float32(sum*in5) - s.q5
				o1 = fma32(d1, s.p1, o1)
				o3 = fma32(d3, s.p3, o3)
				o5 = fma32(d5, s.p5, o5)
				s.q1, s.q3, s.q5 = s.p1, s.p3, s.p5
				s.p1, s.p3, s.p5 = o1, o3, o5
				if i >= 0 && y0+l < h {
					dst[(y0+l)*w+i] = o1 + o3 + o5
				}
			}
		}
	}
}

// s2VBlur applies the vertical pass of the blur from src to dst, handling
// s2Cols columns at a time to stay in cache.
func s2VBlur(dst, src []float32, w, h int, zeros *[s2Cols]float32) {
	const n = s2Radius
	in1, in3, in5 := s2K.in[0], s2K.in[1], s2K.in[2]
	d1, d3, d5 := s2K.prev[0], s2K.prev[1], s2K.prev[2]
	var p1, p3, p5, q1, q3, q5 [s2Cols]float32
	for x0 := 0; x0 < w; x0 += s2Cols {
		cols := min(s2Cols, w-x0)
		clear(p1[:])
		clear(p3[:])
		clear(p5[:])
		clear(q1[:])
		clear(q3[:])
		clear(q5[:])
		for y := -n + 1; y < h; y++ {
			top, bot := zeros[:cols], zeros[:cols]
			if t := y - n - 1; t >= 0 {
				top = src[t*w+x0 : t*w+x0+cols]
			}
			if b := y + n - 1; b < h {
				bot = src[b*w+x0 : b*w+x0+cols]
			}
			var out []float32
			if y >= 0 {
				out = dst[y*w+x0 : y*w+x0+cols]
			}
			bot = bot[:len(top)]
			for i := range top {
				sum := top[i] + bot[i]
				o1 := fma32(sum, in1, -fma32(p1[i], d1, q1[i]))
				o3 := fma32(sum, in3, -fma32(p3[i], d3, q3[i]))
				o5 := fma32(sum, in5, -fma32(p5[i], d5, q5[i]))
				q1[i], q3[i], q5[i] = p1[i], p3[i], p5[i]
				p1[i], p3[i], p5[i] = o1, o3, o5
				if out != nil {
					out[i] = o1 + o3 + o5
				}
			}
		}
	}
}

// Colour constants of the opsin absorbance matrix, rounded to float32 the
// same way the reference does.
var (
	s2M00, s2M01, s2M02         = s2OpsinRow(0.30, 0.078)
	s2M10, s2M11, s2M12         = s2OpsinRow(0.23, 0.078)
	s2M20, s2M21, s2M22         = s2OpsinRow2(0.24342269, 0.20476745)
	s2Bias              float32 = 0.0037930734
	s2NegCbrtBias               = -s2Cbrt(s2Bias)
)

func s2OpsinRow(a, c float32) (float32, float32, float32) { return a, 1 - c - a, c }

func s2OpsinRow2(a, b float32) (float32, float32, float32) { return a, b, 1 - a - b }

// s2Cbrt is the cbrtf of the reference (FreeBSD's algorithm with two Newton
// steps in double precision) for positive normal inputs.
func s2Cbrt(x float32) float32 {
	const b1 = 709958130
	ui := math.Float32bits(x)
	hx := (ui&0x7fffffff)/3 + b1
	ui = ui&0x80000000 | hx
	xd := float64(x)
	t := float64(math.Float32frombits(ui))
	r := t * t * t
	t = t * (xd + xd + r) / (xd + r + r)
	r = t * t * t
	t = t * (xd + xd + r) / (xd + r + r)
	return float32(t)
}

func s2Poly5(x, c0, c1, c2, c3, c4, c5 float32) float32 {
	return c0 + x*(c1+x*(c2+x*(c3+x*(c4+x*c5))))
}

func s2Exp2(x float32) float32 {
	x = min(max(x, -126.99999), 129)
	ipart := int32(x - 0.5)
	fpart := x - float32(ipart)
	expi := math.Float32frombits(uint32((ipart + 127) << 23))
	return expi * s2Poly5(fpart, 9.9999994e-1, 6.931531e-1, 2.4015361e-1, 5.5826318e-2, 8.98934e-3, 1.8775767e-3)
}

func s2Log2(x float32) float32 {
	bits := math.Float32bits(x)
	exp := float32(int32(bits&0x7f800000>>23) - 127)
	mant := math.Float32frombits(bits&0x007fffff | 0x3f800000)
	poly := s2Poly5(mant, 3.11579, -3.324199, 2.5988452, -1.2315303, 3.1821337e-1, -3.4436006e-2)
	return poly*(mant-1) + exp
}

// s2Powf is the polynomial approximation of x^y the reference uses (its
// "fastmath" build, the default).
func s2Powf(x, y float32) float32 { return s2Exp2(s2Log2(x) * y) }

// s2ToXYB converts linear RGB planes to the "positive" XYB planes of
// SSIMULACRA2 (X*14+0.42, Y+0.01, B-Y+0.55).
func s2ToXYB(r, g, b, ox, oy, ob []float32) {
	n := len(r)
	g, b, ox, oy, ob = g[:n], b[:n], ox[:n], oy[:n], ob[:n]
	for i, rv := range r {
		gv, bv := g[i], b[i]
		m0 := fma32(s2M00, rv, fma32(s2M01, gv, fma32(s2M02, bv, s2Bias)))
		m1 := fma32(s2M10, rv, fma32(s2M11, gv, fma32(s2M12, bv, s2Bias)))
		m2 := fma32(s2M20, rv, fma32(s2M21, gv, fma32(s2M22, bv, s2Bias)))
		if m0 < 0 {
			m0 = 0
		}
		if m1 < 0 {
			m1 = 0
		}
		if m2 < 0 {
			m2 = 0
		}
		m0 = s2Cbrt(m0) + s2NegCbrtBias
		m1 = s2Cbrt(m1) + s2NegCbrtBias
		m2 = s2Cbrt(m2) + s2NegCbrtBias
		x := 0.5 * (m0 - m1)
		y := 0.5 * (m0 + m1)
		ox[i] = fma32(x, 14, 0.42)
		oy[i] = y + 0.01
		ob[i] = (m2 - y) + 0.55
	}
}

// s2Downscale halves p (w x h) in place by 2x2 averaging with edge
// replication and returns the new size.
func s2Downscale(p []float32, w, h int) (int, int) {
	ow, oh := (w+1)/2, (h+1)/2
	for oy := range oh {
		r0 := p[2*oy*w : 2*oy*w+w]
		r1 := r0
		if 2*oy+1 < h {
			r1 = p[(2*oy+1)*w : (2*oy+1)*w+w]
		}
		for ox := range ow {
			x0 := 2 * ox
			x1 := min(x0+1, w-1)
			sum := r0[x0] + r0[x1]
			sum += r1[x0]
			sum += r1[x1]
			p[oy*ow+ox] = sum * 0.25
		}
	}
	return ow, oh
}

// s2SSIMMap returns the sum of the SSIM' error and of its fourth power over
// one plane. var1 is the blurred square minus the squared blur of the
// reference plane.
func s2SSIMMap(mu1, var1, mu2, s22, s12 []float32) (sum1, sum4 float64) {
	n := len(mu1)
	var1, mu2, s22, s12 = var1[:n], mu2[:n], s22[:n], s12[:n]
	for i, m1 := range mu1 {
		m2 := mu2[i]
		m22 := float32(m2 * m2)
		m12 := float32(m1 * m2)
		diff := m1 - m2
		numM := fma32(-diff, diff, 1)
		numS := fma32(2, s12[i]-m12, s2C2)
		denom := (var1[i] + (s22[i] - m22)) + s2C2
		d := 1 - float64((numM*numS)/denom)
		if d < 0 {
			d = 0
		}
		d2 := d * d
		sum1 += d
		sum4 += d2 * d2
	}
	return sum1, sum4
}

// s2EdgeMap returns the sums of the artifact and detail loss maps and of
// their fourth powers over one plane.
func s2EdgeMap(img1, mu1, img2, mu2 []float32) (a1, a4, l1, l4 float64) {
	n := len(img1)
	mu1, img2, mu2 = mu1[:n], img2[:n], mu2[:n]
	for i, v1 := range img1 {
		e1 := math.Abs(float64(v1 - mu1[i]))
		e2 := math.Abs(float64(img2[i] - mu2[i]))
		d := (1+e2)/(1+e1) - 1
		ad := math.Abs(d)
		a := (ad + d) * 0.5
		l := (ad - d) * 0.5
		a1 += a
		l1 += l
		a *= a
		l *= l
		a4 += a * a
		l4 += l * l
	}
	return a1, a4, l1, l4
}

// fma32 is a float32 fused multiply-add (single rounding). The reference uses
// fused operations in its blur and map loops, and the recurrences of the blur
// are sensitive enough to rounding that plain float32 arithmetic changes the
// final score by up to a point on high quality images. float64 holds the
// product of two float32 values exactly, so the result is correctly rounded
// except for a double rounding of probability 2^-29.
func fma32(a, b, c float32) float32 {
	return float32(math.FMA(float64(a), float64(b), float64(c)))
}
