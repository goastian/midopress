package fonts

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tdewolff/font"
)

var testFonts = []string{
	"AnonymousPro-Regular.ttf",       // hinted TrueType, bitmap strikes, hdmx, VDMX, LTSH
	"KarlaTamilInclined-Regular.ttf", // kern + GPOS
	"Comfortaa-VF.ttf",               // variable (fvar/gvar), DSIG
	"SourceSans3-Regular-Latin.otf",  // CFF
	"PressStart2P-Regular.woff",      // WOFF1 with DSIG
}

func readFont(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return b
}

func brotliSize(t testing.TB, b []byte) int {
	t.Helper()
	var buf bytes.Buffer
	w := brotli.NewWriterLevel(&buf, 11)
	_, err := w.Write(b)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Len()
}

// toSFNT returns the SFNT bytes of a TTF, OTF or WOFF1 font using the font
// library, independently of this package's own table parsing.
func toSFNT(t testing.TB, raw []byte) []byte {
	t.Helper()
	b, err := font.ToSFNT(raw)
	require.NoError(t, err)
	return b
}

func TestToWOFF2Fonts(t *testing.T) {
	if raceEnabled {
		// Brotli is about ten times slower under the race detector.
		withQuality(t, 6)
	}
	for _, name := range testFonts {
		t.Run(name, func(t *testing.T) {
			raw := readFont(t, name)
			out, err := ToWOFF2(raw)
			require.NoError(t, err)
			assert.Equal(t, KindWOFF2, Sniff(out))
			assert.Less(t, len(out), len(raw))
			if raceEnabled {
				t.Logf("original=%d woff2=%d (brotli11 skipped under -race)", len(raw), len(out))
			} else {
				t.Logf("original=%d brotli11(original)=%d woff2=%d (%.1f%% of original)",
					len(raw), brotliSize(t, raw), len(out), 100*float64(len(out))/float64(len(raw)))
			}

			origSFNT := toSFNT(t, raw)
			decSFNT, err := font.ParseWOFF2(out)
			require.NoError(t, err)
			assertSameTables(t, origSFNT, decSFNT)

			orig, err := font.ParseSFNT(origSFNT, 0)
			require.NoError(t, err)
			dec, err := font.ParseSFNT(decSFNT, 0)
			require.NoError(t, err)
			require.Equal(t, orig.NumGlyphs(), dec.NumGlyphs())
			for r := rune(0x20); r < 0x7F; r++ {
				gi := orig.GlyphIndex(r)
				require.Equal(t, gi, dec.GlyphIndex(r), "glyph index of %q", r)
				require.Equal(t, orig.GlyphAdvance(gi), dec.GlyphAdvance(gi), "advance of %q", r)
			}
			if orig.IsTrueType {
				for id := range orig.NumGlyphs() {
					a, errA := orig.Glyf.Contour(id)
					b, errB := dec.Glyf.Contour(id)
					require.Equal(t, errA == nil, errB == nil)
					require.True(t, reflect.DeepEqual(a, b), "outline of glyph %d", id)
				}
			}
		})
	}
}

// assertSameTables checks that dec contains every table of orig, byte for
// byte, except for the changes WOFF2 mandates (see ToWOFF2). glyf and loca
// are compared per glyph after decoding, so that padding and flag packing
// differences are ignored.
func assertSameTables(t *testing.T, orig, dec []byte) {
	t.Helper()
	a, err := parseSFNTTables(orig)
	require.NoError(t, err)
	b, err := parseSFNTTables(dec)
	require.NoError(t, err)

	want := map[string][]byte{}
	for _, tb := range a.tables {
		if tb.tag != "DSIG" {
			want[tb.tag] = tb.data
		}
	}
	require.Len(t, b.tables, len(want))
	for _, tb := range b.tables {
		o, ok := want[tb.tag]
		require.True(t, ok, "unexpected table %q", tb.tag)
		switch tb.tag {
		case "head":
			require.True(t, headEqual(o, tb.data))
			assert.Equal(t, uint16(0x0800), binary.BigEndian.Uint16(tb.data[16:])&0x0800)
		case "glyf", "loca":
		default:
			assert.True(t, bytes.Equal(o, tb.data), "table %q differs", tb.tag)
		}
	}
	if want["glyf"] != nil {
		assert.True(t, glyfEquivalent(want["head"], want["maxp"], want["glyf"], want["loca"], b.find("glyf"), b.find("loca")))
	}
}

