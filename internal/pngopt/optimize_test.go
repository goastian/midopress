package pngopt

import (
	"bytes"
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// hashNoise is a deterministic stand-in for random noise, safe for concurrent use.
func hashNoise(x, y int) int {
	h := uint32(x)*0x9E3779B1 ^ uint32(y)*0x85EBCA6B
	h ^= h >> 15
	h *= 0x2C1B3C6D
	h ^= h >> 13
	return int(h % 8)
}

type synth struct {
	name       string
	sp         spec
	ctype      int
	depth      int
	mustShrink bool
}

func rgbaPalette(n int) []byte {
	p := make([]byte, 0, 3*n)
	for i := range n {
		p = append(p, byte(i*37), byte(i*91+5), byte(255-i*13))
	}
	return p
}

func synthCases() []synth {
	var cs []synth
	add := func(name string, sp spec, ctype, depth int) {
		cs = append(cs, synth{name: name, sp: sp, ctype: ctype, depth: depth, mustShrink: true})
	}
	flat := func(x, y int, s []uint16) {
		c := (x/40 + y/30) % 5
		s[0], s[1], s[2] = uint16(c*50), uint16(255-c*40), uint16(c*c*10)
	}
	add("flat-rgb8", spec{w: 200, h: 120, ctype: 2, depth: 8, gen: flat}, 3, 4)
	add("flat-rgb8-interlaced", spec{w: 201, h: 121, ctype: 2, depth: 8, interlace: true, gen: flat}, 3, 4)
	two := func(x, y int, s []uint16) {
		v := uint16(0)
		if (x/8+y/8)%2 == 0 {
			v = 255
		}
		s[0], s[1], s[2] = v, v, v
	}
	add("two-colour-rgb", spec{w: 64, h: 64, ctype: 2, depth: 8, gen: two}, 0, 1)
	icon := func(x, y int, s []uint16) {
		dx, dy := float64(x-50), float64(y-50)
		d := math.Hypot(dx, dy)
		a := math.Max(0, math.Min(1, 42-d))
		if a == 0 {
			s[0], s[1], s[2], s[3] = uint16(x*3&255), uint16(y*5&255), 77, 0
			return
		}
		s[0], s[1], s[2], s[3] = 200, 30, 60, uint16(a*255)
	}
	add("icon-rgba8", spec{w: 100, h: 100, ctype: 6, depth: 8, gen: icon}, 3, 8)
	add("icon-rgba8-interlaced", spec{w: 100, h: 100, ctype: 6, depth: 8, interlace: true, gen: icon}, 3, 8)
	opaqueRGBA := func(x, y int, s []uint16) { s[0], s[1], s[2], s[3] = uint16(x%7*30), uint16(y%5*40), 9, 255 }
	add("opaque-rgba8", spec{w: 80, h: 60, ctype: 6, depth: 8, gen: opaqueRGBA}, 3, 8)
	photoRGBA := func(x, y int, s []uint16) {
		s[0], s[1], s[2], s[3] = uint16(x+hashNoise(x, y))&255, uint16(y*2+hashNoise(x, y))&255, uint16((x+y)/2), 255
	}
	add("photo-opaque-rgba8", spec{w: 120, h: 90, ctype: 6, depth: 8, gen: photoRGBA}, 2, 8)
	photoA := func(x, y int, s []uint16) {
		s[0], s[1], s[2], s[3] = uint16(x+hashNoise(x, y))&255, uint16(y*2+hashNoise(x, y))&255, uint16((x+y)/2), uint16(x*2)&255
	}
	add("photo-alpha-rgba8", spec{w: 120, h: 90, ctype: 6, depth: 8, gen: photoA}, 6, 8)
	pal := func(n int) func(x, y int, s []uint16) {
		return func(x, y int, s []uint16) { s[0] = uint16((x*3 + y) % n) }
	}
	add("palette8-two-used", spec{w: 50, h: 50, ctype: 3, depth: 8, plte: rgbaPalette(10), gen: func(x, y int, s []uint16) { s[0] = uint16((x + y) % 2 * 7) }}, 3, 1)
	add("palette8-16", spec{w: 50, h: 50, ctype: 3, depth: 8, plte: rgbaPalette(16), gen: pal(16)}, 3, 4)
	add("palette8-5-trns", spec{w: 50, h: 50, ctype: 3, depth: 8, plte: rgbaPalette(5), trns: []byte{0, 128}, gen: pal(5)}, 3, 4)
	add("palette4-interlaced", spec{w: 53, h: 41, ctype: 3, depth: 4, plte: rgbaPalette(16), interlace: true, gen: pal(16)}, 3, 4)
	add("palette2", spec{w: 50, h: 50, ctype: 3, depth: 2, plte: rgbaPalette(4), gen: pal(4)}, 3, 2)
	add("palette1-grayish", spec{w: 50, h: 50, ctype: 3, depth: 1, plte: []byte{0, 0, 0, 255, 255, 255}, gen: pal(2)}, 0, 1)
	add("palette8-gradient", spec{w: 256, h: 40, ctype: 3, depth: 8, plte: rgbaPalette(256), gen: func(x, y int, s []uint16) { s[0] = uint16(x) }}, 3, 8)
	add("gray8-4levels", spec{w: 64, h: 64, ctype: 0, depth: 8, gen: func(x, y int, s []uint16) { s[0] = uint16((x/4+y/4)%4) * 85 }}, 0, 2)
	add("gray8-gradient", spec{w: 256, h: 64, ctype: 0, depth: 8, gen: func(x, y int, s []uint16) { s[0] = uint16(x) }}, 0, 8)
	add("gray8-odd-levels", spec{w: 64, h: 64, ctype: 0, depth: 8, gen: func(x, y int, s []uint16) { s[0] = uint16((x/4+y/4)%3) * 100 }}, 3, 2)
	add("gray4", spec{w: 64, h: 64, ctype: 0, depth: 4, gen: func(x, y int, s []uint16) { s[0] = uint16(x % 16) }}, 0, 4)
	add("gray1-interlaced", spec{w: 37, h: 19, ctype: 0, depth: 1, interlace: true, gen: func(x, y int, s []uint16) { s[0] = uint16((x + y) % 2) }}, 0, 1)
	add("gray16-reducible", spec{w: 64, h: 64, ctype: 0, depth: 16, gen: func(x, y int, s []uint16) { s[0] = uint16(x*4) * 257 }}, 0, 8)
	add("gray16-full", spec{w: 64, h: 64, ctype: 0, depth: 16, gen: func(x, y int, s []uint16) { s[0] = uint16(x*1000 + y) }}, 0, 16)
	ga := func(x, y int, s []uint16) { s[0], s[1] = uint16(x%4)*85, uint16(y%3)*127 }
	add("graya8-few", spec{w: 64, h: 64, ctype: 4, depth: 8, gen: ga}, 3, 4)
	add("graya8-many", spec{w: 200, h: 64, ctype: 4, depth: 8, gen: func(x, y int, s []uint16) { s[0], s[1] = uint16(x), uint16(y*3) }}, 4, 8)
	add("graya8-opaque", spec{w: 200, h: 64, ctype: 4, depth: 8, gen: func(x, y int, s []uint16) { s[0], s[1] = uint16(x), 255 }}, 0, 8)
	add("graya16", spec{w: 64, h: 64, ctype: 4, depth: 16, gen: func(x, y int, s []uint16) { s[0], s[1] = uint16(x*999), uint16(y*1013) }}, 4, 16)
	add("graya16-reducible-binary-alpha", spec{w: 64, h: 64, ctype: 4, depth: 16, gen: func(x, y int, s []uint16) {
		s[0], s[1] = 0, 0
		if y%2 == 0 {
			s[0], s[1] = uint16(x*3)*257, 65535
		}
	}}, 3, 8)
	add("graya16-reducible-low-alpha-kept", spec{w: 64, h: 64, ctype: 4, depth: 16, gen: func(x, y int, s []uint16) { s[0], s[1] = uint16(x*3)*257, uint16(y*3+1)*257 }}, 4, 16)
	add("rgb16-reducible", spec{w: 64, h: 64, ctype: 2, depth: 16, gen: func(x, y int, s []uint16) { s[0], s[1], s[2] = uint16(x*4)*257, uint16(y*4)*257, 257*9 }}, 2, 8)
	add("rgb16-full", spec{w: 64, h: 64, ctype: 2, depth: 16, gen: func(x, y int, s []uint16) { s[0], s[1], s[2] = uint16(x*1001), uint16(y*977), uint16(x*y) }}, 2, 16)
	add("rgb16-gray", spec{w: 64, h: 64, ctype: 2, depth: 16, gen: func(x, y int, s []uint16) { v := uint16(x*1001 + y); s[0], s[1], s[2] = v, v, v }}, 0, 16)
	add("rgba16", spec{w: 64, h: 64, ctype: 6, depth: 16, gen: func(x, y int, s []uint16) {
		s[0], s[1], s[2], s[3] = uint16(x*1001), uint16(y*977), uint16(x*y), uint16(x*500)
	}}, 6, 16)
	add("rgba16-opaque", spec{w: 64, h: 64, ctype: 6, depth: 16, gen: func(x, y int, s []uint16) { s[0], s[1], s[2], s[3] = uint16(x*1001), uint16(y*977), uint16(x*y), 65535 }}, 2, 16)
	add("rgba16-reducible-interlaced", spec{w: 45, h: 33, ctype: 6, depth: 16, interlace: true, gen: func(x, y int, s []uint16) {
		s[0], s[1], s[2], s[3] = uint16(x*5)*257, uint16(y*5)*257, 257, 255*257
	}}, 2, 8)
	add("gray8-trns-key", spec{w: 64, h: 64, ctype: 0, depth: 8, trns: []byte{0, 85}, gen: func(x, y int, s []uint16) { s[0] = uint16((x/4+y/4)%4) * 85 }}, 3, 2)
	add("gray4-trns-key", spec{w: 64, h: 64, ctype: 0, depth: 4, trns: []byte{0, 3}, gen: func(x, y int, s []uint16) { s[0] = uint16(x % 16) }}, 3, 4)
	add("gray16-trns-key", spec{w: 64, h: 64, ctype: 0, depth: 16, trns: []byte{0x12, 0x34}, gen: func(x, y int, s []uint16) {
		s[0] = uint16(0x1234 + (x%3-1)*(y%2))
	}}, 4, 16)
	add("rgb8-trns-key", spec{w: 64, h: 64, ctype: 2, depth: 8, trns: []byte{0, 255, 0, 0, 0, 0}, gen: func(x, y int, s []uint16) {
		s[0], s[1], s[2] = uint16(x%2)*255, 0, 0
	}}, 3, 1)
	add("rgb16-trns-key", spec{w: 64, h: 64, ctype: 2, depth: 16, trns: []byte{0x12, 0x34, 0, 1, 0, 2}, gen: func(x, y int, s []uint16) {
		s[0], s[1], s[2] = uint16(0x1234+x%2), uint16(1+y%2), 2
	}}, 6, 16)
	add("tiny-1x1", spec{w: 1, h: 1, ctype: 6, depth: 8, gen: func(x, y int, s []uint16) { s[0], s[1], s[2], s[3] = 1, 2, 3, 4 }}, -1, -1)
	cs[len(cs)-1].mustShrink = false
	add("row-1x33-interlaced", spec{w: 33, h: 1, ctype: 2, depth: 8, interlace: true, gen: func(x, y int, s []uint16) { s[0], s[1], s[2] = uint16(x), 2, 3 }}, -1, -1)
	cs[len(cs)-1].mustShrink = false
	add("col-3x40-interlaced", spec{w: 3, h: 40, ctype: 0, depth: 8, interlace: true, gen: func(x, y int, s []uint16) { s[0] = uint16(x*100 + y) }}, -1, -1)
	cs[len(cs)-1].mustShrink = false
	return cs
}

func ihdrOf(t testing.TB, data []byte) header {
	t.Helper()
	p, err := parse(data)
	require.NoError(t, err)
	return p.hdr
}

func chunkTypes(t testing.TB, data []byte) []string {
	t.Helper()
	var out []string
	pos := 8
	for pos+8 <= len(data) {
		n := int(uint32(data[pos])<<24 | uint32(data[pos+1])<<16 | uint32(data[pos+2])<<8 | uint32(data[pos+3]))
		out = append(out, string(data[pos+4:pos+8]))
		pos += 12 + n
	}
	return out
}

func sameSlice(a, b []byte) bool { return len(a) > 0 && len(a) == len(b) && &a[0] == &b[0] }

func TestOptimizeSynthetic(t *testing.T) {
	for _, c := range synthCases() {
		in := buildPNG(c.sp)
		decodeStd(t, in)
		for _, lvl := range []Level{Fast, Default, Best} {
			t.Run(fmt.Sprintf("%s/L%d", c.name, lvl), func(t *testing.T) {
				out, err := Optimize(in, lvl)
				require.NoError(t, err)
				require.LessOrEqual(t, len(out), len(in))
				requireSamePixels(t, in, out)
				if sameSlice(in, out) {
					require.False(t, c.mustShrink, "expected shrink")
					return
				}
				require.NotContains(t, chunkTypes(t, out), "tEXt")
				h := ihdrOf(t, out)
				require.Zero(t, h.ilc)
				if c.ctype >= 0 && lvl == Fast {
					require.Equal(t, c.ctype, int(h.ctype), "colour type")
					require.Equal(t, c.depth, int(h.depth), "depth")
				}
			})
		}
	}
}

func TestOptimizeFFmpeg(t *testing.T) {
	for _, c := range synthCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if c.sp.ctype == 0 && c.sp.depth < 8 && c.sp.trns != nil {
				t.Skip("ffmpeg ignores tRNS on low bit depth grey")
			}
			in := buildPNG(c.sp)
			out, err := Optimize(in, Default)
			require.NoError(t, err)
			requireSameFFmpeg(t, in, out)
		})
	}
}

