package fonts

import (
	"bytes"
	"encoding/binary"
	"slices"
)

// maxVerifyPoints bounds the outline points decoded while comparing glyf
// tables, so a hostile font can not make verification arbitrarily slow.
const maxVerifyPoints = 8 << 20

// glyph is the decoded form of a glyf entry, independent of how flags and
// coordinates were packed.
type glyph struct {
	numContours int16
	bbox        [4]int16
	endPts      []uint16
	instr       []byte
	on          []bool
	xs, ys      []int32
	overlap     bool
	composite   []byte // composite component records and instructions, verbatim
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// parseGlyph decodes one glyf entry. Bytes after the glyph data must be zero
// padding. points is decremented by the number of decoded points.
func parseGlyph(b []byte, points *int) (glyph, bool) {
	var g glyph
	if len(b) == 0 {
		return g, true
	}
	if len(b) < 10 {
		return g, false
	}
	be := binary.BigEndian
	g.numContours = int16(be.Uint16(b))
	for i := range g.bbox {
		g.bbox[i] = int16(be.Uint16(b[2+2*i:]))
	}
	if g.numContours == 0 {
		return g, g.bbox == [4]int16{} && allZero(b[10:])
	}
	if g.numContours < 0 {
		if g.numContours != -1 {
			return g, false
		}
		pos := 10
		for {
			if pos+4 > len(b) {
				return g, false
			}
			flags := be.Uint16(b[pos:])
			pos += 4
			if flags&0x0001 != 0 {
				pos += 4
			} else {
				pos += 2
			}
			switch {
			case flags&0x0008 != 0:
				pos += 2
			case flags&0x0040 != 0:
				pos += 4
			case flags&0x0080 != 0:
				pos += 8
			}
			if flags&0x0020 == 0 {
				if flags&0x0100 != 0 {
					if pos+2 > len(b) {
						return g, false
					}
					pos += 2 + int(be.Uint16(b[pos:]))
				}
				break
			}
		}
		if pos > len(b) || !allZero(b[pos:]) {
			return g, false
		}
		g.composite = b[10:pos]
		return g, true
	}

	n := int(g.numContours)
	pos := 10
	if pos+2*n+2 > len(b) {
		return g, false
	}
	g.endPts = make([]uint16, n)
	for i := range g.endPts {
		g.endPts[i] = be.Uint16(b[pos:])
		pos += 2
		if i > 0 && g.endPts[i] <= g.endPts[i-1] {
			return g, false
		}
	}
	nPts := int(g.endPts[n-1]) + 1
	*points -= nPts
	if *points < 0 {
		return g, false
	}
	instrLen := int(be.Uint16(b[pos:]))
	pos += 2
	if pos+instrLen > len(b) {
		return g, false
	}
	g.instr = b[pos : pos+instrLen]
	pos += instrLen

	flags := make([]byte, 0, nPts)
	for len(flags) < nPts {
		if pos >= len(b) {
			return g, false
		}
		f := b[pos]
		pos++
		flags = append(flags, f)
		if f&0x08 != 0 {
			if pos >= len(b) {
				return g, false
			}
			r := int(b[pos])
			pos++
			if len(flags)+r > nPts {
				return g, false
			}
			for range r {
				flags = append(flags, f)
			}
		}
	}

	coords := func(shortBit, sameBit byte) ([]int32, bool) {
		out := make([]int32, nPts)
		var cur int32
		for i, f := range flags {
			switch {
			case f&shortBit != 0:
				if pos >= len(b) {
					return nil, false
				}
				d := int32(b[pos])
				pos++
				if f&sameBit == 0 {
					d = -d
				}
				cur += d
			case f&sameBit == 0:
				if pos+2 > len(b) {
					return nil, false
				}
				cur += int32(int16(be.Uint16(b[pos:])))
				pos += 2
			}
			out[i] = cur
		}
		return out, true
	}
	var ok bool
	if g.xs, ok = coords(0x02, 0x10); !ok {
		return g, false
	}
	if g.ys, ok = coords(0x04, 0x20); !ok {
		return g, false
	}
	g.on = make([]bool, nPts)
	for i, f := range flags {
		g.on[i] = f&1 != 0
	}
	g.overlap = flags[0]&0x40 != 0
	return g, allZero(b[pos:])
}

func (g *glyph) equal(h *glyph) bool {
	return g.numContours == h.numContours &&
		g.bbox == h.bbox &&
		g.overlap == h.overlap &&
		slices.Equal(g.endPts, h.endPts) &&
		bytes.Equal(g.instr, h.instr) &&
		slices.Equal(g.on, h.on) &&
		slices.Equal(g.xs, h.xs) &&
		slices.Equal(g.ys, h.ys) &&
		bytes.Equal(g.composite, h.composite)
}

// locaOffsets decodes a loca table of numGlyphs+1 entries. The offsets must
// be non-decreasing and inside glyfLen.
func locaOffsets(loca []byte, long bool, numGlyphs, glyfLen int) ([]int, bool) {
	size := 2
	if long {
		size = 4
	}
	if len(loca) != (numGlyphs+1)*size {
		return nil, false
	}
	offs := make([]int, numGlyphs+1)
	prev := 0
	for i := range offs {
		var v int
		if long {
			v = int(binary.BigEndian.Uint32(loca[4*i:]))
		} else {
			v = 2 * int(binary.BigEndian.Uint16(loca[2*i:]))
		}
		if v < prev || v > glyfLen {
			return nil, false
		}
		offs[i], prev = v, v
	}
	return offs, true
}

// glyfEquivalent reports whether two glyf/loca table pairs describe the same
// glyphs: identical outlines, instructions, bounding boxes, composite
// records and overlap flag, regardless of flag/coordinate packing and
// padding between glyphs.
func glyfEquivalent(head, maxp, glyfA, locaA, glyfB, locaB []byte) bool {
	if len(head) < 54 || len(maxp) < 6 {
		return false
	}
	numGlyphs := int(binary.BigEndian.Uint16(maxp[4:]))
	long := binary.BigEndian.Uint16(head[50:]) != 0
	offA, ok := locaOffsets(locaA, long, numGlyphs, len(glyfA))
	if !ok {
		return false
	}
	offB, ok := locaOffsets(locaB, long, numGlyphs, len(glyfB))
	if !ok {
		return false
	}
	points := maxVerifyPoints
	for i := range numGlyphs {
		a, b := glyfA[offA[i]:offA[i+1]], glyfB[offB[i]:offB[i+1]]
		if bytes.Equal(a, b) {
			continue
		}
		ga, ok := parseGlyph(a, &points)
		if !ok {
			return false
		}
		gb, ok := parseGlyph(b, &points)
		if !ok || !ga.equal(&gb) {
			return false
		}
	}
	return true
}
