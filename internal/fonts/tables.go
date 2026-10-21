package fonts

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"io"
	"slices"
	"strings"
)

// maxExtraBlock bounds the WOFF1 metadata and private blocks that are carried
// over to the WOFF2 output.
const maxExtraBlock = 1 << 20

type table struct {
	tag  string
	data []byte
}

// source is a font reduced to its flavor and tables.
type source struct {
	flavor string
	tables []table
	meta   []byte // uncompressed extended metadata (WOFF1 only)
	priv   []byte // private data (WOFF1 only)
}

func (s *source) find(tag string) []byte {
	for i := range s.tables {
		if s.tables[i].tag == tag {
			return s.tables[i].data
		}
	}
	return nil
}

// parseSFNTTables reads the table directory of a TrueType or OpenType font.
// Tables are sub-slices of b.
func parseSFNTTables(b []byte) (*source, error) {
	if len(b) < 12 || len(b) > maxFontSize {
		return nil, ErrNotConvertible
	}
	n := int(binary.BigEndian.Uint16(b[4:]))
	if n == 0 || len(b) < 12+16*n {
		return nil, ErrNotConvertible
	}
	s := &source{flavor: string(b[:4]), tables: make([]table, 0, n)}
	seen := make(map[string]struct{}, n)
	var total uint64
	for i := range n {
		rec := b[12+16*i:]
		tag := string(rec[:4])
		offset := uint64(binary.BigEndian.Uint32(rec[8:]))
		length := uint64(binary.BigEndian.Uint32(rec[12:]))
		if offset+length > uint64(len(b)) {
			return nil, ErrNotConvertible
		}
		if _, dup := seen[tag]; dup {
			return nil, ErrNotConvertible
		}
		seen[tag] = struct{}{}
		total += length
		if total > maxFontSize {
			return nil, ErrNotConvertible
		}
		s.tables = append(s.tables, table{tag, b[offset : offset+length : offset+length]})
	}
	return s, nil
}

// parseWOFFTables reads a WOFF1 font. Every inflation is bounded by the
// declared table size, and the sum of declared sizes is bounded before
// anything is allocated.
func parseWOFFTables(b []byte) (*source, error) {
	if len(b) < 44 || len(b) > maxFontSize {
		return nil, ErrNotConvertible
	}
	be := binary.BigEndian
	if be.Uint32(b[8:]) != uint32(len(b)) || be.Uint16(b[14:]) != 0 {
		return nil, ErrNotConvertible
	}
	n := int(be.Uint16(b[12:]))
	if n == 0 || len(b) < 44+20*n {
		return nil, ErrNotConvertible
	}
	s := &source{flavor: string(b[4:8]), tables: make([]table, 0, n)}
	if s.flavor == "ttcf" {
		return nil, ErrNotConvertible
	}

	type entry struct {
		tag                         string
		offset, compLength, origLen uint64
	}
	entries := make([]entry, 0, n)
	seen := make(map[string]struct{}, n)
	var total uint64
	for i := range n {
		rec := b[44+20*i:]
		e := entry{
			tag:        string(rec[:4]),
			offset:     uint64(be.Uint32(rec[4:])),
			compLength: uint64(be.Uint32(rec[8:])),
			origLen:    uint64(be.Uint32(rec[12:])),
		}
		if e.offset+e.compLength > uint64(len(b)) || e.compLength > e.origLen {
			return nil, ErrNotConvertible
		}
		if _, dup := seen[e.tag]; dup {
			return nil, ErrNotConvertible
		}
		seen[e.tag] = struct{}{}
		total += e.origLen
		if total > maxFontSize {
			return nil, ErrNotConvertible
		}
		entries = append(entries, e)
	}

	for _, e := range entries {
		raw := b[e.offset : e.offset+e.compLength]
		data := raw
		if e.compLength != e.origLen {
			var err error
			if data, err = inflateExact(raw, int(e.origLen)); err != nil {
				return nil, ErrNotConvertible
			}
		}
		s.tables = append(s.tables, table{e.tag, data})
	}

	metaOff, metaLen, metaOrig := uint64(be.Uint32(b[24:])), uint64(be.Uint32(b[28:])), uint64(be.Uint32(b[32:]))
	if metaLen != 0 {
		if metaOff+metaLen > uint64(len(b)) || metaOrig > maxExtraBlock {
			return nil, ErrNotConvertible
		}
		meta, err := inflateExact(b[metaOff:metaOff+metaLen], int(metaOrig))
		if err != nil {
			return nil, ErrNotConvertible
		}
		s.meta = meta
	}
	privOff, privLen := uint64(be.Uint32(b[36:])), uint64(be.Uint32(b[40:]))
	if privLen != 0 {
		if privOff+privLen > uint64(len(b)) || privLen > maxExtraBlock {
			return nil, ErrNotConvertible
		}
		s.priv = b[privOff : privOff+privLen : privOff+privLen]
	}
	return s, nil
}

// inflateExact inflates a zlib stream that must decode to exactly size bytes.
func inflateExact(b []byte, size int) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	out := make([]byte, size)
	if _, err := io.ReadFull(zr, out); err != nil {
		return nil, err
	}
	var extra [1]byte
	if n, _ := zr.Read(extra[:]); n != 0 {
		return nil, ErrNotConvertible
	}
	return out, nil
}

// buildSFNT serialises tables as a canonical SFNT file (sorted directory,
// 4-byte padded tables, valid checksums).
func buildSFNT(s *source) []byte {
	tables := slices.Clone(s.tables)
	slices.SortFunc(tables, func(a, b table) int { return strings.Compare(a.tag, b.tag) })

	n := len(tables)
	size := 12 + 16*n
	for _, t := range tables {
		size += (len(t.data) + 3) &^ 3
	}
	out := make([]byte, size)
	copy(out, s.flavor)
	binary.BigEndian.PutUint16(out[4:], uint16(n))
	var sel, pow uint16
	for pow = 1; int(pow)*2 <= n; pow *= 2 {
		sel++
	}
	binary.BigEndian.PutUint16(out[6:], pow*16)
	binary.BigEndian.PutUint16(out[8:], sel)
	binary.BigEndian.PutUint16(out[10:], uint16(n*16)-pow*16)

	offset := 12 + 16*n
	for i, t := range tables {
		rec := out[12+16*i:]
		copy(rec, t.tag)
		binary.BigEndian.PutUint32(rec[4:], checksum(t.data))
		binary.BigEndian.PutUint32(rec[8:], uint32(offset))
		binary.BigEndian.PutUint32(rec[12:], uint32(len(t.data)))
		copy(out[offset:], t.data)
		offset += (len(t.data) + 3) &^ 3
	}
	return out
}

// checksum computes the SFNT table checksum of data, as if zero padded to a
// multiple of four bytes.
func checksum(data []byte) uint32 {
	var sum uint32
	for len(data) >= 4 {
		sum += binary.BigEndian.Uint32(data)
		data = data[4:]
	}
	if len(data) > 0 {
		var last [4]byte
		copy(last[:], data)
		sum += binary.BigEndian.Uint32(last[:])
	}
	return sum
}
