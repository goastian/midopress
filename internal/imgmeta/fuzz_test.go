package imgmeta

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func jpegSeeds(t testing.TB) [][]byte {
	prog, err := os.ReadFile(filepath.Join("testdata", "progressive.jpg"))
	require.NoError(t, err)
	segs := [][]byte{
		jfifSeg(),
		jseg(0xE1, exifPayload(binary.LittleEndian, 6)),
		jseg(0xE1, []byte("http://ns.adobe.com/xap/1.0/\x00<x/>")),
		jseg(0xFE, []byte("c")),
		jseg(0xEE, []byte("Adobe\x00\x64\x00\x00\x00\x00\x01")),
	}
	segs = append(segs, iccChunkSegs(srgbProfile(), 120)...)
	return [][]byte{testJPEG(t), testGrayJPEG(t), prog, inject(testJPEG(t), segs...), {0xFF, 0xD8, 0xFF, 0xD9}}
}

func FuzzParseJPEG(f *testing.F) {
	for _, s := range jpegSeeds(f) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		j, err := ParseJPEG(raw)
		if err != nil {
			return
		}
		if j.Orientation < 1 || j.Orientation > 8 || j.MetadataBytes < 0 || j.MetadataBytes > len(raw) || len(j.ICC) > len(raw) {
			t.Fatalf("inconsistent result %+v", j)
		}
	})
}

func FuzzStripJPEG(f *testing.F) {
	for _, s := range jpegSeeds(f) {
		f.Add(s, uint8(7))
	}
	f.Fuzz(func(t *testing.T, raw []byte, flags uint8) {
		opts := StripOptions{KeepICC: flags&1 != 0, KeepOrientation: flags&2 != 0, KeepJFIF: flags&4 != 0}
		out, err := StripJPEG(raw, opts)
		if err != nil {
			if _, perr := ParseJPEG(raw); perr == nil {
				t.Fatalf("ParseJPEG accepts what StripJPEG rejects: %v", err)
			}
			return
		}
		if len(out) > len(raw)+minimalEXIFSize || !bytes.HasPrefix(out, []byte{0xFF, 0xD8}) {
			t.Fatalf("bad output length %d for input %d", len(out), len(raw))
		}
		in, perr := ParseJPEG(raw)
		res, rerr := ParseJPEG(out)
		if perr == nil && rerr != nil {
			t.Fatalf("stripped output no longer parses: %v", rerr)
		}
		if perr == nil {
			if res.Width != in.Width || res.Height != in.Height || res.Components != in.Components || res.Progressive != in.Progressive {
				t.Fatalf("frame changed: %+v -> %+v", in, res)
			}
			if opts.KeepOrientation && res.Orientation != in.Orientation {
				t.Fatalf("orientation lost: %d -> %d", in.Orientation, res.Orientation)
			}
			if opts.KeepICC && !bytes.Equal(res.ICC, in.ICC) {
				t.Fatal("ICC changed")
			}
		}
		again, err := StripJPEG(out, opts)
		if err != nil || !bytes.Equal(again, out) {
			t.Fatalf("not idempotent: %v", err)
		}
	})
}

func FuzzParsePNG(f *testing.F) {
	base := testPNG(f)
	f.Add(base)
	f.Add(injectPNG(base, pngChunkBytes("gAMA", be32(45455)), pngChunkBytes("tEXt", []byte("a\x00b")), iccpChunk(adobeProfile()),
		pngChunkBytes("acTL", make([]byte, 8)), pngChunkBytes("eXIf", exifPayload(binary.BigEndian, 3)[6:]), pngChunkBytes("cICP", []byte{1, 13, 0, 1})))
	f.Fuzz(func(t *testing.T, raw []byte) {
		p, err := ParsePNG(raw)
		out, serr := StripPNG(raw)
		if err != nil {
			return
		}
		if serr != nil {
			t.Fatalf("ParsePNG accepts what StripPNG rejects: %v", serr)
		}
		if len(out) != len(raw)-p.AncillaryBytes {
			t.Fatalf("AncillaryBytes %d does not match %d -> %d", p.AncillaryBytes, len(raw), len(out))
		}
		q, err := ParsePNG(out)
		if err != nil {
			t.Fatalf("stripped output no longer parses: %v", err)
		}
		if q.Width != p.Width || q.Animated != p.Animated || q.HasPalette != p.HasPalette || q.HasTransparency != p.HasTransparency ||
			!bytes.Equal(q.ICC, p.ICC) || q.HasGamma != p.HasGamma || q.NeedsColorManagement() != p.NeedsColorManagement() {
			t.Fatalf("color-relevant data changed: %+v -> %+v", p, q)
		}
		_ = p.NeedsColorManagement()
	})
}

func FuzzClassifyICC(f *testing.F) {
	f.Add(srgbProfile())
	f.Add(adobeProfile())
	f.Add(buildProfile(profileSpec{space: "GRAY", desc: "g", trc: paraSRGB}))
	f.Add(buildProfile(profileSpec{space: "RGB ", desc: "sRGB", mluc: true, prim: primSRGB, trc: func() []byte { return curvTableSRGB(64) }}))
	f.Fuzz(func(t *testing.T, profile []byte) {
		c := ClassifyICC(profile)
		if c < ICCNone || c > ICCInvalid || (len(profile) == 0) != (c == ICCNone) {
			t.Fatalf("bad class %d for %d bytes", c, len(profile))
		}
	})
}
