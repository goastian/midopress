package imgmeta

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bitWriter writes what jxlBits reads, so that headers that no encoder here
// makes (wide gamut, PQ, a profile in the file, an orientation) can be tested.
type bitWriter struct {
	b   []byte
	pos int
}

func (w *bitWriter) put(v uint32, n int) *bitWriter {
	for i := 0; i < n; i++ {
		if w.pos>>3 >= len(w.b) {
			w.b = append(w.b, 0)
		}
		w.b[w.pos>>3] |= byte(v>>i&1) << (w.pos & 7)
		w.pos++
	}
	return w
}

func (w *bitWriter) flag(v bool) *bitWriter {
	if v {
		return w.put(1, 1)
	}
	return w.put(0, 1)
}

// enum writes a small enumeration value the short way.
func (w *bitWriter) enum(v uint32) *bitWriter {
	switch {
	case v == 0:
		return w.put(0, 2)
	case v == 1:
		return w.put(1, 2)
	case v < 18:
		return w.put(2, 2).put(v-2, 4)
	}
	return w.put(3, 2).put(v-18, 6)
}

func (w *bitWriter) size(width, height int) *bitWriter {
	// not small, height as 13 bits plus one, ratio 0, width the same way
	return w.flag(false).put(1, 2).put(uint32(height-1), 13).put(0, 3).put(1, 2).put(uint32(width-1), 13)
}

func (w *bitWriter) bytes() []byte { return append([]byte{0xFF, 0x0A}, w.b...) }

// metadata writes an ImageMetadata of 8 bit samples with no extra channels, and
// the colour encoding that colour writes.
func (w *bitWriter) metadata(colour func(*bitWriter)) *bitWriter {
	w.flag(false) // not all default
	w.flag(false) // no extra fields
	w.flag(false) // integer samples
	w.put(0, 2)   // 8 bits
	w.flag(true)  // modular 16 bit buffers
	w.put(0, 2)   // no extra channels
	w.flag(true)  // xyb encoded
	colour(w)
	return w.put(0, 2) // no extensions
}

var defaultColour = func(w *bitWriter) { w.flag(true) }

func explicitColour(space, white, primaries, transfer uint32, wantICC, gamma bool) func(*bitWriter) {
	return func(w *bitWriter) {
		w.flag(false).flag(wantICC)
		if wantICC {
			return
		}
		w.enum(space).enum(white).enum(primaries).flag(gamma)
		if gamma {
			w.put(100000, 24)
		} else {
			w.enum(transfer)
		}
		w.enum(1) // relative intent
	}
}