func TestOptimizeKeepsColourChunks(t *testing.T) {
	gamma := []byte{0, 0, 0xb1, 0x8f}
	for _, name := range []string{"sRGB", "gAMA", "cHRM", "iCCP"} {
		var data []byte
		switch name {
		case "sRGB":
			data = []byte{0}
		case "gAMA":
			data = gamma
		case "cHRM":
			data = bytes.Repeat([]byte{0, 0, 1, 2}, 8)
		case "iCCP":
			data = []byte("name\x00\x00\x78\x9c\x03\x00\x00\x00\x00\x01")
		}
		sp := spec{w: 64, h: 64, ctype: 2, depth: 8, extra: []chunk{{name, data}, {"sBIT", []byte{8, 8, 8}}},
			gen: func(x, y int, s []uint16) { s[0], s[1], s[2] = uint16(x%3*100), uint16(y%2*100), 5 }}
		in := buildPNG(sp)
		out, err := Optimize(in, Default)
		require.NoError(t, err)
		require.False(t, sameSlice(in, out))
		p, err := parse(out)
		require.NoError(t, err)
		var found, sbit bool
		for _, c := range p.color {
			if c.typ == name {
				found = true
				require.Equal(t, data, c.data)
			}
			sbit = sbit || c.typ == "sBIT"
		}
		require.True(t, found, name)
		h := p.hdr
		if name == "iCCP" {
			require.NotContains(t, []int{0, 4}, int(h.ctype), "model must not change with an ICC profile")
			require.True(t, sbit, "sBIT of 8,8,8 stays valid for a palette")
		}
	}
}

