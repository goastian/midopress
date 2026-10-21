package imgmeta

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func gradient(w, h int, seed uint64, noise int) *image.NRGBA {
	rng := rand.New(rand.NewPCG(seed, seed+1))
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			n := 0
			if noise > 0 {
				n = rng.IntN(noise)
			}
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(x*255/w + n),
				G: uint8(y*255/h + n),
				B: uint8((x+y)*255/(w+h) + n),
				A: 255,
			})
		}
	}
	return img
}

func encodeJPEG(t testing.TB, img image.Image, quality int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}))
	return buf.Bytes()
}

func testJPEG(t testing.TB) []byte {
	return encodeJPEG(t, gradient(64, 48, 1, 30), 80)
}

func testGrayJPEG(t testing.TB) []byte {
	g := image.NewGray(image.Rect(0, 0, 40, 40))
	for i := range g.Pix {
		g.Pix[i] = uint8(i * 7)
	}
	return encodeJPEG(t, g, 75)
}

func jseg(marker byte, payload ...[]byte) []byte {
	n := 2
	for _, p := range payload {
		n += len(p)
	}
	out := []byte{0xFF, marker, byte(n >> 8), byte(n)}
	for _, p := range payload {
		out = append(out, p...)
	}
	return out
}

// inject inserts segments right after SOI.
func inject(jpg []byte, segs ...[]byte) []byte {
	out := append([]byte{}, jpg[:2]...)
	for _, s := range segs {
		out = append(out, s...)
	}
	return append(out, jpg[2:]...)
}

func jfifSeg() []byte {
	return jseg(0xE0, []byte("JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00"))
}

// exifPayload builds "Exif\0\0" + TIFF with a Make tag, the orientation tag
// and a second IFD.
type byteOrder interface {
	binary.ByteOrder
	binary.AppendByteOrder
}

func exifPayload(bo byteOrder, orientation uint16) []byte {
	tiff := make([]byte, 8, 128)
	if bo == binary.BigEndian {
		copy(tiff, "MM\x00*")
	} else {
		copy(tiff, "II*\x00")
	}
	bo.PutUint32(tiff[4:], 8)
	entry := func(tag, typ uint16, count uint32, val uint32) []byte {
		e := make([]byte, 12)
		bo.PutUint16(e, tag)
		bo.PutUint16(e[2:], typ)
		bo.PutUint32(e[4:], count)
		if typ == 3 && count == 1 {
			bo.PutUint16(e[8:], uint16(val))
		} else {
			bo.PutUint32(e[8:], val)
		}
		return e
	}
	tiff = bo.AppendUint16(tiff, 2)
	tiff = append(tiff, entry(0x010F, 2, 4, 0x00000000)...)
	tiff = append(tiff, entry(tagOrientation, 3, 1, uint32(orientation))...)
	tiff = bo.AppendUint32(tiff, 0)
	return append([]byte("Exif\x00\x00"), tiff...)
}

func iccChunkSegs(profile []byte, chunkSize int) [][]byte {
	var parts [][]byte
	for len(profile) > 0 {
		n := min(chunkSize, len(profile))
		parts = append(parts, profile[:n])
		profile = profile[n:]
	}
	segs := make([][]byte, len(parts))
	for i, p := range parts {
		segs[i] = jseg(0xE2, []byte("ICC_PROFILE\x00"), []byte{byte(i + 1), byte(len(parts))}, p)
	}
	return segs
}

type profileSpec struct {
	space   string
	desc    string
	mluc    bool
	prim    [3][3]float64
	trc     func() []byte
	a2b0    bool
	noWhite bool
}

var (
	primSRGB  = [3][3]float64{{0.4361, 0.2225, 0.0139}, {0.3851, 0.7169, 0.0971}, {0.1431, 0.0606, 0.7141}}
	primAdobe = [3][3]float64{{0.6097, 0.3111, 0.0195}, {0.2053, 0.6257, 0.0609}, {0.1492, 0.0632, 0.7446}}
)

func s15(v float64) []byte {
	return binary.BigEndian.AppendUint32(nil, uint32(int32(v*65536)))
}

func paraSRGB() []byte {
	out := []byte("para\x00\x00\x00\x00\x00\x03\x00\x00")
	for _, v := range []float64{2.4, 1 / 1.055, 0.055 / 1.055, 1 / 12.92, 0.04045} {
		out = append(out, s15(v)...)
	}
	return out
}

func curvGamma(g float64) []byte {
	out := []byte("curv\x00\x00\x00\x00\x00\x00\x00\x01")
	return binary.BigEndian.AppendUint16(out, uint16(g*256))
}

func curvLinear() []byte { return []byte("curv\x00\x00\x00\x00\x00\x00\x00\x00") }

func curvTableSRGB(n int) []byte {
	out := []byte("curv\x00\x00\x00\x00")
	out = binary.BigEndian.AppendUint32(out, uint32(n))
	for i := range n {
		v := srgbDecode(float64(i) / float64(n-1))
		out = binary.BigEndian.AppendUint16(out, uint16(v*65535+0.5))
	}
	return out
}

