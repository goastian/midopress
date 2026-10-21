package imgmeta

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"math"
)

var (
	errNotPNG   = errors.New("imgmeta: not a PNG")
	errPNGChunk = errors.New("imgmeta: invalid PNG chunk")
	errPNGCRC   = errors.New("imgmeta: PNG chunk checksum mismatch")
	errPNGIHDR  = errors.New("imgmeta: invalid PNG header")
	errPNGNoDat = errors.New("imgmeta: PNG without image data")
)

const (
	pngSigLen = 8
	// maxPNGProfile bounds the inflated size of an embedded ICC profile.
	maxPNGProfile = 8 << 20
	// srgbFileGamma is the gAMA value written for sRGB images (1/2.2).
	srgbFileGamma = 0.45455
)

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// PNG describes the header-level properties of a PNG file.
type PNG struct {
	Width, Height int
	BitDepth      int
	ColorType     int
	Interlaced    bool
	// Animated reports an acTL chunk: an APNG that must not be re-encoded as a
	// still image.
	Animated                    bool
	HasPalette, HasTransparency bool
	// ICC is the inflated iCCP profile, nil when absent or unreadable.
	ICC                                            []byte
	HasSRGB, HasGamma, HasChroma, HasEXIF, HasCICP bool
	// Gamma is the decoding exponent derived from the gAMA chunk, i.e. the
	// reciprocal of the stored file gamma: 2.2 for the usual value 0.45455.
	// It is 0 when the chunk is absent.
	Gamma float64
	// Orientation is the EXIF orientation found in an eXIf chunk, 1 when
	// absent or invalid.
	Orientation int
	// AncillaryBytes is the number of bytes StripPNG removes, including any
	// data trailing IEND.
	AncillaryBytes int
}

type pngChunk struct {
	typ        []byte
	data       []byte
	start, end int
}

func nextPNGChunk(raw []byte, pos int) (pngChunk, error) {
	if pos+8 > len(raw) {
		return pngChunk{}, errTruncated
	}
	l := binary.BigEndian.Uint32(raw[pos:])
	if l > math.MaxInt32 {
		return pngChunk{}, errPNGChunk
	}
	end := pos + 12 + int(l)
	if end > len(raw) || end < pos {
		return pngChunk{}, errTruncated
	}
	return pngChunk{typ: raw[pos+4 : pos+8], data: raw[pos+8 : end-4], start: pos, end: end}, nil
}

func (c *pngChunk) checkCRC(raw []byte) error {
	if crc32.ChecksumIEEE(raw[c.start+4:c.end-4]) != binary.BigEndian.Uint32(raw[c.end-4:]) {
		return errPNGCRC
	}
	return nil
}

// keepsChunk reports whether StripPNG preserves a chunk. Critical chunks are
// always preserved.
func keepsChunk(typ []byte) bool {
	if typ[0]&0x20 == 0 {
		return true
	}
	switch string(typ) {
	case "tRNS", "acTL", "fcTL", "fdAT", "sRGB", "gAMA", "cHRM", "iCCP", "cICP", "sBIT", "mDCv", "cLLi":
		return true
	}
	return false
}

func validIHDR(p *PNG) bool {
	if p.Width < 1 || p.Height < 1 {
		return false
	}
	var depths string
	switch p.ColorType {
	case 0:
		depths = "\x01\x02\x04\x08\x10"
	case 2, 4, 6:
		depths = "\x08\x10"
	case 3:
		depths = "\x01\x02\x04\x08"
	default:
		return false
	}
	return bytes.IndexByte([]byte(depths), byte(p.BitDepth)) >= 0
}

// ParsePNG validates the signature, IHDR and chunk lengths and collects the
// color-related chunks. CRCs are verified only for the chunks it interprets,
// not for IDAT.
func ParsePNG(raw []byte) (*PNG, error) {
	if len(raw) < pngSigLen || !bytes.Equal(raw[:pngSigLen], pngSignature) {
		return nil, errNotPNG
	}
	c, err := nextPNGChunk(raw, pngSigLen)
	if err != nil {
		return nil, err
	}
	if string(c.typ) != "IHDR" || len(c.data) != 13 {
		return nil, errPNGIHDR
	}
	if err := c.checkCRC(raw); err != nil {
		return nil, err
	}
	w, h := binary.BigEndian.Uint32(c.data), binary.BigEndian.Uint32(c.data[4:])
	if w > math.MaxInt32 || h > math.MaxInt32 {
		return nil, errPNGIHDR
	}
	p := &PNG{
		Width:       int(w),
		Height:      int(h),
		BitDepth:    int(c.data[8]),
		ColorType:   int(c.data[9]),
		Interlaced:  c.data[12] == 1,
		Orientation: 1,
	}
	if !validIHDR(p) || c.data[10] != 0 || c.data[11] != 0 || c.data[12] > 1 {
		return nil, errPNGIHDR
	}
	pos := c.end
	sawData := false
	for {
		c, err := nextPNGChunk(raw, pos)
		if err != nil {
			return nil, err
		}
		pos = c.end
		if string(c.typ) == "IEND" {
			p.AncillaryBytes += len(raw) - c.end
			break
		}
		if string(c.typ) == "IDAT" {
			sawData = true
		}
		if !keepsChunk(c.typ) {
			p.AncillaryBytes += c.end - c.start
		}
		if err := p.interpret(raw, &c); err != nil {
			return nil, err
		}
	}
	if !sawData {
		return nil, errPNGNoDat
	}
	return p, nil
}

