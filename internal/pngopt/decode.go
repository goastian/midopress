package pngopt

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"

	"github.com/klauspost/compress/zlib"
)

// MaxPixels is the largest image (width*height) the package accepts.
const MaxPixels = 64 << 20

var (
	// ErrFormat is returned for malformed or unsupported PNG data.
	ErrFormat = errors.New("pngopt: invalid PNG")
	// ErrTooLarge is returned when an image exceeds MaxPixels.
	ErrTooLarge = errors.New("pngopt: image too large")
)

var pngSig = []byte("\x89PNG\r\n\x1a\n")

type chunk struct {
	typ  string
	data []byte
}

type header struct {
	w, h              int
	depth, ctype, ilc byte
}

type parsed struct {
	hdr      header
	plte     []byte
	trns     []byte
	idat     []byte
	color    []chunk
	late     []chunk
	hasICCP  bool
	animated bool
}

func (h header) channels() int {
	switch h.ctype {
	case 2:
		return 3
	case 4:
		return 2
	case 6:
		return 4
	}
	return 1
}

func (h header) bitsPerPixel() int { return h.channels() * int(h.depth) }

func (h header) rowBytes(w int) int { return (w*h.bitsPerPixel() + 7) / 8 }

func (h header) filterBPP() int { return max(1, h.bitsPerPixel()/8) }

var validDepths = map[byte][]byte{
	0: {1, 2, 4, 8, 16},
	2: {8, 16},
	3: {1, 2, 4, 8},
	4: {8, 16},
	6: {8, 16},
}

func parseHeader(d []byte) (header, error) {
	var h header
	w := binary.BigEndian.Uint32(d[0:4])
	ht := binary.BigEndian.Uint32(d[4:8])
	h.depth, h.ctype = d[8], d[9]
	h.ilc = d[12]
	if w == 0 || ht == 0 || w > 1<<31-1 || ht > 1<<31-1 {
		return h, ErrFormat
	}
	if uint64(w)*uint64(ht) > MaxPixels {
		return h, ErrTooLarge
	}
	h.w, h.h = int(w), int(ht)
	ok := false
	for _, v := range validDepths[h.ctype] {
		ok = ok || v == h.depth
	}
	if !ok || d[10] != 0 || d[11] != 0 || h.ilc > 1 {
		return h, ErrFormat
	}
	return h, nil
}

func keepEarly(typ string) bool {
	switch typ {
	case "iCCP", "sRGB", "gAMA", "cHRM", "cICP", "mDCV", "cLLI", "sBIT":
		return true
	}
	return false
}

func parse(raw []byte) (*parsed, error) {
	if len(raw) < 8+25+12 || !bytes.Equal(raw[:8], pngSig) {
		return nil, ErrFormat
	}
	p := &parsed{}
	var idats [][]byte
	idatLen := 0
	first, ended, seenIDAT := true, false, false
	seen := map[string]bool{}
	pos := 8
	for !ended && pos+12 <= len(raw) {
		n := uint64(binary.BigEndian.Uint32(raw[pos:]))
		if n > 1<<31-1 || uint64(pos)+12+n > uint64(len(raw)) {
			return nil, ErrFormat
		}
		end := pos + 8 + int(n)
		if crc32.ChecksumIEEE(raw[pos+4:end]) != binary.BigEndian.Uint32(raw[end:]) {
			return nil, ErrFormat
		}
		typ := string(raw[pos+4 : pos+8])
		data := raw[pos+8 : end]
		pos = end + 4
		if first {
			if typ != "IHDR" || len(data) != 13 {
				return nil, ErrFormat
			}
			h, err := parseHeader(data)
			if err != nil {
				return nil, err
			}
			p.hdr = h
			first = false
			continue
		}
		switch typ {
		case "IHDR":
			return nil, ErrFormat
		case "acTL":
			p.animated = true
			return p, nil
		case "IEND":
			ended = true
		case "IDAT":
			seenIDAT = true
			idats = append(idats, data)
			idatLen += len(data)
			if idatLen > len(raw) {
				return nil, ErrFormat
			}
		case "PLTE":
			if !seenIDAT && p.plte == nil {
				p.plte = data
			}
		case "tRNS":
			if !seenIDAT && p.trns == nil {
				p.trns = data
			}
		default:
			if seenIDAT || seen[typ] {
				continue
			}
			switch {
			case keepEarly(typ):
				seen[typ] = true
				p.hasICCP = p.hasICCP || typ == "iCCP"
				p.color = append(p.color, chunk{typ, data})
			case typ == "pHYs" || typ == "eXIf":
				seen[typ] = true
				p.late = append(p.late, chunk{typ, data})
			}
		}
	}
	if first || !ended || len(idats) == 0 {
		return nil, ErrFormat
	}
	if len(idats) == 1 {
		p.idat = idats[0]
	} else {
		p.idat = make([]byte, 0, idatLen)
		for _, c := range idats {
			p.idat = append(p.idat, c...)
		}
	}
	h := p.hdr
	if h.ctype == 3 {
		if len(p.plte) == 0 || len(p.plte)%3 != 0 || len(p.plte)/3 > 256 || len(p.plte)/3 > 1<<h.depth {
			return nil, ErrFormat
		}
	}
	return p, nil
}