func descTag(s string, mluc bool) []byte {
	if !mluc {
		out := []byte("desc\x00\x00\x00\x00")
		out = binary.BigEndian.AppendUint32(out, uint32(len(s)+1))
		out = append(out, s...)
		return append(out, 0)
	}
	out := []byte("mluc\x00\x00\x00\x00\x00\x00\x00\x01\x00\x00\x00\x0CenUS")
	var text []byte
	for i := range len(s) {
		text = append(text, 0, s[i])
	}
	out = binary.BigEndian.AppendUint32(out, uint32(len(text)))
	out = binary.BigEndian.AppendUint32(out, 28)
	return append(out, text...)
}

func xyzTag(v [3]float64) []byte {
	out := []byte("XYZ \x00\x00\x00\x00")
	for _, c := range v {
		out = append(out, s15(c)...)
	}
	return out
}

func buildProfile(sp profileSpec) []byte {
	type tag struct {
		sig  string
		data []byte
	}
	var tags []tag
	tags = append(tags, tag{"desc", descTag(sp.desc, sp.mluc)})
	if !sp.noWhite {
		tags = append(tags, tag{"wtpt", xyzTag([3]float64{0.9642, 1, 0.8249})})
	}
	if sp.space == "RGB " {
		tags = append(tags, tag{"rXYZ", xyzTag(sp.prim[0])}, tag{"gXYZ", xyzTag(sp.prim[1])}, tag{"bXYZ", xyzTag(sp.prim[2])})
		tags = append(tags, tag{"rTRC", sp.trc()}, tag{"gTRC", sp.trc()}, tag{"bTRC", sp.trc()})
	} else if sp.trc != nil {
		tags = append(tags, tag{"kTRC", sp.trc()})
	}
	if sp.a2b0 {
		tags = append(tags, tag{"A2B0", make([]byte, 32)})
	}
	off := 128 + 4 + 12*len(tags)
	table := binary.BigEndian.AppendUint32(nil, uint32(len(tags)))
	var body []byte
	for _, tg := range tags {
		table = append(table, tg.sig...)
		table = binary.BigEndian.AppendUint32(table, uint32(off+len(body)))
		table = binary.BigEndian.AppendUint32(table, uint32(len(tg.data)))
		body = append(body, tg.data...)
		for len(body)%4 != 0 {
			body = append(body, 0)
		}
	}
	hdr := make([]byte, 128)
	binary.BigEndian.PutUint32(hdr, uint32(128+len(table)+len(body)))
	binary.BigEndian.PutUint32(hdr[8:], 0x02100000)
	copy(hdr[12:], "mntr")
	copy(hdr[16:], sp.space)
	copy(hdr[20:], "XYZ ")
	copy(hdr[36:], "acsp")
	return append(append(hdr, table...), body...)
}

func srgbProfile() []byte {
	return buildProfile(profileSpec{space: "RGB ", desc: "sRGB IEC61966-2.1", prim: primSRGB, trc: paraSRGB})
}

func adobeProfile() []byte {
	return buildProfile(profileSpec{space: "RGB ", desc: "Adobe RGB (1998)", prim: primAdobe, trc: func() []byte { return curvGamma(2.2) }})
}

func samePixels(t testing.TB, a, b []byte) {
	t.Helper()
	ia, err := jpeg.Decode(bytes.NewReader(a))
	require.NoError(t, err)
	ib, err := jpeg.Decode(bytes.NewReader(b))
	require.NoError(t, err)
	require.Equal(t, ia, ib)
}

func pngChunkBytes(typ string, data []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	out = append(out, typ...)
	out = append(out, data...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(out[4:]))
}

// injectPNG inserts chunks right after IHDR.
func injectPNG(raw []byte, chunks ...[]byte) []byte {
	const ihdrEnd = 8 + 25
	out := append([]byte{}, raw[:ihdrEnd]...)
	for _, c := range chunks {
		out = append(out, c...)
	}
	return append(out, raw[ihdrEnd:]...)
}

func iccpChunk(profile []byte) []byte {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	_, _ = zw.Write(profile)
	_ = zw.Close()
	data := append([]byte("icc\x00\x00"), z.Bytes()...)
	return pngChunkBytes("iCCP", data)
}

func encodePNG(t testing.TB, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

type harImage struct {
	mime string
	body []byte
}

func harImages(t testing.TB) []harImage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "corpus", "corpus.har"))
	if err != nil {
		t.Skip("corpus.har not available")
	}
	var har struct {
		Log struct {
			Entries []struct {
				Response struct {
					Content struct {
						MimeType string `json:"mimeType"`
						Text     string `json:"text"`
						Encoding string `json:"encoding"`
					} `json:"content"`
				} `json:"response"`
			} `json:"entries"`
		} `json:"log"`
	}
	require.NoError(t, json.Unmarshal(raw, &har))
	var out []harImage
	for _, e := range har.Log.Entries {
		c := e.Response.Content
		if !strings.HasPrefix(c.MimeType, "image/") || c.Encoding != "base64" {
			continue
		}
		body, err := base64.StdEncoding.DecodeString(c.Text)
		require.NoError(t, err)
		out = append(out, harImage{mime: c.MimeType, body: body})
	}
	return out
}

func readOptional(t testing.TB, pattern string) map[string][]byte {
	t.Helper()
	files, _ := filepath.Glob(pattern)
	out := map[string][]byte{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		out[filepath.Base(f)] = b
	}
	if len(out) == 0 {
		t.Skipf("no files match %s", pattern)
	}
	return out
}