func TestOptimizeDropsInvalidSBIT(t *testing.T) {
	sp := spec{w: 64, h: 64, ctype: 2, depth: 8, extra: []chunk{{"sBIT", []byte{8, 8, 8}}},
		gen: func(x, y int, s []uint16) { v := uint16(x%2) * 255; s[0], s[1], s[2] = v, v, v }}
	out, err := Optimize(buildPNG(sp), Default)
	require.NoError(t, err)
	require.NotContains(t, chunkTypes(t, out), "sBIT")
	require.Equal(t, 1, int(ihdrOf(t, out).depth))
}

func TestOptimizeAPNGUntouched(t *testing.T) {
	in := buildPNG(spec{w: 8, h: 8, ctype: 2, depth: 8, gen: func(x, y int, s []uint16) {}})
	idat := bytes.Index(in, []byte("tEXt")) - 4
	acTL := appendChunk(nil, "acTL", []byte{0, 0, 0, 1, 0, 0, 0, 0})
	apng := append(append(append([]byte{}, in[:idat]...), acTL...), in[idat:]...)
	out, err := Optimize(apng, Best)
	require.NoError(t, err)
	require.True(t, sameSlice(apng, out))
}

func TestOptimizeMalformed(t *testing.T) {
	good := buildPNG(spec{w: 30, h: 30, ctype: 2, depth: 8, gen: func(x, y int, s []uint16) { s[0] = uint16(x) }})
	cases := map[string][]byte{
		"empty":     nil,
		"signature": []byte("not a png at all, definitely not, really not"),
		"truncated": good[:len(good)-20],
		"half":      good[:len(good)/2],
	}
	bad := bytes.Clone(good)
	bad[40] ^= 0xff
	cases["crc"] = bad
	for name, in := range cases {
		out, err := Optimize(in, Default)
		require.Error(t, err, name)
		require.Nil(t, out, name)
	}
}