func TestToWOFF2Kinds(t *testing.T) {
	withQuality(t, 4)
	ttf := readFont(t, "AnonymousPro-Regular.ttf")
	out, err := ToWOFF2(ttf)
	require.NoError(t, err)

	for name, in := range map[string][]byte{
		"woff2":      out,
		"nil":        nil,
		"empty":      {},
		"text":       []byte("<html>not a font</html>"),
		"collection": append([]byte("ttcf\x00\x01\x00\x00\x00\x00\x00\x00"), make([]byte, 100)...),
		"eot":        eotHeader(),
	} {
		_, err := ToWOFF2(in)
		assert.ErrorIs(t, err, ErrNotConvertible, name)
	}
}

func eotHeader() []byte {
	b := make([]byte, 120)
	binary.LittleEndian.PutUint32(b, uint32(len(b)))
	binary.LittleEndian.PutUint32(b[8:], 0x00020001)
	b[34], b[35] = 0x4C, 0x50
	return b
}

func TestSniff(t *testing.T) {
	assert.Equal(t, KindTTF, Sniff(readFont(t, "AnonymousPro-Regular.ttf")))
	assert.Equal(t, KindOTF, Sniff(readFont(t, "SourceSans3-Regular-Latin.otf")))
	assert.Equal(t, KindWOFF, Sniff(readFont(t, "PressStart2P-Regular.woff")))
	assert.Equal(t, KindTTF, Sniff([]byte("true\x00\x00\x00\x00")))
	assert.Equal(t, KindWOFF2, Sniff([]byte("wOF2....")))
	assert.Equal(t, KindCollection, Sniff([]byte("ttcf....")))
	assert.Equal(t, KindEOT, Sniff(eotHeader()))
	assert.Equal(t, KindUnknown, Sniff(nil))
	assert.Equal(t, KindUnknown, Sniff([]byte("abc")))
	assert.Equal(t, KindUnknown, Sniff([]byte("<!DOCTYPE html>")))
}

func TestConvertible(t *testing.T) {
	for _, mt := range []string{
		"font/ttf", "font/otf", "font/sfnt", "application/x-font-ttf", "application/x-font-opentype",
		"application/font-sfnt", "application/x-font-truetype", "application/font-woff", "font/woff",
		"application/x-font-woff", "Font/TTF", " font/otf ", "font/ttf; charset=binary",
	} {
		assert.True(t, Convertible(mt), mt)
	}
	for _, mt := range []string{"", "font/woff2", "text/css", "application/octet-stream", "image/png", "application/vnd.ms-fontobject", "font/collection"} {
		assert.False(t, Convertible(mt), mt)
	}
	assert.Equal(t, "font/woff2", MediaTypeWOFF2)
}

// Verifying with a transform that the font library gets wrong must fall back
// to storing the tables untransformed rather than altering the font.
func TestFallbackWhenTransformIsLossy(t *testing.T) {
	raw := readFont(t, "AnonymousPro-Regular.ttf")
	src, err := parseSFNTTables(raw)
	require.NoError(t, err)
	good, err := encodeWOFF2(src, rawEntries(src), uint32(len(raw)), 5)
	require.NoError(t, err)
	assert.True(t, verify(src, good))

	// Corrupt one glyf byte inside the stored stream: verification must fail.
	entries := rawEntries(src)
	for i := range entries {
		if entries[i].tag == "glyf" {
			d := bytes.Clone(entries[i].data)
			d[len(d)/2] ^= 0xFF
			entries[i].data = d
		}
	}
	bad, err := encodeWOFF2(src, entries, uint32(len(raw)), 5)
	require.NoError(t, err)
	assert.False(t, verify(src, bad))
}

