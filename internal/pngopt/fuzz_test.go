package pngopt

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/stretchr/testify/require"
)

func checkOptimize(t *testing.T, data []byte, level Level) {
	out, err := Optimize(data, level)
	if err != nil {
		require.Nil(t, out)
		return
	}
	require.LessOrEqual(t, len(out), len(data))
	want, derr := png.Decode(bytes.NewReader(data))
	if derr != nil {
		return
	}
	got, err := png.Decode(bytes.NewReader(out))
	require.NoError(t, err)
	require.Equal(t, want.Bounds(), got.Bounds())
	b := want.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			require.Equal(t, color.NRGBA64Model.Convert(want.At(x, y)), color.NRGBA64Model.Convert(got.At(x, y)), "(%d,%d)", x, y)
		}
	}
}

// fixCRCs recomputes the CRC of every chunk so mutated chunks get past the
// checksum test.
func fixCRCs(data []byte) []byte {
	out := bytes.Clone(data)
	pos := 8
	for pos+12 <= len(out) {
		n := int(binary.BigEndian.Uint32(out[pos:]))
		if n < 0 || pos+12+n > len(out) {
			break
		}
		binary.BigEndian.PutUint32(out[pos+8+n:], crc32.ChecksumIEEE(out[pos+4:pos+8+n]))
		pos += 12 + n
	}
	return out
}

func FuzzOptimize(f *testing.F) {
	for i, c := range synthCases() {
		if i%2 == 0 || c.sp.w*c.sp.h <= 600 {
			sp := c.sp
			sp.w, sp.h = min(sp.w, 24), min(sp.h, 24)
			f.Add(buildPNG(sp), uint8(i))
		}
	}
	corpus, _ := readCorpus()
	for _, b := range corpus {
		if len(b) < 4000 {
			f.Add(b, uint8(1))
		}
	}
	f.Fuzz(func(t *testing.T, data []byte, lvl uint8) {
		level := Level(lvl % 3)
		checkOptimize(t, data, level)
		if len(data) > 8 {
			checkOptimize(t, fixCRCs(data), level)
		}
	})
}

var formats = [][2]byte{{0, 1}, {0, 2}, {0, 4}, {0, 8}, {0, 16}, {2, 8}, {2, 16}, {3, 1}, {3, 2}, {3, 4}, {3, 8}, {4, 8}, {4, 16}, {6, 8}, {6, 16}}

// FuzzOptimizeBuilt builds a valid PNG of any format from the fuzz input so
// the pixel paths are exercised rather than the chunk parser.
func FuzzOptimizeBuilt(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}, uint8(13), uint8(7), uint8(5), true, uint8(1))
	f.Add(bytes.Repeat([]byte{0, 255, 0, 0}, 50), uint8(11), uint8(20), uint8(9), false, uint8(2))
	f.Add([]byte{9}, uint8(3), uint8(1), uint8(1), true, uint8(0))
	f.Fuzz(func(t *testing.T, pix []byte, format, width, height uint8, interlace bool, lvl uint8) {
		if len(pix) == 0 {
			return
		}
		fm := formats[int(format)%len(formats)]
		w, h := int(width%24)+1, int(height%24)+1
		hd := header{w: w, h: h, ctype: fm[0], depth: fm[1]}
		passes := adam7
		if !interlace {
			passes = []adamPass{{0, 0, 1, 1}}
		}
		var raw []byte
		pos := 0
		next := func() byte {
			b := pix[pos%len(pix)]
			pos++
			return b
		}
		for _, ps := range passes {
			pw, ph := passSize(hd, ps)
			if pw == 0 || ph == 0 {
				continue
			}
			for range ph {
				raw = append(raw, next()%5)
				for range hd.rowBytes(pw) {
					b := next()
					raw = append(raw, b)
				}
			}
		}
		var zb bytes.Buffer
		zw := zlib.NewWriter(&zb)
		_, _ = zw.Write(raw)
		_ = zw.Close()
		var ihdr [13]byte
		binary.BigEndian.PutUint32(ihdr[0:], uint32(w))
		binary.BigEndian.PutUint32(ihdr[4:], uint32(h))
		ihdr[8], ihdr[9] = fm[1], fm[0]
		if interlace {
			ihdr[12] = 1
		}
		out := append([]byte{}, pngSig...)
		out = appendChunk(out, "IHDR", ihdr[:])
		switch fm[0] {
		case 3:
			n := 1 << fm[1]
			plte := make([]byte, 3*n)
			for i := range plte {
				plte[i] = next()
			}
			out = appendChunk(out, "PLTE", plte)
			out = appendChunk(out, "tRNS", []byte{next(), next(), next() | 255})
		case 0:
			if next()&1 == 1 {
				out = appendChunk(out, "tRNS", []byte{0, byte(int(next()) % (1 << min(fm[1], 8)))})
			}
		case 2:
			if next()&1 == 1 {
				out = appendChunk(out, "tRNS", []byte{0, next(), 0, next(), 0, next()})
			}
		}
		out = appendChunk(out, "IDAT", zb.Bytes())
		out = appendChunk(out, "IEND", nil)
		checkOptimize(t, out, Level(lvl%3))
	})
}

func FuzzQuantizeSmall(f *testing.F) {
	f.Add([]byte{255, 0, 0, 255, 0, 255, 0, 128, 0, 0, 255, 0, 10, 20, 30, 255}, uint8(4), uint8(2), uint8(128))
	f.Add(bytes.Repeat([]byte{1, 2, 3, 4, 200, 100, 50, 255}, 40), uint8(7), uint8(16), uint8(255))
	f.Add([]byte{}, uint8(1), uint8(2), uint8(0))
	f.Fuzz(func(t *testing.T, pix []byte, width, colors, dither uint8) {
		w := int(width%16) + 1
		n := len(pix) / 4
		if n == 0 {
			return
		}
		h := (n + w - 1) / w
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		copy(img.Pix, pix[:n*4])
		opts := QuantizeOptions{Colors: int(colors)%255 + 2, Dither: float64(dither)/128 - 0.5}
		q, err := Quantize(img, opts)
		require.NoError(t, err)
		require.LessOrEqual(t, len(q.Palette), opts.Colors)
		require.Equal(t, img.Bounds(), q.Bounds())
		for _, i := range q.Pix {
			require.Less(t, int(i), len(q.Palette))
		}
		again, err := Quantize(img, opts)
		require.NoError(t, err)
		require.Equal(t, q.Pix, again.Pix)
		data, err := Encode(q, Level(dither%3))
		require.NoError(t, err)
		got, err := png.Decode(bytes.NewReader(data))
		require.NoError(t, err)
		for y := range h {
			for x := range w {
				require.Equal(t, color.NRGBA64Model.Convert(q.At(x, y)), color.NRGBA64Model.Convert(got.At(x, y)))
			}
		}
	})
}
