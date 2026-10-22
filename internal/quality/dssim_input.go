package quality

import (
	"image"
	"image/color"
	"math"
)

// dsSource reads rows of an image as 8-bit straight (non premultiplied) RGBA,
// using a type specific fast path whenever possible.
type dsSource struct {
	kind  sourceKind
	min   image.Point
	w     int
	rgba  *image.RGBA
	nrgba *image.NRGBA
	ycc   *image.YCbCr
	nycca *image.NYCbCrA
	gray  *image.Gray
	pal   *image.Paletted
	gen   image.Image
	xs    uint
	table [256][4]uint8
}

func newDSSource(img image.Image) dsSource {
	b := img.Bounds()
	s := dsSource{min: b.Min, w: b.Dx()}
	switch m := img.(type) {
	case *image.RGBA:
		s.kind, s.rgba = kindRGBA, m
	case *image.NRGBA:
		s.kind, s.nrgba = kindNRGBA, m
	case *image.YCbCr:
		s.kind, s.ycc = kindYCbCr, m
		s.xs, _ = chromaShifts(m.SubsampleRatio)
	case *image.NYCbCrA:
		s.kind, s.nycca = kindNYCbCrA, m
		s.ycc = &m.YCbCr
		s.xs, _ = chromaShifts(m.SubsampleRatio)
	case *image.Gray:
		s.kind, s.gray = kindGray, m
	case *image.Paletted:
		s.kind, s.pal = kindPaletted, m
		for i := range s.table {
			var c color.Color
			if len(m.Palette) > 0 {
				c = m.Palette[0]
				if i < len(m.Palette) {
					c = m.Palette[i]
				}
			}
			s.table[i] = dsStraight(c)
		}
	default:
		s.kind, s.gen = kindGeneric, img
	}
	return s
}

// maybeAlpha reports whether the type of the source can carry transparency.
func (s *dsSource) maybeAlpha() bool {
	switch s.kind {
	case kindYCbCr, kindGray:
		return false
	case kindPaletted:
		for _, p := range s.pal.Palette {
			if _, _, _, a := p.RGBA(); a != 0xffff {
				return true
			}
		}
		return false
	}
	return true
}

// dsStraight converts a color to straight 8-bit RGBA. A nil color is fully
// transparent.
func dsStraight(c color.Color) [4]uint8 {
	if c == nil {
		return [4]uint8{}
	}
	n := color.NRGBAModel.Convert(c).(color.NRGBA)
	return [4]uint8{n.R, n.G, n.B, n.A}
}

// row returns the straight RGBA bytes of source row ry (0 based). The result
// either aliases the image or is dst, which must hold 4*w bytes. It must not
// be modified.
func (s *dsSource) row(ry int, dst []uint8) []uint8 {
	y := s.min.Y + ry
	dst = dst[:4*s.w]
	switch s.kind {
	case kindRGBA:
		o := s.rgba.PixOffset(s.min.X, y)
		src := s.rgba.Pix[o : o+4*s.w]
		if opaque(src) {
			return src
		}
		out := dst
		for len(src) >= 4 && len(out) >= 4 {
			a := uint32(src[3])
			if a == 0 {
				out[0], out[1], out[2], out[3] = 0, 0, 0, 0
			} else {
				out[0] = unpremul(uint32(src[0]), a)
				out[1] = unpremul(uint32(src[1]), a)
				out[2] = unpremul(uint32(src[2]), a)
				out[3] = src[3]
			}
			src, out = src[4:], out[4:]
		}
	case kindNRGBA:
		o := s.nrgba.PixOffset(s.min.X, y)
		return s.nrgba.Pix[o : o+4*s.w]
	case kindYCbCr:
		s.rowYCbCr(y, dst, nil)
	case kindNYCbCrA:
		ao := s.nycca.AOffset(s.min.X, y)
		s.rowYCbCr(y, dst, s.nycca.A[ao:ao+s.w])
	case kindGray:
		o := s.gray.PixOffset(s.min.X, y)
		out := dst
		for _, v := range s.gray.Pix[o : o+s.w] {
			out[0], out[1], out[2], out[3] = v, v, v, 255
			out = out[4:]
		}
	case kindPaletted:
		o := s.pal.PixOffset(s.min.X, y)
		out := dst
		for _, v := range s.pal.Pix[o : o+s.w] {
			t := &s.table[v]
			out[0], out[1], out[2], out[3] = t[0], t[1], t[2], t[3]
			out = out[4:]
		}
	default:
		for i := range s.w {
			c := dsStraight(s.gen.At(s.min.X+i, y))
			copy(dst[4*i:], c[:])
		}
	}
	return dst
}

