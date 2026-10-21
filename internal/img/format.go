// Package img recodes images: it picks the smallest representation a client can
// display that stays visually indistinguishable from the original, and proves it
// by measuring the result.
package img

import (
	"bytes"

	"github.com/goastian/midopress/internal/negotiate"
)

// Format is an image file format.
type Format uint8

const (
	Unknown Format = iota
	JPEG
	PNG
	GIF
	WebP
	AVIF
	JXL
)

var formatNames = [...]string{"unknown", "jpeg", "png", "gif", "webp", "avif", "jxl"}

func (f Format) String() string {
	if int(f) < len(formatNames) {
		return formatNames[f]
	}
	return "unknown"
}

// MIME returns the media type of the format.
func (f Format) MIME() string {
	switch f {
	case JPEG:
		return "image/jpeg"
	case PNG:
		return "image/png"
	case GIF:
		return "image/gif"
	case WebP:
		return "image/webp"
	case AVIF:
		return "image/avif"
	case JXL:
		return "image/jxl"
	}
	return ""
}

// Sniff identifies the format from the first bytes of a file.
func Sniff(b []byte) Format {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return JPEG
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return PNG
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return GIF
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return WebP
	case len(b) >= 12 && string(b[4:8]) == "ftyp" && isAVIFBrand(b[8:12]):
		return AVIF
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0x0A:
		return JXL
	case len(b) >= 12 && bytes.Equal(b[:12], []byte{0, 0, 0, 0x0C, 'J', 'X', 'L', ' ', 0x0D, 0x0A, 0x87, 0x0A}):
		return JXL
	}
	return Unknown
}

func isAVIFBrand(b []byte) bool {
	return string(b) == "avif" || string(b) == "avis"
}

// Caps are the modern formats a client says it can display. JPEG, PNG and GIF
// are assumed everywhere.
type Caps struct {
	WebP, AVIF, JXL bool
}

// CapsFromAccept reads the Accept header of an image request. Only formats the
// client lists by name count, a wildcard does not prove support.
func CapsFromAccept(accept string) Caps {
	return Caps{
		WebP: negotiate.AcceptsMedia(accept, "image/webp"),
		AVIF: negotiate.AcceptsMedia(accept, "image/avif"),
		JXL:  negotiate.AcceptsMedia(accept, "image/jxl"),
	}
}

// Has reports whether the client can display f.
func (c Caps) Has(f Format) bool {
	switch f {
	case WebP:
		return c.WebP
	case AVIF:
		return c.AVIF
	case JXL:
		return c.JXL
	}
	return f != Unknown
}
