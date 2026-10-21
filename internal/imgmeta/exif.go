package imgmeta

import "encoding/binary"

const tagOrientation = 0x0112

var exifPrefix = []byte("Exif\x00")

// ParseOrientation extracts the orientation (1..8) from the payload of an APP1
// EXIF segment ("Exif\0\0" followed by a TIFF structure). It returns 1 on any
// problem or when the tag is absent.
func ParseOrientation(exif []byte) int {
	if len(exif) < 6 || string(exif[:5]) != string(exifPrefix) {
		return 1
	}
	return tiffOrientation(exif[6:])
}

// tiffOrientation reads the orientation from IFD0 of a bare TIFF structure.
func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:4]) {
	case "II*\x00":
		bo = binary.LittleEndian
	case "MM\x00*":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := uint64(bo.Uint32(t[4:]))
	if off < 8 || off+2 > uint64(len(t)) {
		return 1
	}
	n := uint64(bo.Uint16(t[off:]))
	pos := off + 2
	if avail := (uint64(len(t)) - pos) / 12; n > avail {
		n = avail
	}
	for i := uint64(0); i < n; i++ {
		e := t[pos+i*12 : pos+i*12+12]
		if bo.Uint16(e) != tagOrientation {
			continue
		}
		if bo.Uint16(e[2:]) != 3 || bo.Uint32(e[4:]) != 1 {
			return 1
		}
		if v := int(bo.Uint16(e[8:])); v >= 1 && v <= 8 {
			return v
		}
		return 1
	}
	return 1
}

const minimalEXIFSize = 36

// minimalEXIF returns an APP1 segment (marker included) holding an EXIF block
// with the orientation tag only.
func minimalEXIF(orientation int) [minimalEXIFSize]byte {
	return [minimalEXIFSize]byte{
		0xFF, 0xE1, 0x00, 0x22,
		'E', 'x', 'i', 'f', 0, 0,
		'M', 'M', 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08,
		0x00, 0x01,
		0x01, 0x12, 0x00, 0x03, 0x00, 0x00, 0x00, 0x01, 0x00, byte(orientation), 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
	}
}
