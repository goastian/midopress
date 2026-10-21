package imgmeta

import (
	"bytes"
	"encoding/binary"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseJPEGBasics(t *testing.T) {
	j, err := ParseJPEG(testJPEG(t))
	require.NoError(t, err)
	assert.Equal(t, 64, j.Width)
	assert.Equal(t, 48, j.Height)
	assert.Equal(t, 3, j.Components)
	assert.False(t, j.Progressive)
	assert.Equal(t, 1, j.Orientation)
	assert.Nil(t, j.ICC)
	assert.False(t, j.HasEXIF || j.HasXMP || j.HasPhotoshop || j.HasComment || j.Adobe)
	assert.Equal(t, -1, j.AdobeTransform)
	assert.Zero(t, j.MetadataBytes)

	g, err := ParseJPEG(testGrayJPEG(t))
	require.NoError(t, err)
	assert.Equal(t, 1, g.Components)
	assert.Equal(t, 40, g.Width)
}

func TestParseJPEGProgressiveFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "progressive.jpg"))
	require.NoError(t, err)
	j, err := ParseJPEG(raw)
	require.NoError(t, err)
	assert.True(t, j.Progressive)
	assert.Equal(t, 48, j.Width)
	assert.Equal(t, 32, j.Height)
	out, err := StripJPEG(raw, DefaultStripOptions())
	require.NoError(t, err)
	samePixels(t, raw, out)
}

func TestParseJPEGMetadata(t *testing.T) {
	icc := srgbProfile()
	xmp := jseg(0xE1, []byte("http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta/>"))
	exif := jseg(0xE1, exifPayload(binary.LittleEndian, 6))
	com := jseg(0xFE, []byte("hello"))
	ps := jseg(0xED, []byte("Photoshop 3.0\x008BIM"))
	adobe := jseg(0xEE, []byte("Adobe\x00\x64\x00\x00\x00\x00\x01"))
	segs := [][]byte{jfifSeg(), exif, xmp, com, ps, adobe}
	segs = append(segs, iccChunkSegs(icc, 100)...)
	raw := inject(testJPEG(t), segs...)

	j, err := ParseJPEG(raw)
	require.NoError(t, err)
	assert.Equal(t, 6, j.Orientation)
	assert.Equal(t, icc, j.ICC)
	assert.True(t, j.HasEXIF)
	assert.True(t, j.HasXMP)
	assert.True(t, j.HasComment)
	assert.True(t, j.HasPhotoshop)
	assert.True(t, j.Adobe)
	assert.Equal(t, 1, j.AdobeTransform)
	assert.Equal(t, len(exif)+len(xmp)+len(com)+len(ps)-minimalEXIFSize, j.MetadataBytes)

	out, err := StripJPEG(raw, DefaultStripOptions())
	require.NoError(t, err)
	assert.Equal(t, len(raw)-j.MetadataBytes, len(out))
	samePixels(t, raw, out)
}

