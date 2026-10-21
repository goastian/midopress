package fonts

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tdewolff/font"
)

const maxHostileAlloc = 128 << 20

// convertBounded runs ToWOFF2 and fails the test if it takes too long or
// allocates too much. It returns the result of the conversion.
func convertBounded(t *testing.T, name string, in []byte) ([]byte, error) {
	t.Helper()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	out, err := ToWOFF2(in)
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	assert.Less(t, elapsed, 10*time.Second, "%s took too long", name)
	assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(maxHostileAlloc), "%s allocated too much", name)
	if err == nil {
		dec, derr := font.ParseWOFF2(out)
		require.NoError(t, derr, name)
		assert.NotEmpty(t, dec)
		assert.Less(t, len(out), len(in))
	} else {
		assert.ErrorIs(t, err, ErrNotConvertible, name)
	}
	return out, err
}

func withQuality(t *testing.T, q int) {
	t.Helper()
	forceQuality = q
	t.Cleanup(func() { forceQuality = -1 })
}

func TestTruncated(t *testing.T) {
	withQuality(t, 1)
	for _, name := range testFonts {
		raw := readFont(t, name)
		for k := 0; k < 16; k++ {
			in := raw[:len(raw)*k/16]
			_, err := convertBounded(t, name, in)
			assert.Error(t, err, "%s truncated to %d/16", name, k)
		}
	}
}

func TestByteFlips(t *testing.T) {
	withQuality(t, 1)
	rng := rand.New(rand.NewSource(1))
	for _, name := range testFonts {
		raw := readFont(t, name)
		for i := 0; i < 8; i++ {
			in := bytes.Clone(raw)
			for range 1 + rng.Intn(8) {
				in[rng.Intn(len(in))] ^= byte(1 + rng.Intn(255))
			}
			_, _ = convertBounded(t, name, in)
		}
		// Flips concentrated in the headers and table directory.
		for i := 0; i < 8; i++ {
			in := bytes.Clone(raw)
			in[rng.Intn(min(len(in), 256))] ^= byte(1 + rng.Intn(255))
			_, _ = convertBounded(t, name, in)
		}
	}
}

func sfntWithDirectory(numTables int, recs ...[4]uint32) []byte {
	b := make([]byte, 12+16*len(recs)+64)
	copy(b, "\x00\x01\x00\x00")
	binary.BigEndian.PutUint16(b[4:], uint16(numTables))
	for i, r := range recs {
		copy(b[12+16*i:], "tag"+string(rune('a'+i)))
		binary.BigEndian.PutUint32(b[12+16*i+4:], r[0])
		binary.BigEndian.PutUint32(b[12+16*i+8:], r[1])
		binary.BigEndian.PutUint32(b[12+16*i+12:], r[2])
	}
	return b
}

func TestBogusDirectories(t *testing.T) {
	withQuality(t, 1)
	ttf := readFont(t, "AnonymousPro-Regular.ttf")
	mutate := func(f func(b []byte)) []byte {
		b := bytes.Clone(ttf)
		f(b)
		return b
	}
	cases := map[string][]byte{
		"numTables 0xFFFF":      mutate(func(b []byte) { binary.BigEndian.PutUint16(b[4:], 0xFFFF) }),
		"numTables 0":           mutate(func(b []byte) { binary.BigEndian.PutUint16(b[4:], 0) }),
		"numTables 0xFFFF tiny": sfntWithDirectory(0xFFFF),
		"huge length":           sfntWithDirectory(1, [4]uint32{0, 20, 0xFFFFFFFF}),
		"huge offset":           sfntWithDirectory(1, [4]uint32{0, 0xFFFFFFFF, 4}),
		"offset+length wraps":   sfntWithDirectory(1, [4]uint32{0, 0xFFFFFFF0, 0x20}),
		"all tables huge": mutate(func(b []byte) {
			n := int(binary.BigEndian.Uint16(b[4:]))
			for i := range n {
				binary.BigEndian.PutUint32(b[12+16*i+12:], 0x00FFFFFF)
			}
		}),
		"duplicate tags": mutate(func(b []byte) { copy(b[12+16:], b[12:12+4]) }),
		"overlapping tables": mutate(func(b []byte) {
			n := int(binary.BigEndian.Uint16(b[4:]))
			for i := 1; i < n; i++ {
				copy(b[12+16*i+8:], b[12+8:12+12])
			}
		}),
		"all tables same range": sfntWithDirectory(3, [4]uint32{0, 12, 8}, [4]uint32{0, 12, 8}, [4]uint32{0, 12, 8}),
		"zero length head":      sfntWithDirectory(1, [4]uint32{0, 12, 0}),
	}
	for name, in := range cases {
		_, err := convertBounded(t, name, in)
		if name != "overlapping tables" {
			assert.Error(t, err, name)
		}
	}
}