type adamPass struct{ xo, yo, xs, ys int }

var adam7 = []adamPass{{0, 0, 8, 8}, {4, 0, 8, 8}, {0, 4, 4, 8}, {2, 0, 4, 4}, {0, 2, 2, 4}, {1, 0, 2, 2}, {0, 1, 1, 2}}

// raster is the canonical in-memory image: non-premultiplied NRGBA with
// 8-bit (4 bytes per pixel) or 16-bit big endian (8 bytes) samples. Pixels
// with alpha 0 have zero colour in 8-bit rasters; 16-bit rasters keep it.
type raster struct {
	w, h int
	deep bool
	pix  []byte
}

func (r *raster) pixSize() int {
	if r.deep {
		return 8
	}
	return 4
}

func newRaster(w, h int, deep bool) (*raster, error) {
	if w <= 0 || h <= 0 {
		return nil, ErrFormat
	}
	if uint64(w)*uint64(h) > MaxPixels {
		return nil, ErrTooLarge
	}
	r := &raster{w: w, h: h, deep: deep}
	r.pix = make([]byte, w*h*r.pixSize())
	return r, nil
}

func decodeRaster(p *parsed) (*raster, error) {
	h := p.hdr
	deep := h.depth == 16
	passes := adam7
	if h.ilc == 0 {
		passes = []adamPass{{0, 0, 1, 1}}
	}
	var total uint64
	maxRB := 0
	for _, ps := range passes {
		pw, ph := passSize(h, ps)
		if pw == 0 || ph == 0 {
			continue
		}
		rb := h.rowBytes(pw)
		maxRB = max(maxRB, rb)
		total += uint64(rb+1) * uint64(ph)
	}
	// A deflate stream cannot expand by more than about 1032:1.
	if total > uint64(len(p.idat))*1032+1024 {
		return nil, fmt.Errorf("%w: truncated image data", ErrFormat)
	}
	r, err := newRaster(h.w, h.h, deep)
	if err != nil {
		return nil, err
	}
	zr, err := zlib.NewReader(bytes.NewReader(p.idat))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrFormat, err)
	}
	defer func() { _ = zr.Close() }()

	var pal [256][4]byte
	var exp expander
	exp.init(p, &pal)

	bufA := make([]byte, maxRB+1)
	bufB := make([]byte, maxRB+1)
	bpp := h.filterBPP()
	ps8 := r.pixSize()
	for _, ps := range passes {
		pw, ph := passSize(h, ps)
		if pw == 0 || ph == 0 {
			continue
		}
		rb := h.rowBytes(pw)
		prev := bufB[1 : rb+1]
		clear(prev)
		for row := range ph {
			cur := bufA[:rb+1]
			if _, err := io.ReadFull(zr, cur); err != nil {
				return nil, fmt.Errorf("%w: %w", ErrFormat, err)
			}
			if err := unfilter(cur[0], cur[1:], prev, bpp); err != nil {
				return nil, fmt.Errorf("%w: %w", ErrFormat, err)
			}
			y := ps.yo + row*ps.ys
			base := (y*h.w + ps.xo) * ps8
			if err := exp.expand(cur[1:], pw, r.pix, base, ps.xs*ps8); err != nil {
				return nil, err
			}
			prev = bufA[1 : rb+1]
			bufA, bufB = bufB, bufA
		}
	}
	var one [1]byte
	if _, err := zr.Read(one[:]); err != nil && err != io.EOF {
		return nil, fmt.Errorf("%w: %w", ErrFormat, err)
	}
	return r, nil
}

func passSize(h header, ps adamPass) (pw, ph int) {
	return (h.w - ps.xo + ps.xs - 1) / ps.xs, (h.h - ps.yo + ps.ys - 1) / ps.ys
}

type expander struct {
	h       header
	pal     *[256][4]byte
	npal    int
	hasKey  bool
	key     [3]uint16
	scale   [256]byte
	keyMask uint16
}