func unpremul(c, a uint32) uint8 {
	return uint8(min((c*255+a/2)/a, 255))
}

func (s *dsSource) rowYCbCr(y int, dst []uint8, alpha []uint8) {
	m := s.ycc
	yo := m.YOffset(s.min.X, y)
	co := m.COffset(s.min.X, y)
	cx0 := s.min.X >> s.xs
	last := -1
	var tr, tg, tb int32
	for i, yv := range m.Y[yo : yo+s.w] {
		ci := co + ((s.min.X + i) >> s.xs) - cx0
		if ci != last {
			last = ci
			cb, cr := m.Cb[ci], m.Cr[ci]
			tr, tg, tb = crToR[cr], cbToG[cb]+crToG[cr], cbToB[cb]
		}
		yy1 := int32(yv) * 0x10101
		dst[0] = uint8(clamp16(yy1+tr) >> 8)
		dst[1] = uint8(clamp16(yy1+tg) >> 8)
		dst[2] = uint8(clamp16(yy1+tb) >> 8)
		if alpha != nil {
			dst[3] = alpha[i]
		} else {
			dst[3] = 255
		}
		dst = dst[4:]
	}
}

// dsLinear holds one scale of the image as planar linear-light RGB,
// premultiplied by alpha. The alpha plane is only meaningful when the image
// has transparency.
type dsLinear struct {
	w, h    int
	p       [4][]float32 // r, g, b, a
	hasAlph bool
}

var (
	dsLUT      = makeDSLUT()
	dsAlphaLUT = makeDSAlphaLUT()
)

func makeDSLUT() (t [256]float32) {
	for i := range t {
		s := float64(i) / 255
		if s <= 0.04045 {
			t[i] = float32(s / 12.92)
		} else {
			t[i] = float32(math.Pow((s+0.055)/1.055, 2.4))
		}
	}
	return t
}

func makeDSAlphaLUT() (t [256]float32) {
	for i := range t {
		t[i] = float32(i) / 255
	}
	return t
}

// dsConvRow converts straight RGBA bytes to premultiplied linear planes.
func dsConvRow(px []uint8, r, g, b, a []float32, withAlpha bool) {
	n := len(r)
	r, g, b = r[:n], g[:n], b[:n]
	px = px[:4*n]
	if !withAlpha {
		for i := range r {
			p := px[4*i : 4*i+4 : 4*i+4]
			r[i], g[i], b[i] = dsLUT[p[0]], dsLUT[p[1]], dsLUT[p[2]]
		}
		return
	}
	a = a[:n]
	for i := range r {
		p := px[4*i : 4*i+4 : 4*i+4]
		au := dsAlphaLUT[p[3]]
		r[i], g[i], b[i], a[i] = dsLUT[p[0]]*au, dsLUT[p[1]]*au, dsLUT[p[2]]*au, au
	}
}