func (p *PNG) interpret(raw []byte, c *pngChunk) error {
	d := c.data
	switch string(c.typ) {
	case "PLTE":
		p.HasPalette = true
	case "tRNS":
		p.HasTransparency = true
	case "acTL":
		if len(d) == 8 {
			p.Animated = true
		}
	case "sRGB":
		if len(d) == 1 {
			p.HasSRGB = true
		}
	case "gAMA":
		if len(d) == 4 {
			if v := binary.BigEndian.Uint32(d); v != 0 {
				p.HasGamma = true
				p.Gamma = 100000 / float64(v)
			}
		}
	case "cHRM":
		if len(d) == 32 {
			p.HasChroma = true
		}
	case "cICP":
		if len(d) == 4 {
			p.HasCICP = true
		}
	case "eXIf":
		p.HasEXIF = true
		if bytes.HasPrefix(d, exifPrefix) {
			p.Orientation = ParseOrientation(d)
		} else {
			p.Orientation = tiffOrientation(d)
		}
	case "iCCP":
		if p.ICC == nil {
			p.ICC = inflateProfile(d)
		}
	default:
		return nil
	}
	return c.checkCRC(raw)
}

func inflateProfile(d []byte) []byte {
	i := bytes.IndexByte(d, 0)
	if i < 1 || i > 79 || i+2 > len(d) || d[i+1] != 0 {
		return nil
	}
	zr, err := zlib.NewReader(bytes.NewReader(d[i+2:]))
	if err != nil {
		return nil
	}
	out, err := io.ReadAll(io.LimitReader(zr, maxPNGProfile+1))
	if err != nil || len(out) > maxPNGProfile || len(out) == 0 {
		return nil
	}
	return out
}

// NeedsColorManagement reports whether decoding the PNG with a plain sRGB
// assumption would show different colors: an ICCOther profile, a gAMA value
// that differs from sRGB by more than 2% when no sRGB chunk or usable profile
// is present, or a cICP chunk.
func (p *PNG) NeedsColorManagement() bool {
	if p.HasCICP {
		return true
	}
	class := ClassifyICC(p.ICC)
	if class == ICCOther {
		return true
	}
	if class != ICCNone && class != ICCInvalid {
		return false
	}
	if p.HasGamma && !p.HasSRGB {
		fileGamma := 1 / p.Gamma
		return math.Abs(fileGamma-srgbFileGamma)/srgbFileGamma > 0.02
	}
	return false
}

// StripPNG drops text, time, eXIf, pHYs, bKGD, hIST, sPLT and every other
// ancillary chunk that affects neither colors nor decoding, as well as any data
// after IEND. IHDR, PLTE, tRNS, IDAT, IEND, acTL/fcTL/fdAT, sRGB, gAMA, cHRM,
// iCCP, cICP, sBIT, mDCv, cLLi and unknown critical chunks are kept. The input
// is returned when there is nothing to remove.
func StripPNG(raw []byte) ([]byte, error) {
	if len(raw) < pngSigLen || !bytes.Equal(raw[:pngSigLen], pngSignature) {
		return nil, errNotPNG
	}
	pos := pngSigLen
	dropped := 0
	for {
		c, err := nextPNGChunk(raw, pos)
		if err != nil {
			return nil, err
		}
		pos = c.end
		if string(c.typ) == "IEND" {
			dropped += len(raw) - c.end
			break
		}
		if !keepsChunk(c.typ) {
			dropped += c.end - c.start
		}
	}
	if dropped == 0 {
		return raw, nil
	}
	out := make([]byte, 0, len(raw)-dropped)
	out = append(out, raw[:pngSigLen]...)
	pos = pngSigLen
	for {
		c, err := nextPNGChunk(raw, pos)
		if err != nil {
			return nil, err
		}
		pos = c.end
		if keepsChunk(c.typ) {
			out = append(out, raw[c.start:c.end]...)
		}
		if string(c.typ) == "IEND" {
			return out, nil
		}
	}
}
