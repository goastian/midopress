package imgmeta

import (
	"encoding/binary"
	"errors"
)

// AVIF is what the container of an AVIF file says about the picture, read from
// its boxes without decoding it.
type AVIF struct {
	Width, Height int
	// Animated is an image sequence ("avis"), which is not a still.
	Animated bool
	// Depth is the bits per channel, 8 when the file does not say.
	Depth int
	// ICC is an embedded colour profile, nil when there is none.
	ICC []byte
	// NCLX lists the colour descriptions given by code points: the primaries
	// and the transfer characteristics, as numbers of ITU-T H.273.
	NCLX []AVIFColour
	// Transformed is true when the file asks the viewer to rotate, mirror or
	// crop the picture (irot, imir, clap). A decoder gives the pixels as they
	// are coded, so recoding them would lose that.
	Transformed bool
}

// AVIFColour is a colr box of type nclx.
type AVIFColour struct {
	Primaries, Transfer, Matrix int
	FullRange                   bool
}

var errBadAVIF = errors.New("imgmeta: malformed AVIF")

type isoBox struct {
	typ  string
	body []byte
}

// isoBoxes splits a run of ISO BMFF boxes. It stops at the first one that does
// not fit.
func isoBoxes(b []byte) []isoBox {
	var out []isoBox
	for len(b) >= 8 {
		size := uint64(binary.BigEndian.Uint32(b))
		typ := string(b[4:8])
		hdr := uint64(8)
		switch size {
		case 0:
			size = uint64(len(b))
		case 1:
			if len(b) < 16 {
				return out
			}
			size = binary.BigEndian.Uint64(b[8:])
			hdr = 16
		}
		if size < hdr || size > uint64(len(b)) {
			return out
		}
		out = append(out, isoBox{typ, b[hdr:size]})
		b = b[size:]
	}
	return out
}

// ParseAVIF reads the boxes of an AVIF file that matter to recoding it.
func ParseAVIF(raw []byte) (*AVIF, error) {
	a := &AVIF{Depth: 8}
	var gotFtyp, gotSize bool
	for _, bx := range isoBoxes(raw) {
		switch bx.typ {
		case "ftyp":
			if len(bx.body) < 8 {
				return nil, errBadAVIF
			}
			gotFtyp = true
			brands := []string{string(bx.body[:4])}
			for i := 8; i+4 <= len(bx.body); i += 4 {
				brands = append(brands, string(bx.body[i:i+4]))
			}
			var still bool
			for _, b := range brands {
				switch b {
				case "avis":
					a.Animated = true
				case "avif":
					still = true
				}
			}
			if !still && !a.Animated {
				return nil, errBadAVIF
			}
		case "meta":
			if len(bx.body) < 4 {
				return nil, errBadAVIF
			}
			for _, c := range isoBoxes(bx.body[4:]) {
				if c.typ != "iprp" {
					continue
				}
				for _, p := range isoBoxes(c.body) {
					if p.typ == "ipco" {
						gotSize = a.properties(p.body) || gotSize
					}
				}
			}
		}
	}
	if !gotFtyp || !gotSize {
		return nil, errBadAVIF
	}
	return a, nil
}

// properties reads the item properties and reports whether it found the size.
// A file has properties for the picture, its alpha plane and its tiles; they
// are all read, so a colour or a transformation that belongs to any of them
// counts. The size is that of the largest picture.
func (a *AVIF) properties(ipco []byte) bool {
	var found bool
	for _, p := range isoBoxes(ipco) {
		switch p.typ {
		case "ispe":
			if len(p.body) >= 12 {
				w := int(binary.BigEndian.Uint32(p.body[4:]))
				h := int(binary.BigEndian.Uint32(p.body[8:]))
				if w > 0 && h > 0 && w*h > a.Width*a.Height {
					a.Width, a.Height = w, h
				}
				found = true
			}
		case "pixi":
			if len(p.body) >= 6 {
				n := int(p.body[4])
				for i := 0; i < n && 5+i < len(p.body); i++ {
					a.Depth = max(a.Depth, int(p.body[5+i]))
				}
			}
		case "colr":
			if len(p.body) < 4 {
				continue
			}
			switch string(p.body[:4]) {
			case "nclx":
				if len(p.body) >= 11 {
					a.NCLX = append(a.NCLX, AVIFColour{
						Primaries: int(binary.BigEndian.Uint16(p.body[4:])),
						Transfer:  int(binary.BigEndian.Uint16(p.body[6:])),
						Matrix:    int(binary.BigEndian.Uint16(p.body[8:])),
						FullRange: p.body[10]&0x80 != 0,
					})
				}
			case "rICC", "prof":
				a.ICC = p.body[4:]
			}
		case "irot", "imir", "clap":
			a.Transformed = true
		}
	}
	return found
}

// SDRsRGB reports whether the colour description is the one every browser
// shows without managing colour: BT.709 primaries, or unspecified, with a
// transfer curve of sRGB, BT.709, BT.601 or unspecified. Anything else (Display
// P3, BT.2020, PQ, HLG) needs a colour managed path that a recode would drop.
func (c AVIFColour) SDRsRGB() bool {
	if c.Primaries != 1 && c.Primaries != 2 {
		return false
	}
	switch c.Transfer {
	case 1, 2, 6, 13:
		return true
	}
	return false
}
