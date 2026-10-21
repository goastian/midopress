package fonts

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"strings"

	"github.com/andybalholm/brotli"
)

var woff2KnownTags = []string{
	"cmap", "head", "hhea", "hmtx",
	"maxp", "name", "OS/2", "post",
	"cvt ", "fpgm", "glyf", "loca",
	"prep", "CFF ", "VORG", "EBDT",
	"EBLC", "gasp", "hdmx", "kern",
	"LTSH", "PCLT", "VDMX", "vhea",
	"vmtx", "BASE", "GDEF", "GPOS",
	"GSUB", "EBSC", "JSTF", "MATH",
	"CBDT", "CBLC", "COLR", "CPAL",
	"SVG ", "sbix", "acnt", "avar",
	"bdat", "bloc", "bsln", "cvar",
	"fdsc", "feat", "fmtx", "fvar",
	"gvar", "hsty", "just", "lcar",
	"mort", "morx", "opbd", "prop",
	"trak", "Zapf", "Silf", "Glat",
	"Gloc", "Feat", "Sill",
}

var errW2 = errors.New("fonts: invalid woff2 stream")

// w2entry is one table of a WOFF2 container.
type w2entry struct {
	tag     string
	version byte   // transform version (bits 6-7 of the directory flags)
	origLen uint32 // length of the original table
	data    []byte // bytes stored in the compressed stream (transformed if transformed() is true)
}

// transformed reports whether the directory entry carries a transformLength.
func (e *w2entry) transformed() bool {
	return (e.tag == "glyf" || e.tag == "loca") && e.version == 0 || e.tag == "hmtx" && e.version == 1
}

func appendBase128(b []byte, v uint32) []byte {
	var tmp [5]byte
	i := len(tmp)
	for {
		i--
		tmp[i] = byte(v & 0x7F)
		v >>= 7
		if v == 0 {
			break
		}
	}
	for j := i; j < len(tmp)-1; j++ {
		tmp[j] |= 0x80
	}
	return append(b, tmp[i:]...)
}

func readBase128(b []byte) (uint32, []byte, error) {
	var v uint32
	for i := range 5 {
		if i >= len(b) {
			return 0, nil, errW2
		}
		c := b[i]
		if i == 0 && c == 0x80 || v&0xFE000000 != 0 {
			return 0, nil, errW2
		}
		v = v<<7 | uint32(c&0x7F)
		if c&0x80 == 0 {
			return v, b[i+1:], nil
		}
	}
	return 0, nil, errW2
}

// parseW2Entries decodes the table directory and decompresses the data of a
// WOFF2 file without applying any inverse transform. It is only used on the
// output of the transform step, never on untrusted input.
func parseW2Entries(b []byte) ([]w2entry, error) {
	if len(b) < 48 || string(b[:4]) != "wOF2" {
		return nil, errW2
	}
	n := int(binary.BigEndian.Uint16(b[12:]))
	compSize := int(binary.BigEndian.Uint32(b[20:]))
	rest := b[48:]
	entries := make([]w2entry, 0, n)
	var total uint64
	for range n {
		if len(rest) < 1 {
			return nil, errW2
		}
		flags := rest[0]
		rest = rest[1:]
		var e w2entry
		e.version = flags >> 6
		if idx := int(flags & 0x3F); idx == 63 {
			if len(rest) < 4 {
				return nil, errW2
			}
			e.tag = string(rest[:4])
			rest = rest[4:]
		} else {
			e.tag = woff2KnownTags[idx]
		}
		var err error
		if e.origLen, rest, err = readBase128(rest); err != nil {
			return nil, err
		}
		size := e.origLen
		if e.transformed() {
			if size, rest, err = readBase128(rest); err != nil {
				return nil, err
			}
		}
		total += uint64(size)
		if total > 4*maxFontSize {
			return nil, errW2
		}
		e.data = make([]byte, size)
		entries = append(entries, e)
	}
	if len(rest) < compSize {
		return nil, errW2
	}
	br := brotli.NewReader(bytes.NewReader(rest[:compSize]))
	for i := range entries {
		if _, err := io.ReadFull(br, entries[i].data); err != nil {
			return nil, errW2
		}
	}
	return entries, nil
}

