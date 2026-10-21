package quality

import (
	"image"
	"image/color"
)

// greyBackground is the 8-bit value images are composited over.
const greyBackground = 128

type sourceKind uint8

const (
	kindGeneric sourceKind = iota
	kindRGBA
	kindNRGBA
	kindYCbCr
	kindNYCbCrA
	kindGray
	kindPaletted
)

// source reads rows of an image as 8-bit interleaved RGBX (4 bytes per pixel,
// the last one unused) composited over mid-grey, using a type specific fast
// path whenever possible.
type source struct {
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
	ys    uint
	table [256][4]uint8
}

func newSource(img image.Image) source {
	b := img.Bounds()
	s := source{min: b.Min, w: b.Dx()}
	switch m := img.(type) {
	case *image.RGBA:
		s.kind, s.rgba = kindRGBA, m
	case *image.NRGBA:
		s.kind, s.nrgba = kindNRGBA, m
	case *image.YCbCr:
		s.kind, s.ycc = kindYCbCr, m
		s.xs, s.ys = chromaShifts(m.SubsampleRatio)
	case *image.NYCbCrA:
		s.kind, s.nycca = kindNYCbCrA, m
		s.ycc = &m.YCbCr
		s.xs, s.ys = chromaShifts(m.SubsampleRatio)
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
			s.table[i] = colorToRGB(c)
		}
	default:
		s.kind, s.gen = kindGeneric, img
	}
	return s
}

func chromaShifts(r image.YCbCrSubsampleRatio) (xs, ys uint) {
	switch r {
	case image.YCbCrSubsampleRatio422:
		return 1, 0
	case image.YCbCrSubsampleRatio420:
		return 1, 1
	case image.YCbCrSubsampleRatio440:
		return 0, 1
	case image.YCbCrSubsampleRatio411:
		return 2, 0
	case image.YCbCrSubsampleRatio410:
		return 2, 1
	default:
		return 0, 0
	}
}

// over composites an 8-bit alpha-premultiplied channel over mid-grey.
func over(c, a uint32) uint8 {
	v := c + (greyBackground*(255-a)+127)/255
	return uint8(min(v, 255))
}

// colorToRGB defines the reference semantics every fast path must reproduce:
// the premultiplied 16-bit channels are truncated to 8 bits and composited
// over mid-grey. A nil color is treated as fully transparent.
func colorToRGB(c color.Color) [4]uint8 {
	if c == nil {
		return [4]uint8{greyBackground, greyBackground, greyBackground, 255}
	}
	r, g, b, a := c.RGBA()
	a >>= 8
	return [4]uint8{over(r>>8, a), over(g>>8, a), over(b>>8, a), 255}
}

// Fixed-point contributions of the chroma samples, matching the arithmetic
// of color.YCbCr.RGBA.
var (
	crToR = chromaTable(91881)
	crToG = chromaTable(-46802)
	cbToG = chromaTable(-22554)
	cbToB = chromaTable(116130)
)

func chromaTable(k int32) (t [256]int32) {
	for i := range t {
		t[i] = k * (int32(i) - 128)
	}
	return t
}

// clamp16 turns a value at 24-bit scale into the 16-bit channel produced by
// color.YCbCr.RGBA.
func clamp16(v int32) uint32 {
	if uint32(v)&0xff000000 == 0 {
		return uint32(v >> 8)
	}
	return uint32(^(v >> 31) & 0xffff)
}

// ycc16 is the 16-bit conversion used by color.YCbCr.RGBA.
func ycc16(y, cb, cr uint8) (uint32, uint32, uint32) {
	yy1 := int32(y) * 0x10101
	return clamp16(yy1 + crToR[cr]), clamp16(yy1 + cbToG[cb] + crToG[cr]), clamp16(yy1 + cbToB[cb])
}

// row returns the RGBX values of source row ry (0 based). The result either
// aliases the image (opaque RGBA and NRGBA rows) or is dst, which must hold
// 4*w bytes. It must not be modified.
func (s *source) row(ry int, dst []uint8) []uint8 {
	y := s.min.Y + ry
	dst = dst[:4*s.w]
	switch s.kind {
	case kindRGBA:
		return s.rowRGBA(y, dst)
	case kindNRGBA:
		return s.rowNRGBA(y, dst)
	case kindYCbCr:
		s.rowYCbCr(y, dst)
	case kindNYCbCrA:
		s.rowNYCbCrA(y, dst)
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
			out[0], out[1], out[2], out[3] = t[0], t[1], t[2], 255
			out = out[4:]
		}
	default:
		for i := range s.w {
			c := colorToRGB(s.gen.At(s.min.X+i, y))
			copy(dst[4*i:], c[:])
		}
	}
	return dst
}

// opaque reports whether every alpha byte of a 4 byte per pixel row is 255.
func opaque(pix []uint8) bool {
	acc := uint8(255)
	for i := 3; i < len(pix); i += 4 {
		acc &= pix[i]
	}
	return acc == 255
}

func (s *source) rowRGBA(y int, dst []uint8) []uint8 {
	o := s.rgba.PixOffset(s.min.X, y)
	src := s.rgba.Pix[o : o+4*s.w]
	if opaque(src) {
		return src
	}
	out := dst
	for len(src) >= 4 && len(out) >= 4 {
		a := uint32(src[3])
		out[0], out[1], out[2], out[3] = over(uint32(src[0]), a), over(uint32(src[1]), a), over(uint32(src[2]), a), 255
		src, out = src[4:], out[4:]
	}
	return dst
}

// premulNRGBA mirrors color.NRGBA.RGBA followed by truncation to 8 bits.
func premulNRGBA(c, a uint8) uint32 {
	v := uint32(c)
	v |= v << 8
	v *= uint32(a)
	v /= 0xff
	return v >> 8
}

func (s *source) rowNRGBA(y int, dst []uint8) []uint8 {
	o := s.nrgba.PixOffset(s.min.X, y)
	src := s.nrgba.Pix[o : o+4*s.w]
	if opaque(src) {
		return src
	}
	out := dst
	for len(src) >= 4 && len(out) >= 4 {
		a := src[3]
		a32 := uint32(a)
		out[0] = over(premulNRGBA(src[0], a), a32)
		out[1] = over(premulNRGBA(src[1], a), a32)
		out[2] = over(premulNRGBA(src[2], a), a32)
		out[3] = 255
		src, out = src[4:], out[4:]
	}
	return dst
}

func (s *source) rowYCbCr(y int, dst []uint8) {
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
		dst = dst[4:]
	}
}

func (s *source) rowNYCbCrA(y int, dst []uint8) {
	m := s.nycca
	yo := m.YOffset(s.min.X, y)
	co := m.COffset(s.min.X, y)
	ao := m.AOffset(s.min.X, y)
	cx0 := s.min.X >> s.xs
	ap := m.A[ao : ao+s.w]
	for i, yv := range m.Y[yo : yo+s.w] {
		ci := co + ((s.min.X + i) >> s.xs) - cx0
		r, g, b := ycc16(yv, m.Cb[ci], m.Cr[ci])
		a8 := uint32(ap[i])
		a := a8 * 0x101
		r = r * a / 0xffff >> 8
		g = g * a / 0xffff >> 8
		b = b * a / 0xffff >> 8
		dst[0], dst[1], dst[2] = over(r, a8), over(g, a8), over(b, a8)
		dst = dst[4:]
	}
}
