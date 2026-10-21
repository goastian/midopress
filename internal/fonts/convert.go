package fonts

import (
	"bytes"
	"encoding/binary"

	"github.com/tdewolff/font"
)

const (
	// verifyQuality is the Brotli quality used to build candidates that are
	// only checked for losslessness.
	verifyQuality = 0
)

// forceQuality overrides outputQuality when not negative. Tests set it to
// keep fuzzing fast.
var forceQuality = -1

// outputQuality picks the Brotli quality of the returned font for an input
// of n bytes. Quality 11 is about 2% smaller than 10 but several times
// slower, which is too expensive on a busy proxy, so it is never used.
func outputQuality(n int) int {
	switch {
	case forceQuality >= 0:
		return forceQuality
	case n <= 512<<10:
		return 10
	case n <= 4<<20:
		return 8
	default:
		return 5
	}
}

// ToWOFF2 converts a TTF, OTF or WOFF1 font to WOFF2. It returns
// ErrNotConvertible for collections, EOT, already-WOFF2 input, malformed
// fonts, fonts larger than 16 MiB, or when the result is not smaller than the
// input.
//
// The output is verified before being returned: decoding it back with
// font.ParseWOFF2 yields the same tables as the input. The only differences
// are the ones WOFF2 mandates: the DSIG table is dropped, bit 11 of
// head.flags is set, head.checkSumAdjustment is recomputed, and glyf/loca
// may be re-encoded with identical glyph content.
func ToWOFF2(raw []byte) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, ErrNotConvertible
		}
	}()

	var src *source
	switch Sniff(raw) {
	case KindTTF, KindOTF:
		src, err = parseSFNTTables(raw)
	case KindWOFF:
		src, err = parseWOFFTables(raw)
	default:
		return nil, ErrNotConvertible
	}
	if err != nil {
		return nil, err
	}
	if head := src.find("head"); len(head) < 54 {
		return nil, ErrNotConvertible
	}

	norm := buildSFNT(src)
	sf, err := font.ParseSFNT(norm, 0)
	if err != nil {
		return nil, ErrNotConvertible
	}
	sfntSize := uint32(len(norm))

	var candidates [][]w2entry
	if src.find("glyf") != nil && src.find("loca") != nil {
		if entries := transformedEntries(sf); entries != nil {
			candidates = append(candidates, entries, withRawHmtx(entries, src))
		}
	}
	candidates = append(candidates, rawEntries(src))

	for _, entries := range candidates {
		trial, err := encodeWOFF2(src, entries, sfntSize, verifyQuality)
		if err != nil || !verify(src, trial) {
			continue
		}
		out, err := encodeWOFF2(src, entries, sfntSize, outputQuality(len(raw)))
		if err != nil || !verify(src, out) || len(out) >= len(raw) {
			return nil, ErrNotConvertible
		}
		return out, nil
	}
	return nil, ErrNotConvertible
}

// transformedEntries lets the font library apply the glyf and hmtx
// transforms and returns the resulting tables, or nil on failure.
func transformedEntries(sf *font.SFNT) []w2entry {
	w2, err := sf.WriteWOFF2()
	if err != nil {
		return nil
	}
	entries, err := parseW2Entries(w2)
	if err != nil {
		return nil
	}
	return entries
}

func withRawHmtx(entries []w2entry, src *source) []w2entry {
	out := make([]w2entry, len(entries))
	copy(out, entries)
	for i := range out {
		if out[i].tag == "hmtx" {
			hmtx := src.find("hmtx")
			out[i] = w2entry{tag: "hmtx", origLen: uint32(len(hmtx)), data: hmtx}
		}
	}
	return out
}

// rawEntries stores every table untransformed.
func rawEntries(src *source) []w2entry {
	entries := make([]w2entry, 0, len(src.tables))
	for _, t := range src.tables {
		if t.tag == "DSIG" {
			continue
		}
		e := w2entry{tag: t.tag, origLen: uint32(len(t.data)), data: t.data}
		if t.tag == "glyf" || t.tag == "loca" {
			e.version = 3
		}
		entries = append(entries, e)
	}
	return withHeadFlag(entries)
}

// withHeadFlag returns entries with bit 11 of head.flags set, as required of
// WOFF2 encoders.
func withHeadFlag(entries []w2entry) []w2entry {
	for i := range entries {
		if entries[i].tag != "head" {
			continue
		}
		head := bytes.Clone(entries[i].data)
		flags := binary.BigEndian.Uint16(head[16:]) | 0x0800
		binary.BigEndian.PutUint16(head[16:], flags)
		entries[i].data = head
	}
	return entries
}

// verify decodes woff2 with the font library and checks that every table of
// src is present in the result.
func verify(src *source, woff2 []byte) bool {
	dec, err := decodeWOFF2(woff2)
	if err != nil {
		return false
	}
	got, err := parseSFNTTables(dec)
	if err != nil || got.flavor != src.flavor {
		return false
	}

	want := make(map[string][]byte, len(src.tables))
	for _, t := range src.tables {
		if t.tag != "DSIG" {
			want[t.tag] = t.data
		}
	}
	if len(got.tables) != len(want) {
		return false
	}
	for _, t := range got.tables {
		orig, ok := want[t.tag]
		if !ok {
			return false
		}
		switch t.tag {
		case "head":
			if !headEqual(orig, t.data) {
				return false
			}
		case "glyf", "loca":
			// compared together below
		default:
			if !bytes.Equal(orig, t.data) {
				return false
			}
		}
	}

	if want["glyf"] != nil || want["loca"] != nil {
		if bytes.Equal(want["glyf"], got.find("glyf")) && bytes.Equal(want["loca"], got.find("loca")) {
			return true
		}
		return glyfEquivalent(want["head"], want["maxp"], want["glyf"], want["loca"], got.find("glyf"), got.find("loca"))
	}
	return true
}

// decodeWOFF2 wraps font.ParseWOFF2, which is not trusted not to panic.
func decodeWOFF2(b []byte) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, ErrNotConvertible
		}
	}()
	return font.ParseWOFF2(b)
}

// headEqual compares head tables ignoring checkSumAdjustment (recomputed by
// every decoder) and bit 11 of flags (set by WOFF2 encoders).
func headEqual(orig, dec []byte) bool {
	if len(orig) != len(dec) || len(orig) < 54 {
		return false
	}
	a, b := bytes.Clone(orig), bytes.Clone(dec)
	clear(a[8:12])
	clear(b[8:12])
	a[16] |= 0x08
	b[16] |= 0x08
	return bytes.Equal(a, b)
}