func TestWOFF1MetadataAndPrivateCarriedOver(t *testing.T) {
	withQuality(t, 4)
	sfnt := readFont(t, "AnonymousPro-Regular.ttf")
	meta := []byte(`<?xml version="1.0"?><metadata version="1.0"><uniqueid id="x"/></metadata>`)
	priv := []byte("private-data!")
	woff := buildWOFF1(t, sfnt, meta, priv)

	out, err := ToWOFF2(woff)
	require.NoError(t, err)

	be := binary.BigEndian
	metaOff, metaLen, metaOrig := be.Uint32(out[28:]), be.Uint32(out[32:]), be.Uint32(out[36:])
	privOff, privLen := be.Uint32(out[40:]), be.Uint32(out[44:])
	require.NotZero(t, metaLen)
	require.EqualValues(t, len(meta), metaOrig)
	var got bytes.Buffer
	_, err = got.ReadFrom(brotli.NewReader(bytes.NewReader(out[metaOff : metaOff+metaLen])))
	require.NoError(t, err)
	assert.Equal(t, meta, got.Bytes())
	assert.Equal(t, priv, out[privOff:privOff+privLen])
	assert.Zero(t, metaOff%4)
	assert.Zero(t, privOff%4)
	assert.EqualValues(t, len(out), be.Uint32(out[8:]))

	dec, err := font.ParseWOFF2(out)
	require.NoError(t, err)
	assertSameTables(t, sfnt, dec)
}

// buildWOFF1 hand-builds a WOFF1 file from an SFNT, with optional metadata and
// private blocks.
func buildWOFF1(t testing.TB, sfnt, meta, priv []byte) []byte {
	t.Helper()
	src, err := parseSFNTTables(sfnt)
	require.NoError(t, err)
	deflate := func(b []byte) []byte {
		var buf bytes.Buffer
		w := zlib.NewWriter(&buf)
		_, _ = w.Write(b)
		require.NoError(t, w.Close())
		return buf.Bytes()
	}
	type rec struct {
		tag               string
		comp              []byte
		origLen, checksum uint32
	}
	var recs []rec
	for _, tb := range src.tables {
		comp := deflate(tb.data)
		if len(comp) >= len(tb.data) {
			comp = tb.data
		}
		recs = append(recs, rec{tb.tag, comp, uint32(len(tb.data)), checksum(tb.data)})
	}
	for i := range recs {
		for j := i + 1; j < len(recs); j++ {
			if recs[j].tag < recs[i].tag {
				recs[i], recs[j] = recs[j], recs[i]
			}
		}
	}
	be := binary.BigEndian
	n := len(recs)
	out := make([]byte, 44+20*n)
	pad := func() {
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
	}
	sfntSize := 12 + 16*n
	for i, r := range recs {
		pad()
		be.PutUint32(out[44+20*i+4:], uint32(len(out)))
		out = append(out, r.comp...)
		be.PutUint32(out[44+20*i+8:], uint32(len(r.comp)))
		be.PutUint32(out[44+20*i+12:], r.origLen)
		be.PutUint32(out[44+20*i+16:], r.checksum)
		copy(out[44+20*i:], r.tag)
		sfntSize += int(r.origLen+3) &^ 3
	}
	var metaOff, metaLen, privOff uint32
	if len(meta) > 0 {
		pad()
		metaOff = uint32(len(out))
		c := deflate(meta)
		metaLen = uint32(len(c))
		out = append(out, c...)
	}
	if len(priv) > 0 {
		pad()
		privOff = uint32(len(out))
		out = append(out, priv...)
	}
	copy(out, "wOFF")
	copy(out[4:], src.flavor)
	be.PutUint32(out[8:], uint32(len(out)))
	be.PutUint16(out[12:], uint16(n))
	be.PutUint32(out[16:], uint32(sfntSize))
	be.PutUint32(out[24:], metaOff)
	be.PutUint32(out[28:], metaLen)
	be.PutUint32(out[32:], uint32(len(meta)))
	be.PutUint32(out[36:], privOff)
	be.PutUint32(out[40:], uint32(len(priv)))
	return out
}

func BenchmarkToWOFF2(b *testing.B) {
	for _, name := range testFonts {
		b.Run(name, func(b *testing.B) {
			raw := readFont(b, name)
			var size int
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			for b.Loop() {
				out, err := ToWOFF2(raw)
				if err != nil {
					b.Fatal(err)
				}
				size = len(out)
			}
			b.ReportMetric(float64(len(raw)), "in-bytes")
			b.ReportMetric(float64(size), "out-bytes")
			b.ReportMetric(100*float64(size)/float64(len(raw)), "out/in-%")
		})
	}
}
