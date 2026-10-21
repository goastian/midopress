package imgmeta

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyICC(t *testing.T) {
	linear := func() []byte { return curvLinear() }
	gamma := func(g float64) func() []byte { return func() []byte { return curvGamma(g) } }
	cases := []struct {
		name string
		prof []byte
		want ICCClass
	}{
		{"nil", nil, ICCNone},
		{"empty", []byte{}, ICCNone},
		{"srgb para v2 desc", srgbProfile(), ICCsRGB},
		{"srgb mluc", buildProfile(profileSpec{space: "RGB ", desc: "sRGB built-in", mluc: true, prim: primSRGB, trc: paraSRGB}), ICCsRGB},
		{"srgb curve table", buildProfile(profileSpec{space: "RGB ", desc: "sRGB IEC61966-2.1", prim: primSRGB, trc: func() []byte { return curvTableSRGB(1024) }}), ICCsRGB},
		{"srgb gamma 2.2", buildProfile(profileSpec{space: "RGB ", desc: "sRGB 2.2", prim: primSRGB, trc: gamma(2.2)}), ICCsRGB},
		{"srgb without wtpt", buildProfile(profileSpec{space: "RGB ", desc: "sRGB", prim: primSRGB, trc: paraSRGB, noWhite: true}), ICCsRGB},
		{"adobe rgb", adobeProfile(), ICCOther},
		{"named srgb with adobe primaries", buildProfile(profileSpec{space: "RGB ", desc: "sRGB IEC61966-2.1", prim: primAdobe, trc: paraSRGB}), ICCOther},
		{"srgb primaries gamma 1.8", buildProfile(profileSpec{space: "RGB ", desc: "sRGB", prim: primSRGB, trc: gamma(1.8)}), ICCOther},
		{"srgb primaries linear", buildProfile(profileSpec{space: "RGB ", desc: "sRGB", prim: primSRGB, trc: linear}), ICCOther},
		{"srgb primaries and curves under another name", buildProfile(profileSpec{space: "RGB ", desc: "c2", prim: primSRGB, trc: paraSRGB}), ICCsRGB},
		{"srgb with LUT", buildProfile(profileSpec{space: "RGB ", desc: "sRGB v4 preference", prim: primSRGB, trc: paraSRGB, a2b0: true}), ICCOther},
		{"gray 2.2", buildProfile(profileSpec{space: "GRAY", desc: "Gray Gamma 2.2", trc: gamma(2.2)}), ICCGray},
		{"gray srgb curve", buildProfile(profileSpec{space: "GRAY", desc: "sGray", trc: paraSRGB}), ICCGray},
		{"gray table", buildProfile(profileSpec{space: "GRAY", desc: "sGray", trc: func() []byte { return curvTableSRGB(256) }}), ICCGray},
		{"gray 1.8", buildProfile(profileSpec{space: "GRAY", desc: "Gray 1.8", trc: gamma(1.8)}), ICCOther},
		{"gray linear", buildProfile(profileSpec{space: "GRAY", desc: "Linear", trc: linear}), ICCOther},
		{"gray no trc", buildProfile(profileSpec{space: "GRAY", desc: "Gray"}), ICCOther},
		{"cmyk", buildProfile(profileSpec{space: "CMYK", desc: "SWOP"}), ICCOther},
		{"garbage", make([]byte, 300), ICCInvalid},
		{"short", []byte("acsp"), ICCInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ClassifyICC(tc.prof))
		})
	}
}

func TestClassifyICCMalformed(t *testing.T) {
	good := srgbProfile()
	for n := range len(good) {
		assert.NotPanics(t, func() { ClassifyICC(good[:n]) })
	}
	for i := 128; i < len(good); i++ {
		mut := append([]byte{}, good...)
		mut[i] ^= 0xFF
		assert.NotPanics(t, func() { ClassifyICC(mut) })
	}
	big := append([]byte{}, good...)
	big[128], big[129], big[130], big[131] = 0xFF, 0xFF, 0xFF, 0xFF
	assert.Equal(t, ICCInvalid, ClassifyICC(big))

	// a damaged description does not matter any more: sRGB is told by the
	// colorants and the curves
	oobDesc := append([]byte{}, good...)
	oobDesc[128+4+4], oobDesc[128+4+5] = 0xFF, 0xFF
	assert.NotEqual(t, ICCInvalid, ClassifyICC(oobDesc))

	// but a damaged colorant makes the profile unreadable, so it is kept as is
	oobColorant := append([]byte{}, good...)
	n := int(binary.BigEndian.Uint32(good[128:]))
	found := false
	for i := range n {
		entry := 128 + 4 + i*12
		if string(good[entry:entry+4]) == "rXYZ" {
			oobColorant[entry+4], oobColorant[entry+5] = 0xFF, 0xFF
			found = true
		}
	}
	require.True(t, found, "the test profile must have an rXYZ tag")
	assert.Equal(t, ICCOther, ClassifyICC(oobColorant))

	sized := append([]byte{}, good...)
	sized[0], sized[1], sized[2], sized[3] = 0xFF, 0, 0, 0
	assert.Equal(t, ICCInvalid, ClassifyICC(sized))
}

func TestClassifyICCSystemProfiles(t *testing.T) {
	dir := "/usr/share/ghostscript"
	matches, _ := filepath.Glob(filepath.Join(dir, "*", "iccprofiles", "*.icc"))
	if len(matches) == 0 {
		t.Skip("no system ICC profiles")
	}
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		c := ClassifyICC(b)
		t.Logf("%s: %d", filepath.Base(m), c)
		switch filepath.Base(m) {
		case "a98.icc", "default_cmyk.icc", "lab.icc", "rommrgb.icc", "scrgb.icc", "ps_gray.icc", "sgray.icc":
			assert.Equal(t, ICCOther, c, m)
		case "srgb.icc", "default_rgb.icc":
			assert.Equal(t, ICCsRGB, c, m)
		case "default_gray.icc":
			assert.Equal(t, ICCGray, c, m)
		}
	}
}