func (e *expander) init(p *parsed, pal *[256][4]byte) {
	h := p.hdr
	e.h, e.pal = h, pal
	if h.depth < 8 {
		mask := 1<<h.depth - 1
		for v := 0; v <= mask; v++ {
			e.scale[v] = byte(v * 255 / mask)
		}
		e.keyMask = uint16(mask)
	}
	switch h.ctype {
	case 3:
		e.npal = len(p.plte) / 3
		for i := range e.npal {
			pal[i] = [4]byte{p.plte[3*i], p.plte[3*i+1], p.plte[3*i+2], 255}
			if i < len(p.trns) {
				pal[i][3] = p.trns[i]
			}
			if pal[i][3] == 0 {
				pal[i] = [4]byte{}
			}
		}
	case 0:
		if len(p.trns) >= 2 {
			e.hasKey = true
			e.key[0] = binary.BigEndian.Uint16(p.trns)
		}
	case 2:
		if len(p.trns) >= 6 {
			e.hasKey = true
			for i := range 3 {
				e.key[i] = binary.BigEndian.Uint16(p.trns[2*i:])
			}
		}
	}
	if h.depth == 8 {
		e.keyMask = 0xff
	}
	if e.hasKey && h.depth < 16 {
		for i := range e.key {
			e.key[i] &= e.keyMask
		}
	}
}

func (e *expander) expand(data []byte, n int, dst []byte, base, stride int) error {
	h := e.h
	o := base
	switch {
	case h.ctype == 6 && h.depth == 8:
		for i := range n {
			s := data[4*i : 4*i+4]
			if s[3] == 0 {
				clear(dst[o : o+4])
			} else {
				copy(dst[o:o+4], s)
			}
			o += stride
		}
	case h.ctype == 6:
		for i := range n {
			copy(dst[o:o+8], data[8*i:8*i+8])
			o += stride
		}
	case h.ctype == 2 && h.depth == 8:
		for i := range n {
			r, g, b := data[3*i], data[3*i+1], data[3*i+2]
			d := dst[o : o+4]
			if e.hasKey && uint16(r) == e.key[0] && uint16(g) == e.key[1] && uint16(b) == e.key[2] {
				d[0], d[1], d[2], d[3] = 0, 0, 0, 0
			} else {
				d[0], d[1], d[2], d[3] = r, g, b, 255
			}
			o += stride
		}
	case h.ctype == 2:
		for i := range n {
			s := data[6*i : 6*i+6]
			d := dst[o : o+8]
			copy(d[:6], s)
			d[6], d[7] = 255, 255
			if e.hasKey && binary.BigEndian.Uint16(s) == e.key[0] &&
				binary.BigEndian.Uint16(s[2:]) == e.key[1] && binary.BigEndian.Uint16(s[4:]) == e.key[2] {
				d[6], d[7] = 0, 0
			}
			o += stride
		}
	case h.ctype == 4 && h.depth == 8:
		for i := range n {
			g, a := data[2*i], data[2*i+1]
			d := dst[o : o+4]
			if a == 0 {
				g = 0
			}
			d[0], d[1], d[2], d[3] = g, g, g, a
			o += stride
		}
	case h.ctype == 4:
		for i := range n {
			s := data[4*i : 4*i+4]
			d := dst[o : o+8]
			d[0], d[1], d[2], d[3], d[4], d[5], d[6], d[7] = s[0], s[1], s[0], s[1], s[0], s[1], s[2], s[3]
			o += stride
		}
	case h.ctype == 0 && h.depth == 16:
		for i := range n {
			s := data[2*i : 2*i+2]
			d := dst[o : o+8]
			d[0], d[1], d[2], d[3], d[4], d[5], d[6], d[7] = s[0], s[1], s[0], s[1], s[0], s[1], 255, 255
			if e.hasKey && binary.BigEndian.Uint16(s) == e.key[0] {
				d[6], d[7] = 0, 0
			}
			o += stride
		}
	case h.ctype == 0 && h.depth == 8:
		for i := range n {
			v := data[i]
			d := dst[o : o+4]
			if e.hasKey && uint16(v) == e.key[0] {
				d[0], d[1], d[2], d[3] = 0, 0, 0, 0
			} else {
				d[0], d[1], d[2], d[3] = v, v, v, 255
			}
			o += stride
		}
	case h.ctype == 0:
		for i := range n {
			v := e.sample(data, i)
			d := dst[o : o+4]
			if e.hasKey && uint16(v) == e.key[0] {
				d[0], d[1], d[2], d[3] = 0, 0, 0, 0
			} else {
				g := e.scale[v]
				d[0], d[1], d[2], d[3] = g, g, g, 255
			}
			o += stride
		}
	case h.ctype == 3:
		for i := range n {
			var v int
			if h.depth == 8 {
				v = int(data[i])
			} else {
				v = int(e.sample(data, i))
			}
			if v >= e.npal {
				return fmt.Errorf("%w: palette index out of range", ErrFormat)
			}
			copy(dst[o:o+4], e.pal[v][:])
			o += stride
		}
	default:
		return ErrFormat
	}
	return nil
}

func (e *expander) sample(data []byte, i int) byte {
	d := int(e.h.depth)
	bit := i * d
	return data[bit>>3] >> (8 - d - bit&7) & byte(1<<d-1)
}