// encodeWOFF2 assembles a WOFF2 file. Tables are ordered by tag with loca
// directly after glyf, as the specification requires.
func encodeWOFF2(src *source, entries []w2entry, sfntSize uint32, quality int) ([]byte, error) {
	entries = slices.Clone(entries)
	slices.SortFunc(entries, func(a, b w2entry) int { return strings.Compare(a.tag, b.tag) })
	if g := slices.IndexFunc(entries, func(e w2entry) bool { return e.tag == "glyf" }); g >= 0 {
		if l := slices.IndexFunc(entries, func(e w2entry) bool { return e.tag == "loca" }); l >= 0 {
			loca := entries[l]
			entries = slices.Delete(entries, l, l+1)
			g = slices.IndexFunc(entries, func(e w2entry) bool { return e.tag == "glyf" })
			entries = slices.Insert(entries, g+1, loca)
		}
	}

	dir := make([]byte, 0, 8*len(entries))
	for i := range entries {
		e := &entries[i]
		idx := slices.Index(woff2KnownTags, e.tag)
		if idx < 0 {
			idx = 63
		}
		dir = append(dir, e.version<<6|byte(idx))
		if idx == 63 {
			dir = append(dir, e.tag...)
		}
		dir = appendBase128(dir, e.origLen)
		if e.transformed() {
			dir = appendBase128(dir, uint32(len(e.data)))
		}
	}

	var comp bytes.Buffer
	bw := brotli.NewWriterOptions(&comp, brotli.WriterOptions{Quality: quality, LGWin: 22})
	for i := range entries {
		if _, err := bw.Write(entries[i].data); err != nil {
			return nil, err
		}
	}
	if err := bw.Close(); err != nil {
		return nil, err
	}

	var meta bytes.Buffer
	if len(src.meta) > 0 {
		mw := brotli.NewWriterOptions(&meta, brotli.WriterOptions{Quality: quality, LGWin: 22})
		if _, err := mw.Write(src.meta); err != nil {
			return nil, err
		}
		if err := mw.Close(); err != nil {
			return nil, err
		}
	}

	pad4 := func(b []byte) []byte {
		for len(b)%4 != 0 {
			b = append(b, 0)
		}
		return b
	}
	out := make([]byte, 48, 48+len(dir)+comp.Len()+meta.Len()+len(src.priv)+8)
	out = append(out, dir...)
	out = append(out, comp.Bytes()...)
	out = pad4(out)

	var metaOff, metaLen, metaOrig, privOff, privLen uint32
	if meta.Len() > 0 {
		metaOff, metaLen, metaOrig = uint32(len(out)), uint32(meta.Len()), uint32(len(src.meta))
		out = append(out, meta.Bytes()...)
		if len(src.priv) > 0 {
			out = pad4(out)
		}
	}
	if len(src.priv) > 0 {
		privOff, privLen = uint32(len(out)), uint32(len(src.priv))
		out = append(out, src.priv...)
	}

	be := binary.BigEndian
	copy(out, "wOF2")
	copy(out[4:], src.flavor)
	be.PutUint32(out[8:], uint32(len(out)))
	be.PutUint16(out[12:], uint16(len(entries)))
	be.PutUint32(out[16:], sfntSize)
	be.PutUint32(out[20:], uint32(comp.Len()))
	be.PutUint16(out[24:], 1)
	be.PutUint32(out[28:], metaOff)
	be.PutUint32(out[32:], metaLen)
	be.PutUint32(out[36:], metaOrig)
	be.PutUint32(out[40:], privOff)
	be.PutUint32(out[44:], privLen)
	return out, nil
}