// dsAccRow adds, for every block of f source pixels of the straight RGBA row
// px (w pixels), the premultiplied linear values to acc.
func dsAccRow(px []uint8, f, w int, acc *[4][]float32, withAlpha bool) {
	rw := len(acc[0])
	ar, ag, ab, aa := acc[0][:rw], acc[1][:rw], acc[2][:rw], acc[3][:rw]
	for ox := range rw {
		blk := px[4*ox*f : 4*min(ox*f+f, w)]
		var sr, sg, sb, sa float32
		if withAlpha {
			for ; len(blk) >= 4; blk = blk[4:] {
				p := blk[:4:4]
				au := dsAlphaLUT[p[3]]
				sr += dsLUT[p[0]] * au
				sg += dsLUT[p[1]] * au
				sb += dsLUT[p[2]] * au
				sa += au
			}
		} else {
			for ; len(blk) >= 4; blk = blk[4:] {
				p := blk[:4:4]
				sr += dsLUT[p[0]]
				sg += dsLUT[p[1]]
				sb += dsLUT[p[2]]
			}
		}
		ar[ox] += sr
		ag[ox] += sg
		ab[ox] += sb
		aa[ox] += sa
	}
}

// load reads img into dst at reduced size (box filter of factor f, performed
// on linear light) and sets dst.hasAlph.
func (s *dsScratch) load(img image.Image, f int, dst *dsLinear) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	rw, rh := ceilDiv(w, f), ceilDiv(h, f)
	dst.w, dst.h = rw, rh
	src := newDSSource(img)
	withAlpha := src.maybeAlpha()
	for i := range dst.p {
		if i < 3 || withAlpha {
			dst.p[i] = grow(dst.p[i], rw*rh)
		}
	}
	s.row = grow(s.row, 4*w)
	dst.hasAlph = false

	if f == 1 {
		for y := range h {
			px := src.row(y, s.row)
			if withAlpha && !opaque(px) {
				dst.hasAlph = true
			}
			var al []float32
			if withAlpha {
				al = dst.p[3][y*w : (y+1)*w]
			}
			dsConvRow(px, dst.p[0][y*w:(y+1)*w], dst.p[1][y*w:(y+1)*w], dst.p[2][y*w:(y+1)*w], al, withAlpha)
		}
		return
	}

	for i := range s.lacc {
		s.lacc[i] = grow(s.lacc[i], rw)
	}
	for oy := range rh {
		for i := range s.lacc {
			clear(s.lacc[i])
		}
		y0, y1 := oy*f, min(oy*f+f, h)
		for y := y0; y < y1; y++ {
			px := src.row(y, s.row)
			if withAlpha && !opaque(px) {
				dst.hasAlph = true
			}
			dsAccRow(px, f, w, &s.lacc, withAlpha)
		}
		rows := y1 - y0
		for i := range s.lacc {
			if i == 3 && !withAlpha {
				continue
			}
			out := dst.p[i][oy*rw : (oy+1)*rw]
			for ox, v := range s.lacc[i] {
				out[ox] = v / float32(rows*(min(ox*f+f, w)-ox*f))
			}
		}
	}
}

// downsample halves src with a 2x2 box filter. It reports false (and leaves
// dst alone) when src is smaller than 8 pixels in either dimension.
func dsDownsample(dst, src *dsLinear) bool {
	if src.w < 8 || src.h < 8 {
		return false
	}
	w, h := src.w/2, src.h/2
	dst.w, dst.h, dst.hasAlph = w, h, src.hasAlph
	planes := 3
	if src.hasAlph {
		planes = 4
	}
	for i := range planes {
		dst.p[i] = grow(dst.p[i], w*h)
		in, out := src.p[i], dst.p[i]
		for y := range h {
			top := in[2*y*src.w : 2*y*src.w+2*w]
			bot := in[(2*y+1)*src.w : (2*y+1)*src.w+2*w]
			o := out[y*w : (y+1)*w]
			for x := range o {
				o[x] = (top[2*x] + top[2*x+1] + bot[2*x] + bot[2*x+1]) * 0.25
			}
		}
	}
	return true
}