func TestStripJPEG(t *testing.T) {
	icc := adobeProfile()
	exif := jseg(0xE1, exifPayload(binary.BigEndian, 3))
	segs := [][]byte{
		exif,
		jfifSeg(),
		jseg(0xE1, []byte("http://ns.adobe.com/xap/1.0/\x00<x/>")),
		jseg(0xFE, []byte("c")),
		jseg(0xE0, []byte("JFXX\x00\x10thumb")),
		jseg(0xE5, []byte("FPXR\x00junk")),
		jseg(0xE2, []byte("MPF\x00junk")),
	}
	segs = append(segs, iccChunkSegs(icc, 200)...)
	raw := inject(testJPEG(t), segs...)

	t.Run("default", func(t *testing.T) {
		out, err := StripJPEG(raw, DefaultStripOptions())
		require.NoError(t, err)
		samePixels(t, raw, out)
		j, err := ParseJPEG(out)
		require.NoError(t, err)
		assert.Equal(t, 3, j.Orientation)
		assert.Equal(t, icc, j.ICC)
		assert.False(t, j.HasXMP || j.HasComment)
		assert.Zero(t, j.MetadataBytes)
		assert.Equal(t, jfifSeg(), out[2:2+len(jfifSeg())])
		again, err := StripJPEG(out, DefaultStripOptions())
		require.NoError(t, err)
		assert.Same(t, &out[0], &again[0])
		assert.Len(t, again, len(out))
	})

	t.Run("nothing kept", func(t *testing.T) {
		out, err := StripJPEG(raw, StripOptions{})
		require.NoError(t, err)
		samePixels(t, raw, out)
		j, err := ParseJPEG(out)
		require.NoError(t, err)
		assert.Equal(t, 1, j.Orientation)
		assert.Nil(t, j.ICC)
		assert.False(t, j.HasEXIF)
		assert.Equal(t, testJPEG(t), out)
	})

	t.Run("icc only", func(t *testing.T) {
		out, err := StripJPEG(raw, StripOptions{KeepICC: true})
		require.NoError(t, err)
		j, err := ParseJPEG(out)
		require.NoError(t, err)
		assert.Equal(t, icc, j.ICC)
		assert.False(t, j.HasEXIF)
		assert.NotContains(t, string(out), "JFIF")
	})

	t.Run("orientation after jfif", func(t *testing.T) {
		out, err := StripJPEG(raw, StripOptions{KeepOrientation: true, KeepJFIF: true})
		require.NoError(t, err)
		m := minimalEXIF(3)
		want := append(append([]byte{}, jfifSeg()...), m[:]...)
		assert.Equal(t, want, out[2:2+len(want)])
	})

	t.Run("no-op returns input", func(t *testing.T) {
		in := inject(testJPEG(t), jfifSeg())
		out, err := StripJPEG(in, DefaultStripOptions())
		require.NoError(t, err)
		assert.Same(t, &in[0], &out[0])
	})

	t.Run("orientation 1 writes no exif", func(t *testing.T) {
		in := inject(testJPEG(t), jseg(0xE1, exifPayload(binary.LittleEndian, 1)))
		out, err := StripJPEG(in, DefaultStripOptions())
		require.NoError(t, err)
		j, err := ParseJPEG(out)
		require.NoError(t, err)
		assert.False(t, j.HasEXIF)
	})
}

func TestStripJPEGAllOrientations(t *testing.T) {
	for o := uint16(2); o <= 8; o++ {
		in := inject(testJPEG(t), jseg(0xE1, exifPayload(binary.LittleEndian, o)), jseg(0xFE, []byte("x")))
		out, err := StripJPEG(in, DefaultStripOptions())
		require.NoError(t, err)
		j, err := ParseJPEG(out)
		require.NoError(t, err)
		assert.Equal(t, int(o), j.Orientation)
		assert.Equal(t, minimalEXIFSize, len(out)-len(testJPEG(t)))
	}
}

func TestStripJPEGFillBytes(t *testing.T) {
	base := testJPEG(t)
	in := append(append(append([]byte{}, base[:2]...), 0xFF, 0xFF), jseg(0xFE, []byte("c"))...)
	in = append(in, 0xFF)
	in = append(in, base[2:]...)
	j, err := ParseJPEG(in)
	require.NoError(t, err)
	assert.True(t, j.HasComment)
	out, err := StripJPEG(in, DefaultStripOptions())
	require.NoError(t, err)
	assert.Equal(t, base, out)
}

func TestJPEGSOFVariants(t *testing.T) {
	sof := func(m byte, nf int) []byte {
		p := []byte{8, 0, 20, 0, 30, byte(nf)}
		for i := range nf {
			p = append(p, byte(i+1), 0x11, 0)
		}
		return jseg(m, p)
	}
	for _, m := range []byte{0xC0, 0xC1, 0xC2, 0xC3, 0xC5, 0xC6, 0xC7, 0xC9, 0xCA, 0xCB, 0xCD, 0xCE, 0xCF} {
		raw := []byte{0xFF, 0xD8}
		raw = append(raw, sof(m, 4)...)
		raw = append(raw, jseg(0xDA, []byte{1, 1, 0, 0, 0x3F, 0})...)
		raw = append(raw, 0x12, 0x34, 0xFF, 0xD9)
		j, err := ParseJPEG(raw)
		require.NoError(t, err, "marker %x", m)
		assert.Equal(t, 30, j.Width)
		assert.Equal(t, 20, j.Height)
		assert.Equal(t, 4, j.Components)
		assert.Equal(t, m == 0xC2 || m == 0xC6 || m == 0xCA || m == 0xCE, j.Progressive)
		out, err := StripJPEG(raw, DefaultStripOptions())
		require.NoError(t, err)
		assert.Equal(t, raw, out)
	}
}