func woff1WithTable(origLen uint32, comp []byte) []byte {
	b := make([]byte, 44+20, 44+20+len(comp))
	be := binary.BigEndian
	copy(b, "wOFF\x00\x01\x00\x00")
	be.PutUint16(b[12:], 1)
	copy(b[44:], "head")
	be.PutUint32(b[48:], uint32(len(b)))
	be.PutUint32(b[52:], uint32(len(comp)))
	be.PutUint32(b[56:], origLen)
	b = append(b, comp...)
	be.PutUint32(b[8:], uint32(len(b)))
	be.PutUint32(b[16:], 12+16+(origLen+3)&^3)
	return b
}

func TestWOFF1Bombs(t *testing.T) {
	withQuality(t, 1)

	var bomb bytes.Buffer
	zw, err := zlib.NewWriterLevel(&bomb, zlib.BestCompression)
	require.NoError(t, err)
	zeros := make([]byte, 1<<20)
	for range 32 {
		_, err = zw.Write(zeros)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	require.Less(t, bomb.Len(), 1<<20)

	cases := map[string][]byte{
		"declared 0xFFFFFFFF":         woff1WithTable(0xFFFFFFFF, bomb.Bytes()),
		"declared 16MiB, real 32MiB":  woff1WithTable(16<<20, bomb.Bytes()),
		"declared 1KiB, real 32MiB":   woff1WithTable(1024, bomb.Bytes()),
		"declared 100 MiB":            woff1WithTable(100<<20, bomb.Bytes()),
		"declared 32MiB, exact":       woff1WithTable(32<<20, bomb.Bytes()),
		"declared 64, truncated zlib": woff1WithTable(64, bomb.Bytes()[:20]),
		"compressed larger than orig": woff1WithTable(4, bomb.Bytes()),
	}
	for name, in := range cases {
		_, err := convertBounded(t, name, in)
		assert.Error(t, err, name)
	}

	// Several tables, each within limits but summing past them.
	b := woff1WithTable(10<<20, bomb.Bytes())
	b2 := bytes.Clone(b)
	binary.BigEndian.PutUint16(b2[12:], 2)
	_, err = convertBounded(t, "bad table count", b2)
	assert.Error(t, err)
}

func FuzzToWOFF2(f *testing.F) {
	forceQuality = 1
	f.Cleanup(func() { forceQuality = -1 })
	for _, name := range testFonts {
		raw, err := os.ReadFile(filepath.Join("testdata", name))
		require.NoError(f, err)
		f.Add(raw)
	}
	f.Add([]byte{})
	f.Add([]byte("wOFF"))
	f.Add([]byte("\x00\x01\x00\x00\x00\x01"))
	f.Fuzz(func(t *testing.T, in []byte) {
		out, err := ToWOFF2(in)
		if err != nil {
			require.ErrorIs(t, err, ErrNotConvertible)
			return
		}
		require.Less(t, len(out), len(in))
		require.Equal(t, KindWOFF2, Sniff(out))
		_, err = font.ParseWOFF2(out)
		require.NoError(t, err)
	})
}
