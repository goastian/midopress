package imgmeta

import (
	"encoding/binary"
	"math"
)

// ICCClass is the coarse classification of an ICC profile.
type ICCClass int

const (
	// ICCNone means there is no profile.
	ICCNone ICCClass = iota
	// ICCsRGB is an sRGB profile: safe to drop when the target is sRGB.
	ICCsRGB
	// ICCGray is a grayscale profile with a gamma or sRGB-like tone curve.
	ICCGray
	// ICCOther is anything else (Adobe RGB, Display P3, ProPhoto, camera, CMYK,
	// unknown): it must be preserved.
	ICCOther
	// ICCInvalid is a profile whose header is malformed.
	ICCInvalid
)

const (
	iccHeaderSize = 128
	iccMaxTags    = 1 << 16
)

// iccTags is a view over a validated profile.
type iccTags struct {
	p []byte
	n int
}

func (t iccTags) find(sig string) []byte {
	for i := 0; i < t.n; i++ {
		e := t.p[iccHeaderSize+4+i*12:]
		if string(e[:4]) != sig {
			continue
		}
		off := uint64(binary.BigEndian.Uint32(e[4:]))
		size := uint64(binary.BigEndian.Uint32(e[8:]))
		if off+size > uint64(len(t.p)) || size < 8 {
			return nil
		}
		return t.p[off : off+size]
	}
	return nil
}

// ClassifyICC parses the header and the desc, colorant and tone curve
// tags of an ICC profile far enough to recognise sRGB reliably, by what the
// profile does and not by what it is called: a profile that carries the name
// "sRGB" with other primaries is ICCOther, and one with the sRGB primaries and
// curves is ICCsRGB whatever its name.
func ClassifyICC(profile []byte) ICCClass {
	if len(profile) == 0 {
		return ICCNone
	}
	if len(profile) < iccHeaderSize+4 || string(profile[36:40]) != "acsp" {
		return ICCInvalid
	}
	size := uint64(binary.BigEndian.Uint32(profile))
	if size < iccHeaderSize+4 || size > uint64(len(profile)) {
		return ICCInvalid
	}
	profile = profile[:size]
	n := uint64(binary.BigEndian.Uint32(profile[iccHeaderSize:]))
	if n > iccMaxTags || iccHeaderSize+4+n*12 > size {
		return ICCInvalid
	}
	tags := iccTags{p: profile, n: int(n)}
	switch string(profile[16:20]) {
	case "GRAY":
		if isGrayLike(tags) {
			return ICCGray
		}
	case "RGB ":
		if isSRGB(tags) {
			return ICCsRGB
		}
	}
	return ICCOther
}

func isGrayLike(t iccTags) bool {
	trc := t.find("kTRC")
	if trc == nil || t.find("A2B0") != nil {
		return false
	}
	y, ok := trcAt(trc, 0.5)
	if !ok || y <= 0 || y >= 1 {
		return false
	}
	g := math.Log(y) / math.Log(0.5)
	if g < 1.9 || g > 2.5 {
		return false
	}
	for _, x := range [...]float64{0.25, 0.75} {
		y, ok := trcAt(trc, x)
		if !ok || !(math.Abs(y-math.Pow(x, g)) <= 0.02) {
			return false
		}
	}
	return true
}

var srgbPrimaries = [...]struct {
	sig     string
	x, y, z float64
}{
	{"rXYZ", 0.4361, 0.2225, 0.0139},
	{"gXYZ", 0.3851, 0.7169, 0.0971},
	{"bXYZ", 0.1431, 0.0606, 0.7141},
}

func isSRGB(t iccTags) bool {
	// The name is not required: the compact profiles that image servers embed
	// (Clinton Ritter's "c2" ones, 524 bytes) are called just "c2". The
	// colorants and the tone curves are what make a profile sRGB.
	if t.find("A2B0") != nil {
		return false
	}
	for _, p := range srgbPrimaries {
		x, y, z, ok := xyzValue(t.find(p.sig))
		if !ok || math.Abs(x-p.x) > 0.005 || math.Abs(y-p.y) > 0.005 || math.Abs(z-p.z) > 0.005 {
			return false
		}
	}
	for _, sig := range [...]string{"rTRC", "gTRC", "bTRC"} {
		trc := t.find(sig)
		if trc == nil {
			return false
		}
		for x := 0.05; x < 1; x += 0.1 {
			y, ok := trcAt(trc, x)
			if !ok || !(math.Abs(y-srgbDecode(x)) <= 0.01) {
				return false
			}
		}
	}
	return true
}

func srgbDecode(x float64) float64 {
	if x <= 0.04045 {
		return x / 12.92
	}
	return math.Pow((x+0.055)/1.055, 2.4)
}

func s15f16(b []byte) float64 {
	return float64(int32(binary.BigEndian.Uint32(b))) / 65536
}

func xyzValue(tag []byte) (x, y, z float64, ok bool) {
	if len(tag) < 20 || string(tag[:4]) != "XYZ " {
		return 0, 0, 0, false
	}
	return s15f16(tag[8:]), s15f16(tag[12:]), s15f16(tag[16:]), true
}

// trcAt evaluates a 'curv' or 'para' tone reproduction curve at x in [0,1].
func trcAt(tag []byte, x float64) (float64, bool) {
	if len(tag) < 12 {
		return 0, false
	}
	switch string(tag[:4]) {
	case "curv":
		n := uint64(binary.BigEndian.Uint32(tag[8:]))
		switch {
		case n == 0:
			return x, true
		case n == 1:
			if len(tag) < 14 {
				return 0, false
			}
			g := float64(binary.BigEndian.Uint16(tag[12:])) / 256
			return math.Pow(x, g), true
		case 12+2*n > uint64(len(tag)):
			return 0, false
		}
		pos := x * float64(n-1)
		i := uint64(pos)
		if i >= n-1 {
			i = n - 2
		}
		a := float64(binary.BigEndian.Uint16(tag[12+2*i:])) / 65535
		b := float64(binary.BigEndian.Uint16(tag[14+2*i:])) / 65535
		return a + (b-a)*(pos-float64(i)), true
	case "para":
		return paraAt(tag, x)
	}
	return 0, false
}

func paraAt(tag []byte, x float64) (float64, bool) {
	counts := [...]int{1, 3, 4, 5, 7}
	typ := int(binary.BigEndian.Uint16(tag[8:]))
	if typ >= len(counts) || len(tag) < 12+4*counts[typ] {
		return 0, false
	}
	var p [7]float64
	for i := 0; i < counts[typ]; i++ {
		p[i] = s15f16(tag[12+4*i:])
	}
	g, a, b, c, d, e, f := p[0], p[1], p[2], p[3], p[4], p[5], p[6]
	switch typ {
	case 0:
		return math.Pow(x, g), true
	case 1:
		if a == 0 || x < -b/a {
			return 0, true
		}
		return math.Pow(a*x+b, g), true
	case 2:
		if a == 0 || x < -b/a {
			return c, true
		}
		return math.Pow(a*x+b, g) + c, true
	case 3:
		if x < d {
			return c * x, true
		}
		return math.Pow(a*x+b, g), true
	}
	if x < d {
		return c*x + f, true
	}
	return math.Pow(a*x+b, g) + e, true
}
