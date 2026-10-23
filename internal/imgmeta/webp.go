package imgmeta

import (
	"encoding/binary"
	"errors"
)

// WebP is what the container of a WebP file says about the picture, read from
// its headers without decoding it.
type WebP struct {
	Width, Height int
	Animated      bool
	Alpha         bool
	Lossless      bool
	// ICC is the embedded colour profile, nil when there is none.
	ICC []byte
	// Orientation is the EXIF orientation, 1 when there is none. Browsers do
	// not agree on whether they apply it to a WebP, so a file that carries one
	// other than 1 is not safe to decode and re-encode.
	Orientation int
}

var errBadWebP = errors.New("imgmeta: malformed WebP")

// ParseWebP reads the headers of a WebP file.
func ParseWebP(raw []byte) (*WebP, error) {
	if len(raw) < 20 || string(raw[:4]) != "RIFF" || string(raw[8:12]) != "WEBP" {
		return nil, errBadWebP
	}
	end := 8 + int(binary.LittleEndian.Uint32(raw[4:]))
	if end > len(raw) || end < 12 {
		end = len(raw)
	}
	w := &WebP{Orientation: 1}
	var gotSize bool
	for pos := 12; pos+8 <= end; {
		typ := string(raw[pos : pos+4])
		n := int(binary.LittleEndian.Uint32(raw[pos+4:]))
		body := pos + 8
		if n < 0 || body+n > end {
			return nil, errBadWebP
		}
		data := raw[body : body+n]
		switch typ {
		case "VP8X":
			if n < 10 {
				return nil, errBadWebP
			}
			flags := data[0]
			w.Animated = flags&0x02 != 0
			w.Alpha = flags&0x10 != 0
			w.Width = 1 + int(data[4]) | int(data[5])<<8 | int(data[6])<<16
			w.Height = 1 + int(data[7]) | int(data[8])<<8 | int(data[9])<<16
			gotSize = true
		case "ANIM", "ANMF":
			w.Animated = true
		case "ICCP":
			w.ICC = data
		case "EXIF":
			// the payload is a bare TIFF structure, with or without the Exif prefix
			if len(data) >= 6 && string(data[:5]) == "Exif\x00" {
				w.Orientation = tiffOrientation(data[6:])
			} else {
				w.Orientation = tiffOrientation(data)
			}
		case "VP8 ":
			w.Lossless = false
			if !gotSize {
				if n < 10 || data[3] != 0x9d || data[4] != 0x01 || data[5] != 0x2a {
					return nil, errBadWebP
				}
				w.Width = int(binary.LittleEndian.Uint16(data[6:])) & 0x3fff
				w.Height = int(binary.LittleEndian.Uint16(data[8:])) & 0x3fff
				gotSize = true
			}
		case "VP8L":
			w.Lossless = true
			w.Alpha = w.Alpha || (n >= 5 && data[4]&0x10 != 0)
			if !gotSize {
				if n < 5 || data[0] != 0x2f {
					return nil, errBadWebP
				}
				b := binary.LittleEndian.Uint32(data[1:])
				w.Width = int(b&0x3fff) + 1
				w.Height = int(b>>14&0x3fff) + 1
				gotSize = true
			}
		}
		pos = body + n + n&1
	}
	if !gotSize || w.Width < 1 || w.Height < 1 {
		return nil, errBadWebP
	}
	return w, nil
}