func TestOptimizeTooLarge(t *testing.T) {
	var ihdr [13]byte
	ihdr[0], ihdr[1], ihdr[2], ihdr[3] = 0, 0, 0x40, 0
	ihdr[4], ihdr[5], ihdr[6], ihdr[7] = 0, 0, 0x40, 0
	ihdr[8], ihdr[9] = 8, 2
	var in []byte
	in = append(in, pngSig...)
	in = appendChunk(in, "IHDR", ihdr[:])
	in = appendChunk(in, "IDAT", []byte{0x78, 0x9c, 3, 0, 0, 0, 0, 1})
	in = appendChunk(in, "IEND", nil)
	_, err := Optimize(in, Fast)
	require.ErrorIs(t, err, ErrTooLarge)
}

func TestOptimizeDecompressionBomb(t *testing.T) {
	var ihdr [13]byte
	ihdr[2], ihdr[3] = 0x20, 0
	ihdr[6], ihdr[7] = 0x20, 0
	ihdr[8], ihdr[9] = 16, 6
	var in []byte
	in = append(in, pngSig...)
	in = appendChunk(in, "IHDR", ihdr[:])
	in = appendChunk(in, "IDAT", []byte{0x78, 0x9c, 3, 0, 0, 0, 0, 1})
	in = appendChunk(in, "IEND", nil)
	_, err := Optimize(in, Fast)
	require.ErrorIs(t, err, ErrFormat)
}

func TestOptimizeIdempotentSize(t *testing.T) {
	in := buildPNG(synthCases()[0].sp)
	a, err := Optimize(in, Best)
	require.NoError(t, err)
	b, err := Optimize(a, Best)
	require.NoError(t, err)
	require.LessOrEqual(t, len(b), len(a))
	requireSamePixels(t, in, b)
}

func TestUniqueColors(t *testing.T) {
	img := decodeStd(t, buildPNG(synthCases()[0].sp))
	n, trunc := UniqueColors(img, 100)
	require.Equal(t, 5, n)
	require.False(t, trunc)
	n, trunc = UniqueColors(img, 4)
	require.Equal(t, 4, n)
	require.True(t, trunc)
	n, trunc = UniqueColors(img, 5)
	require.Equal(t, 5, n)
	require.False(t, trunc)
	n, trunc = UniqueColors(img, 0)
	require.Equal(t, 5, n)
	require.False(t, trunc)
}