func TestParseJPEGAdobeAndRestart(t *testing.T) {
	raw := inject(testJPEG(t), jseg(0xEE, []byte("Adobe\x00\x64\x80\x00\x00\x00\x02")), jseg(0xDD, []byte{0, 4}), []byte{0xFF, 0xD0})
	j, err := ParseJPEG(raw)
	require.NoError(t, err)
	assert.True(t, j.Adobe)
	assert.Equal(t, 2, j.AdobeTransform)
	assert.Zero(t, j.MetadataBytes)
	out, err := StripJPEG(raw, DefaultStripOptions())
	require.NoError(t, err)
	assert.Same(t, &raw[0], &out[0])

	short := inject(testJPEG(t), jseg(0xEE, []byte("Adobe\x00\x64")))
	j, err = ParseJPEG(short)
	require.NoError(t, err)
	assert.False(t, j.Adobe)
	assert.Equal(t, -1, j.AdobeTransform)
}

func TestParseJPEGICCReassembly(t *testing.T) {
	icc := srgbProfile()
	segs := iccChunkSegs(icc, 150)
	require.GreaterOrEqual(t, len(segs), 3)
	parse := func(s ...[]byte) []byte {
		j, err := ParseJPEG(inject(testJPEG(t), s...))
		require.NoError(t, err)
		return j.ICC
	}

	assert.Equal(t, icc, parse(segs...))

	rev := make([][]byte, len(segs))
	for i, s := range segs {
		rev[len(segs)-1-i] = s
	}
	assert.Equal(t, icc, parse(rev...), "out of order")
	assert.Nil(t, parse(segs[1:]...), "missing first chunk")
	assert.Nil(t, parse(segs[:len(segs)-1]...), "missing last chunk")
	assert.Nil(t, parse(append(append([][]byte{}, segs...), segs[0])...), "duplicate chunk")

	bad := append([]byte{}, segs[1]...)
	bad[4+13] = 99
	assert.Nil(t, parse(segs[0], bad, segs[2]), "count mismatch")

	zero := append([]byte{}, segs[0]...)
	zero[4+12] = 0
	assert.Nil(t, parse(zero, segs[1], segs[2]), "sequence zero")

	assert.Equal(t, []byte{}, parse(jseg(0xE2, []byte("ICC_PROFILE\x00\x01\x01"))), "empty single chunk")
	assert.Nil(t, parse(jseg(0xE2, []byte("ICC_PROFILE\x00\x01"))), "short chunk is ignored")
}

func TestParseJPEGErrors(t *testing.T) {
	base := inject(testJPEG(t), jseg(0xE1, exifPayload(binary.LittleEndian, 6)), jfifSeg())
	sos := bytes.Index(base, []byte{0xFF, 0xDA})
	require.Positive(t, sos)
	for n := 0; n <= sos+1; n++ {
		_, err := ParseJPEG(base[:n])
		assert.Error(t, err, "prefix %d", n)
		_, err = StripJPEG(base[:n], DefaultStripOptions())
		assert.Error(t, err, "prefix %d", n)
	}
	_, err := ParseJPEG([]byte("GIF89a............"))
	assert.Error(t, err)
	_, err = ParseJPEG(nil)
	assert.Error(t, err)

	noSOF := []byte{0xFF, 0xD8, 0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9}
	_, err = ParseJPEG(noSOF)
	assert.ErrorIs(t, err, errNoSOF)
	_, err = ParseJPEG([]byte{0xFF, 0xD8, 0xFF, 0xD9})
	assert.Error(t, err)
	_, err = ParseJPEG([]byte{0xFF, 0xD8, 0x12, 0x34})
	assert.Error(t, err)
	_, err = ParseJPEG([]byte{0xFF, 0xD8, 0xFF, 0xFE, 0x00, 0x01})
	assert.Error(t, err)
	_, err = ParseJPEG([]byte{0xFF, 0xD8, 0xFF, 0x00})
	assert.Error(t, err)
}

