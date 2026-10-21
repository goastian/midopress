// Package fonts converts TrueType, OpenType and WOFF1 web fonts to WOFF2.
//
// The conversion is lossless: every font table is preserved and the result is
// verified by decoding it back before it is returned.
package fonts

import (
	"encoding/binary"
	"errors"
	"strings"
)

// ErrNotConvertible is returned when a font can not be converted safely.
var ErrNotConvertible = errors.New("fonts: font can not be converted safely")

// MediaTypeWOFF2 is the Content-Type of the converted fonts.
const MediaTypeWOFF2 = "font/woff2"

// maxFontSize is the largest input font (and the largest total table size)
// that is accepted.
const maxFontSize = 16 << 20

// Kind identifies a font container format.
type Kind int

// Font container formats recognised by Sniff.
const (
	KindUnknown Kind = iota
	KindTTF
	KindOTF
	KindWOFF
	KindWOFF2
	KindCollection
	KindEOT
)

// Sniff identifies the font container format of b by its magic number.
func Sniff(b []byte) Kind {
	if len(b) >= 4 {
		switch binary.BigEndian.Uint32(b) {
		case 0x00010000, 0x74727565: // 'true'
			return KindTTF
		case 0x4F54544F: // 'OTTO'
			return KindOTF
		case 0x774F4646: // 'wOFF'
			return KindWOFF
		case 0x774F4632: // 'wOF2'
			return KindWOFF2
		case 0x74746366: // 'ttcf'
			return KindCollection
		}
	}
	// EOT header: EOTSize(4) FontDataSize(4) Version(4) Flags(4) ... then the
	// magic number 0x504C at offset 34.
	if len(b) >= 36 && b[34] == 0x4C && b[35] == 0x50 {
		version := binary.LittleEndian.Uint32(b[8:])
		if version == 0x00010000 || version == 0x00020001 || version == 0x00020002 {
			return KindEOT
		}
	}
	return KindUnknown
}

var convertibleTypes = map[string]bool{
	"font/ttf":                    true,
	"font/otf":                    true,
	"font/sfnt":                   true,
	"application/x-font-ttf":      true,
	"application/x-font-opentype": true,
	"application/font-sfnt":       true,
	"application/x-font-truetype": true,
	"application/font-woff":       true,
	"font/woff":                   true,
	"application/x-font-woff":     true,
}

// Convertible reports whether mediaType is a font type that ToWOFF2 converts.
// Parameters, case and surrounding spaces are ignored.
func Convertible(mediaType string) bool {
	if i := strings.IndexByte(mediaType, ';'); i >= 0 {
		mediaType = mediaType[:i]
	}
	return convertibleTypes[strings.ToLower(strings.TrimSpace(mediaType))]
}
