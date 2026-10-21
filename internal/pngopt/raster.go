package pngopt

import (
	"image"
	"image/color"
)

// fromImage converts any image to the canonical raster. Types that can hold
// more than 8 bits per sample produce a 16-bit raster.
func fromImage(img image.Image) (*raster, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, ErrFormat
	}
	deep := false
	switch img.(type) {
	case *image.NRGBA64, *image.RGBA64, *image.Gray16:
		deep = true
	}
	r, err := newRaster(w, h, deep)
	if err != nil {
		return nil, err
	}
	switch im := img.(type) {
	case *image.NRGBA:
		for y := range h {
			row := im.Pix[y*im.Stride : y*im.Stride+4*w]
			dst := r.pix[y*w*4 : (y+1)*w*4]
			copy(dst, row)
			for i := 3; i < len(dst); i += 4 {
				if dst[i] == 0 {
					dst[i-3], dst[i-2], dst[i-1] = 0, 0, 0
				}
			}
		}
	case *image.RGBA:
		for y := range h {
			row := im.Pix[y*im.Stride : y*im.Stride+4*w]
			dst := r.pix[y*w*4 : (y+1)*w*4]
			for i := 0; i < len(row); i += 4 {
				switch a := row[i+3]; a {
				case 255:
					copy(dst[i:i+4], row[i:i+4])
				case 0:
				default:
					a16 := uint32(a) * 0x101
					for k := range 3 {
						dst[i+k] = byte((uint32(row[i+k]) * 0x101 * 0xffff / a16) >> 8)
					}
					dst[i+3] = a
				}
			}
		}
	case *image.Gray:
		for y := range h {
			row := im.Pix[y*im.Stride : y*im.Stride+w]
			dst := r.pix[y*w*4 : (y+1)*w*4]
			for x, v := range row {
				dst[4*x], dst[4*x+1], dst[4*x+2], dst[4*x+3] = v, v, v, 255
			}
		}
	case *image.Paletted:
		pal := make([]color.NRGBA, len(im.Palette))
		for i, c := range im.Palette {
			pal[i] = color.NRGBAModel.Convert(c).(color.NRGBA)
			if pal[i].A == 0 {
				pal[i] = color.NRGBA{}
			}
		}
		for y := range h {
			row := im.Pix[y*im.Stride : y*im.Stride+w]
			dst := r.pix[y*w*4 : (y+1)*w*4]
			for x, idx := range row {
				var c color.NRGBA
				if int(idx) < len(pal) {
					c = pal[idx]
				}
				dst[4*x], dst[4*x+1], dst[4*x+2], dst[4*x+3] = c.R, c.G, c.B, c.A
			}
		}
	default:
		o := 0
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				c := img.At(x, y)
				if deep {
					n := color.NRGBA64Model.Convert(c).(color.NRGBA64)
					r.pix[o], r.pix[o+1] = byte(n.R>>8), byte(n.R)
					r.pix[o+2], r.pix[o+3] = byte(n.G>>8), byte(n.G)
					r.pix[o+4], r.pix[o+5] = byte(n.B>>8), byte(n.B)
					r.pix[o+6], r.pix[o+7] = byte(n.A>>8), byte(n.A)
					o += 8
					continue
				}
				n := color.NRGBAModel.Convert(c).(color.NRGBA)
				if n.A == 0 {
					n = color.NRGBA{}
				}
				r.pix[o], r.pix[o+1], r.pix[o+2], r.pix[o+3] = n.R, n.G, n.B, n.A
				o += 4
			}
		}
	}
	return r, nil
}

// reduce16 returns the 8-bit equivalent of r when every sample is of the
// form v*257, or nil when the reduction would not be exact. Pixels with alpha
// 0 must also have zero colour, as 16-bit decoders report that colour.
func reduce16(r *raster) *raster {
	for i := 0; i < len(r.pix); i += 8 {
		for j := i; j < i+8; j += 2 {
			if r.pix[j] != r.pix[j+1] {
				return nil
			}
		}
		a := uint16(r.pix[i+6])<<8 | uint16(r.pix[i+7])
		switch {
		case a == 0:
			if (r.pix[i] | r.pix[i+2] | r.pix[i+4]) != 0 {
				return nil
			}
		case a != 0xffff && !roundTrips(r.pix[i:i+8]):
			return nil
		}
	}
	out := &raster{w: r.w, h: r.h, pix: make([]byte, len(r.pix)/2)}
	for i := range out.pix {
		out.pix[i] = r.pix[2*i]
	}
	return out
}

