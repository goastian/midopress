package imgmeta

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// JXL is what the header of a JPEG XL file says about the picture. Only files
// whose header can be read to the end and say, without room for doubt, that they
// are a still picture in sRGB with no alpha are described: the rest is an error,
// because the codestream has more ways to describe colour, orientation, previews
// and animation than a recode would keep.
type JXL struct {
	Width, Height int
}

var errNotPlainJXL = errors.New("imgmeta: not a plain still JPEG XL picture in sRGB")

// jxlBits reads the bit stream of a codestream: least significant bit first.
type jxlBits struct {
	b   []byte
	pos int // in bits
	err bool
}

func (r *jxlBits) bits(n int) uint32 {
	var v uint32
	for i := 0; i < n; i++ {
		if r.pos>>3 >= len(r.b) {
			r.err = true
			return 0
		}
		v |= uint32(r.b[r.pos>>3]>>(r.pos&7)&1) << i
		r.pos++
	}
	return v
}

func (r *jxlBits) flag() bool { return r.bits(1) == 1 }

// u32 reads a value coded as one of four distributions: the two bits that come
// first pick which, and each is a constant or a number of bits plus an offset.
func (r *jxlBits) u32(d [4]jxlDist) uint32 {
	x := d[r.bits(2)]
	if x.bits == 0 {
		return x.offset
	}
	return r.bits(x.bits) + x.offset
}

type jxlDist struct {
	bits   int
	offset uint32
}

func val(v uint32) jxlDist            { return jxlDist{0, v} }
func bitsOff(n int, o uint32) jxlDist { return jxlDist{n, o} }

// enum reads an enumeration: 0, 1, 2 to 17 or 18 to 81.
func (r *jxlBits) enum() uint32 {
	return r.u32([4]jxlDist{val(0), val(1), bitsOff(4, 2), bitsOff(6, 18)})
}

var jxlRatios = [8][2]int{{0, 0}, {1, 1}, {12, 10}, {4, 3}, {3, 2}, {16, 9}, {5, 4}, {2, 1}}

// ParseJXL reads the header of a JPEG XL file, bare or in its container.
func ParseJXL(raw []byte) (*JXL, error) {
	code, err := jxlCodestream(raw)
	if err != nil {
		return nil, err
	}
	r := &jxlBits{b: code}

	// SizeHeader
	var w, h int
	small := r.flag()
	if small {
		h = (int(r.bits(5)) + 1) * 8
	} else {
		h = int(r.u32([4]jxlDist{bitsOff(9, 1), bitsOff(13, 1), bitsOff(18, 1), bitsOff(30, 1)}))
	}
	ratio := r.bits(3)
	if ratio == 0 {
		if small {
			w = (int(r.bits(5)) + 1) * 8
		} else {
			w = int(r.u32([4]jxlDist{bitsOff(9, 1), bitsOff(13, 1), bitsOff(18, 1), bitsOff(30, 1)}))
		}
	} else {
		w = h * jxlRatios[ratio][0] / jxlRatios[ratio][1]
	}
	if r.err || w < 1 || h < 1 || w > 1<<24 || h > 1<<24 {
		return nil, errNotPlainJXL
	}

	// ImageMetadata
	if !r.flag() { // not all default, which would be 8 bit sRGB RGB with nothing else
		if r.flag() { // extra fields: orientation, intrinsic size, preview, animation
			return nil, errNotPlainJXL
		}
		if r.flag() { // float samples
			return nil, errNotPlainJXL
		}
		r.u32([4]jxlDist{val(8), val(10), val(12), bitsOff(6, 1)}) // bits per sample
		r.flag()                                                   // modular 16 bit buffers
		if r.u32([4]jxlDist{val(0), val(1), bitsOff(4, 2), bitsOff(12, 1)}) != 0 {
			return nil, errNotPlainJXL // an alpha plane or another channel
		}
		r.flag()       // xyb encoded: how the pixels are coded, not what colour they are
		if !r.flag() { // colour encoding is not the default, which is sRGB
			if r.flag() { // want_icc: the profile is in the file
				return nil, errNotPlainJXL
			}
			space := r.enum()
			if space != 0 { // RGB
				return nil, errNotPlainJXL
			}
			if r.enum() != 1 { // white point D65
				return nil, errNotPlainJXL
			}
			if r.enum() != 1 { // primaries sRGB
				return nil, errNotPlainJXL
			}
			if r.flag() { // a gamma
				return nil, errNotPlainJXL
			}
			if r.enum() != 13 { // transfer function sRGB
				return nil, errNotPlainJXL
			}
			r.enum() // rendering intent: does not change the pixels a browser shows
		}
		// tone mapping exists only with extra fields, and the extensions that
		// follow are either empty or something this does not understand
		if r.u64() != 0 {
			return nil, errNotPlainJXL
		}
	}
	if r.err {
		return nil, errNotPlainJXL
	}
	return &JXL{Width: w, Height: h}, nil
}

// u64 reads the selector of the extensions: 0 for none.
func (r *jxlBits) u64() uint64 {
	switch r.bits(2) {
	case 0:
		return 0
	case 1:
		return uint64(r.bits(4)) + 1
	case 2:
		return uint64(r.bits(8)) + 17
	}
	return 1 << 40 // more than any file this accepts
}

// jxlCodestream finds the codestream in a file: the file itself when it starts
// with the signature of a bare one, else the first jxlc box, or the first part of
// a jxlp.
func jxlCodestream(raw []byte) ([]byte, error) {
	if len(raw) >= 2 && raw[0] == 0xFF && raw[1] == 0x0A {
		return raw[2:], nil
	}
	sig := []byte{0, 0, 0, 0x0C, 'J', 'X', 'L', ' ', 0x0D, 0x0A, 0x87, 0x0A}
	if !bytes.HasPrefix(raw, sig) {
		return nil, errNotPlainJXL
	}
	for _, bx := range isoBoxes(raw[len(sig):]) {
		switch bx.typ {
		case "jxlc":
			if len(bx.body) >= 2 && bx.body[0] == 0xFF && bx.body[1] == 0x0A {
				return bx.body[2:], nil
			}
		case "jxlp":
			if len(bx.body) >= 6 && binary.BigEndian.Uint32(bx.body)&0x7fffffff == 0 && bx.body[4] == 0xFF && bx.body[5] == 0x0A {
				return bx.body[6:], nil
			}
		}
	}
	return nil, errNotPlainJXL
}