func TestParseJXLHeader(t *testing.T) {
	t.Run("all default is 8 bit sRGB", func(t *testing.T) {
		j, err := ParseJXL(new(bitWriter).size(640, 480).flag(true).bytes())
		require.NoError(t, err)
		assert.Equal(t, JXL{Width: 640, Height: 480}, *j)
	})
	t.Run("colour that is the default, said or written out", func(t *testing.T) {
		for name, c := range map[string]func(*bitWriter){
			"default": defaultColour, "written": explicitColour(0, 1, 1, 13, false, false),
		} {
			j, err := ParseJXL(new(bitWriter).size(300, 200).metadata(c).bytes())
			require.NoError(t, err, name)
			assert.Equal(t, [2]int{300, 200}, [2]int{j.Width, j.Height}, name)
		}
	})
	t.Run("small headers and aspect ratios", func(t *testing.T) {
		// small: height 8 * (5+1) = 48, ratio 5 is 16:9, so the width is 48*16/9
		w := new(bitWriter).flag(true).put(5, 5).put(5, 3).flag(true)
		j, err := ParseJXL(w.bytes())
		require.NoError(t, err)
		assert.Equal(t, [2]int{85, 48}, [2]int{j.Width, j.Height})
		// small with its own width: 8*(9+1) by 8*(3+1)
		w = new(bitWriter).flag(true).put(3, 5).put(0, 3).put(9, 5).flag(true)
		j, err = ParseJXL(w.bytes())
		require.NoError(t, err)
		assert.Equal(t, [2]int{80, 32}, [2]int{j.Width, j.Height})
	})
	t.Run("colour that a recode would change", func(t *testing.T) {
		for name, c := range map[string]func(*bitWriter){
			"a profile in the file":    explicitColour(0, 1, 1, 13, true, false),
			"Display P3":               explicitColour(0, 1, 11, 13, false, false),
			"BT.2020":                  explicitColour(0, 1, 9, 13, false, false),
			"PQ":                       explicitColour(0, 1, 1, 16, false, false),
			"HLG":                      explicitColour(0, 1, 1, 18, false, false),
			"linear":                   explicitColour(0, 1, 1, 8, false, false),
			"a gamma":                  explicitColour(0, 1, 1, 0, false, true),
			"grey":                     explicitColour(1, 1, 1, 13, false, false),
			"a white point of its own": explicitColour(0, 2, 1, 13, false, false),
		} {
			_, err := ParseJXL(new(bitWriter).size(100, 100).metadata(c).bytes())
			assert.Error(t, err, name)
		}
	})
	t.Run("what else a header can carry", func(t *testing.T) {
		// extra fields (orientation, preview, animation)
		_, err := ParseJXL(new(bitWriter).size(100, 100).flag(false).flag(true).put(0, 20).bytes())
		assert.Error(t, err, "extra fields")
		// an extra channel (alpha)
		w := new(bitWriter).size(100, 100).flag(false).flag(false).flag(false).put(0, 2).flag(true).put(1, 2)
		_, err = ParseJXL(w.bytes())
		assert.Error(t, err, "extra channel")
		// float samples
		w = new(bitWriter).size(100, 100).flag(false).flag(false).flag(true)
		_, err = ParseJXL(w.bytes())
		assert.Error(t, err, "float samples")
		// extensions
		w = new(bitWriter).size(100, 100).flag(false).flag(false).flag(false).put(0, 2).flag(true).put(0, 2).flag(true).flag(true).put(1, 2).put(3, 4)
		_, err = ParseJXL(w.bytes())
		assert.Error(t, err, "extensions")
	})
	t.Run("not a JPEG XL, or cut short", func(t *testing.T) {
		for name, b := range map[string][]byte{
			"empty": nil, "jpeg": {0xFF, 0xD8, 0xFF, 0xE0}, "signature only": {0xFF, 0x0A}, "text": []byte("hello world, not an image"),
			"cut in the middle": new(bitWriter).size(10, 10).bytes()[:3],
		} {
			_, err := ParseJXL(b)
			assert.Error(t, err, name)
		}
	})
}

func TestParseJXLContainer(t *testing.T) {
	code := new(bitWriter).size(64, 32).flag(true).bytes()
	sig := []byte{0, 0, 0, 0x0C, 'J', 'X', 'L', ' ', 0x0D, 0x0A, 0x87, 0x0A}
	ftyp := box("ftyp", []byte("jxl "), u32(0), []byte("jxl "))

	whole := append(append(append([]byte{}, sig...), ftyp...), box("jxlc", code)...)
	j, err := ParseJXL(whole)
	require.NoError(t, err)
	assert.Equal(t, [2]int{64, 32}, [2]int{j.Width, j.Height})

	// the high bit of the index says it is the last part; the rest is the index
	for name, idx := range map[string]uint32{"first and last part": 0x80000000, "first of several": 0} {
		j, err = ParseJXL(append(append(append([]byte{}, sig...), ftyp...), box("jxlp", binary.BigEndian.AppendUint32(nil, idx), code)...))
		require.NoError(t, err, name)
		assert.Equal(t, 64, j.Width, name)
	}
	_, err = ParseJXL(append(append(append([]byte{}, sig...), ftyp...), box("jxlp", binary.BigEndian.AppendUint32(nil, 1), code)...))
	assert.Error(t, err, "a part that is not the first does not start with the header")

	_, err = ParseJXL(append(append([]byte{}, sig...), ftyp...))
	assert.Error(t, err, "no codestream")
	_, err = ParseJXL(bytes.Repeat([]byte{0}, 40))
	assert.Error(t, err)
}

func FuzzParseJXL(f *testing.F) {
	f.Add(new(bitWriter).size(640, 480).flag(true).bytes())
	f.Add(new(bitWriter).size(100, 100).metadata(explicitColour(0, 1, 1, 13, false, false)).bytes())
	f.Fuzz(func(t *testing.T, b []byte) {
		if j, err := ParseJXL(b); err == nil && (j.Width < 1 || j.Height < 1) {
			t.Fatalf("accepted a picture of %dx%d", j.Width, j.Height)
		}
	})
}