// roundTrips reports whether a semi-transparent 16-bit pixel survives being
// stored as 8-bit NRGBA and read back through Go's premultiplying colour
// models, which are lossy for low alpha. Decoders built on those models
// would otherwise report different pixels.
func roundTrips(p []byte) bool {
	c8 := color.NRGBA{p[0], p[2], p[4], p[6]}
	c16 := color.NRGBA64Model.Convert(c8).(color.NRGBA64)
	want := color.NRGBA64{
		uint16(p[0])<<8 | uint16(p[1]), uint16(p[2])<<8 | uint16(p[3]),
		uint16(p[4])<<8 | uint16(p[5]), uint16(p[6])<<8 | uint16(p[7]),
	}
	return c16 == want
}

// to8 returns an 8-bit view of r, rounding 16-bit samples.
func to8(r *raster) *raster {
	if !r.deep {
		return r
	}
	out := &raster{w: r.w, h: r.h, pix: make([]byte, len(r.pix)/2)}
	for i := range out.pix {
		v := uint32(r.pix[2*i])<<8 | uint32(r.pix[2*i+1])
		out.pix[i] = byte((v + 128) / 257)
	}
	return out
}

// colorTable is a fixed-capacity hash table mapping up to 256 packed colours
// to their first-seen index.
type colorTable struct {
	keys [1024]uint32
	vals [1024]int16
	list [256]uint32
	n    int
}

func newColorTable() *colorTable {
	t := &colorTable{}
	for i := range t.vals {
		t.vals[i] = -1
	}
	return t
}

func (t *colorTable) slot(key uint32) int {
	h := int(key * 0x9E3779B1 >> 22)
	for t.vals[h] >= 0 && t.keys[h] != key {
		h = (h + 1) & 1023
	}
	return h
}

func (t *colorTable) find(key uint32) int { return int(t.vals[t.slot(key)]) }

// add returns the index of key, inserting it when absent, or -1 when the
// table is full.
func (t *colorTable) add(key uint32) int {
	h := t.slot(key)
	if v := t.vals[h]; v >= 0 {
		return int(v)
	}
	if t.n == 256 {
		return -1
	}
	t.keys[h] = key
	t.vals[h] = int16(t.n)
	t.list[t.n] = key
	t.n++
	return t.n - 1
}

func packKey(r, g, b, a byte) uint32 {
	return uint32(r)<<24 | uint32(g)<<16 | uint32(b)<<8 | uint32(a)
}

func unpackKey(k uint32) [4]byte { return [4]byte{byte(k >> 24), byte(k >> 16), byte(k >> 8), byte(k)} }

type analysis struct {
	opaque, gray, palOK bool
	tab                 *colorTable
	counts              [256]int
}

func analyze8(r *raster) *analysis {
	a := &analysis{opaque: true, gray: true, palOK: true, tab: newColorTable()}
	pix := r.pix
	for i := 0; i+3 < len(pix); i += 4 {
		rr, g, b, al := pix[i], pix[i+1], pix[i+2], pix[i+3]
		if al != 255 {
			a.opaque = false
		}
		if rr != g || g != b {
			a.gray = false
		}
		if a.palOK {
			idx := a.tab.add(packKey(rr, g, b, al))
			if idx < 0 {
				a.palOK = false
			} else {
				a.counts[idx]++
			}
		} else if !a.opaque && !a.gray {
			break
		}
	}
	return a
}

func analyze16(r *raster) (opaque, gray bool) {
	opaque, gray = true, true
	p := r.pix
	for i := 0; i+7 < len(p); i += 8 {
		if p[i+6] != 255 || p[i+7] != 255 {
			opaque = false
		}
		if p[i] != p[i+2] || p[i+1] != p[i+3] || p[i] != p[i+4] || p[i+1] != p[i+5] {
			gray = false
		}
		if !opaque && !gray {
			break
		}
	}
	return opaque, gray
}

// stdGrayDepth returns the smallest bit depth (1, 2, 4 or 8) that represents
// every grey level of an opaque grey image exactly.
func stdGrayDepth(t *colorTable) int {
	for _, d := range []int{1, 2, 4} {
		step := 255 / (1<<d - 1)
		ok := true
		for i := range t.n {
			if int(t.list[i]>>24)%step != 0 {
				ok = false
				break
			}
		}
		if ok {
			return d
		}
	}
	return 8
}