func TestJPEGRealFiles(t *testing.T) {
	files := readOptional(t, "/tmp/opencode/img/gal*.jpg")
	_, jtErr := exec.LookPath("jpegtran")
	for name, raw := range files {
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(raw))
		require.NoError(t, err)
		j, err := ParseJPEG(raw)
		require.NoError(t, err, name)
		assert.Equal(t, cfg.Width, j.Width, name)
		assert.Equal(t, cfg.Height, j.Height, name)
		out, err := StripJPEG(raw, DefaultStripOptions())
		require.NoError(t, err, name)
		samePixels(t, raw, out)
		withMeta := inject(raw, jseg(0xE1, exifPayload(binary.LittleEndian, 6)), jseg(0xFE, []byte("c")), jseg(0xED, []byte("Photoshop 3.0\x00x")))
		stripped, err := StripJPEG(withMeta, StripOptions{KeepJFIF: true})
		require.NoError(t, err)
		assert.Equal(t, raw, stripped, name)
		t.Logf("%s: %d -> %d bytes (metadata %d, icc %d, exif %v, xmp %v)", name, len(raw), len(out), j.MetadataBytes, len(j.ICC), j.HasEXIF, j.HasXMP)
		if jtErr == nil {
			cmd := exec.Command("jpegtran", "-copy", "none")
			cmd.Stdin = bytes.NewReader(raw)
			jt, err := cmd.Output()
			require.NoError(t, err)
			samePixels(t, raw, jt)
			t.Logf("%s: jpegtran -copy none: %d bytes", name, len(jt))
		}
	}
}

func TestJPEGCorpus(t *testing.T) {
	n := 0
	for _, im := range harImages(t) {
		if im.mime != "image/jpeg" {
			continue
		}
		n++
		j, err := ParseJPEG(im.body)
		require.NoError(t, err)
		out, err := StripJPEG(im.body, DefaultStripOptions())
		require.NoError(t, err)
		samePixels(t, im.body, out)
		assert.Equal(t, len(im.body)-j.MetadataBytes, len(out))
		t.Logf("%dx%d comps=%d prog=%v meta=%d orient=%d icc=%d", j.Width, j.Height, j.Components, j.Progressive, j.MetadataBytes, j.Orientation, len(j.ICC))
	}
	assert.Positive(t, n)
}

func TestParseOrientation(t *testing.T) {
	for _, bo := range []byteOrder{binary.LittleEndian, binary.BigEndian} {
		for o := uint16(1); o <= 8; o++ {
			assert.Equal(t, int(o), ParseOrientation(exifPayload(bo, o)))
		}
		assert.Equal(t, 1, ParseOrientation(exifPayload(bo, 0)))
		assert.Equal(t, 1, ParseOrientation(exifPayload(bo, 9)))
		assert.Equal(t, 1, ParseOrientation(exifPayload(bo, 0xFFFF)))
	}
	assert.Equal(t, 1, ParseOrientation(nil))
	assert.Equal(t, 1, ParseOrientation([]byte("Exif\x00\x00")))
	assert.Equal(t, 1, ParseOrientation([]byte("Exif\x00\x00II*\x00")))
	assert.Equal(t, 1, ParseOrientation([]byte("Exif\x00\x00XX*\x00\x08\x00\x00\x00")))

	good := exifPayload(binary.LittleEndian, 6)
	for n := range len(good) {
		assert.NotPanics(t, func() { ParseOrientation(good[:n]) })
	}

	badOff := append([]byte{}, good...)
	binary.LittleEndian.PutUint32(badOff[6+4:], 0xFFFFFFFF)
	assert.Equal(t, 1, ParseOrientation(badOff))
	binary.LittleEndian.PutUint32(badOff[6+4:], 4)
	assert.Equal(t, 1, ParseOrientation(badOff))

	hugeCount := append([]byte{}, good...)
	binary.LittleEndian.PutUint16(hugeCount[6+8:], 0xFFFF)
	assert.Equal(t, 6, ParseOrientation(hugeCount))

	wrongType := append([]byte{}, good...)
	binary.LittleEndian.PutUint16(wrongType[6+8+2+12+2:], 4)
	assert.Equal(t, 1, ParseOrientation(wrongType))

	lastFound := exifPayload(binary.BigEndian, 8)
	assert.Equal(t, 8, ParseOrientation(lastFound[:len(lastFound)-4]))
}
