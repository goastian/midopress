package quality

import (
	"encoding/binary"
	"image"
)

// planeBuilder turns consecutive RGB rows into the luma plane and the 2x2
// reduced chroma planes without keeping the full RGB image around.
type planeBuilder struct {
	luma, cb, cr []uint8
	w, h, cw     int
	y            int
	even, odd    []uint16
}

// extract converts img into the luma plane and the 2x2 reduced chroma planes
// (after an optional box reduction by factor f) in s.planes and returns the
// width and height of each plane.
func (s *scratch) extract(img image.Image, f int) [3][2]int {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	rw, rh := ceilDiv(w, f), ceilDiv(h, f)
	cw, ch := ceilDiv(rw, 2), ceilDiv(rh, 2)
	s.planes[0] = grow(s.planes[0], rw*rh)
	s.planes[1] = grow(s.planes[1], cw*ch)
	s.planes[2] = grow(s.planes[2], cw*ch)
	s.hsum = grow(s.hsum, 6*cw)
	pb := planeBuilder{
		luma: s.planes[0], cb: s.planes[1], cr: s.planes[2],
		w: rw, h: rh, cw: cw,
		even: s.hsum[:3*cw], odd: s.hsum[3*cw:],
	}
	src := newSource(img)
	s.row = grow(s.row, w*4)
	if f == 1 {
		for y := range h {
			pb.add(src.row(y, s.row))
		}
	} else {
		s.reduce(&src, w, h, f, &pb)
	}
	return [3][2]int{{rw, rh}, {cw, ch}, {cw, ch}}
}

// reduce box filters the source by f in both directions and feeds the
// reduced rows to pb.
func (s *scratch) reduce(src *source, w, h, f int, pb *planeBuilder) {
	rw, rh := pb.w, pb.h
	s.acc = grow(s.acc, rw*3)
	s.out = grow(s.out, rw*4)
	for oy := range rh {
		clear(s.acc)
		y0, y1 := oy*f, min(oy*f+f, h)
		for y := y0; y < y1; y++ {
			row := src.row(y, s.row)
			for ox := range rw {
				x0, x1 := ox*f, min(ox*f+f, w)
				var sr, sg, sb uint32
				px := row[4*x0 : 4*x1]
				for len(px) >= 4 {
					sr += uint32(px[0])
					sg += uint32(px[1])
					sb += uint32(px[2])
					px = px[4:]
				}
				a := s.acc[3*ox : 3*ox+3 : 3*ox+3]
				a[0] += sr
				a[1] += sg
				a[2] += sb
			}
		}
		rows := uint32(y1 - y0)
		for ox := range rw {
			n := rows * uint32(min(ox*f+f, w)-ox*f)
			for k := range 3 {
				s.out[4*ox+k] = uint8((s.acc[3*ox+k] + n/2) / n)
			}
		}
		pb.add(s.out)
	}
}

// add consumes the next RGBX row.
func (pb *planeBuilder) add(row []uint8) {
	y := pb.y
	pb.y++
	row = row[:4*pb.w]
	dst := pb.even
	if y%2 == 1 {
		dst = pb.odd
	}
	lumaAndPairs(pb.luma[y*pb.w:(y+1)*pb.w], dst, row)
	switch {
	case y%2 == 1:
		pb.chromaRow(y/2, pb.even, pb.odd)
	case y == pb.h-1:
		pb.chromaRow(y/2, pb.even, pb.even)
	}
}

func luma(r, g, b uint32) uint8 {
	return uint8((19595*r + 38470*g + 7471*b + 1<<15) >> 16)
}

// lumaAndPairs computes the luma of every pixel of an RGBX row and stores the
// per-channel sums of horizontally adjacent pixel pairs in pairs. A trailing
// unpaired pixel is counted twice, which makes the later average equal to the
// average over the pixels that exist.
func lumaAndPairs(dst []uint8, pairs []uint16, row []uint8) {
	w := len(dst)
	for cx := range w / 2 {
		v := binary.LittleEndian.Uint64(row[8*cx:])
		r0, g0, b0 := uint32(v&0xff), uint32(v>>8&0xff), uint32(v>>16&0xff)
		r1, g1, b1 := uint32(v>>32&0xff), uint32(v>>40&0xff), uint32(v>>48&0xff)
		dst[2*cx] = luma(r0, g0, b0)
		dst[2*cx+1] = luma(r1, g1, b1)
		d := pairs[3*cx : 3*cx+3 : 3*cx+3]
		d[0], d[1], d[2] = uint16(r0+r1), uint16(g0+g1), uint16(b0+b1)
	}
	if w%2 == 1 {
		p := row[4*(w-1) : 4*w : 4*w]
		dst[w-1] = luma(uint32(p[0]), uint32(p[1]), uint32(p[2]))
		d := pairs[3*(w/2) : 3*(w/2)+3 : 3*(w/2)+3]
		d[0], d[1], d[2] = 2*uint16(p[0]), 2*uint16(p[1]), 2*uint16(p[2])
	}
}

func (pb *planeBuilder) chromaRow(cy int, top, bot []uint16) {
	cb := pb.cb[cy*pb.cw : (cy+1)*pb.cw]
	cr := pb.cr[cy*pb.cw : (cy+1)*pb.cw]
	top, bot = top[:3*pb.cw], bot[:3*pb.cw]
	for cx := range cb {
		t := top[3*cx : 3*cx+3 : 3*cx+3]
		b := bot[3*cx : 3*cx+3 : 3*cx+3]
		r := int32((t[0] + b[0] + 2) >> 2)
		g := int32((t[1] + b[1] + 2) >> 2)
		bl := int32((t[2] + b[2] + 2) >> 2)
		cb[cx], cr[cx] = chroma(r, g, bl)
	}
}

// chroma is the Cb/Cr part of color.RGBToYCbCr.
func chroma(r, g, b int32) (uint8, uint8) {
	cb := -11056*r - 21712*g + 32768*b + 257<<15
	if uint32(cb)&0xff000000 == 0 {
		cb >>= 16
	} else {
		cb = ^(cb >> 31)
	}
	cr := 32768*r - 27440*g - 5328*b + 257<<15
	if uint32(cr)&0xff000000 == 0 {
		cr >>= 16
	} else {
		cr = ^(cr >> 31)
	}
	return uint8(cb), uint8(cr)
}
