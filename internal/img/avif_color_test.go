package img

import (
	"context"
	"encoding/binary"
	"image"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/colorcms"
	"github.com/goastian/midopress/internal/imgmeta"
)

type fixtureBox struct {
	kind string
	body []byte
}

func fixtureBoxes(t *testing.T, raw []byte) []fixtureBox {
	t.Helper()
	var out []fixtureBox
	for len(raw) > 0 {
		require.GreaterOrEqual(t, len(raw), 8)
		n := int(binary.BigEndian.Uint32(raw))
		require.GreaterOrEqual(t, n, 8)
		require.LessOrEqual(t, n, len(raw))
		out = append(out, fixtureBox{string(raw[4:8]), append([]byte(nil), raw[8:n]...)})
		raw = raw[n:]
	}
	return out
}

func packFixtureBoxes(boxes []fixtureBox) []byte {
	var out []byte
	for _, box := range boxes {
		out = binary.BigEndian.AppendUint32(out, uint32(len(box.body)+8))
		out = append(out, box.kind...)
		out = append(out, box.body...)
	}
	return out
}

// managedAVIFFixture extends a real encoder-produced AVIF's primary item with
// presentation and ICC properties. Its iloc media offsets move with the metadata.
// Restricted to the single still image layout produced for this fixture.
func managedAVIFFixture(t *testing.T, raw []byte, extras []fixtureBox) []byte {
	t.Helper()
	top := fixtureBoxes(t, raw)
	for i := range top {
		if top[i].kind != "meta" {
			continue
		}
		oldSize := len(top[i].body)
		children := fixtureBoxes(t, top[i].body[4:])
		for j := range children {
			if children[j].kind != "iprp" {
				continue
			}
			props := fixtureBoxes(t, children[j].body)
			var indices []byte
			for k := range props {
				if props[k].kind == "ipco" {
					count := len(fixtureBoxes(t, props[k].body))
					for n := range extras {
						require.Less(t, count+n+1, 128)
						index := byte(count + n + 1)
						if extras[n].kind == "clap" || extras[n].kind == "irot" || extras[n].kind == "imir" {
							index |= 0x80
						}
						indices = append(indices, index)
					}
					props[k].body = append(props[k].body, packFixtureBoxes(extras)...)
				}
			}
			for k := range props {
				if props[k].kind == "ipma" {
					b := props[k].body
					require.GreaterOrEqual(t, len(b), 11)
					require.Equal(t, []byte{0, 0, 0, 0}, b[:4], "fixture expects 8-bit associations")
					require.Equal(t, uint32(1), binary.BigEndian.Uint32(b[4:]))
					require.Equal(t, 11+int(b[10]), len(b))
					b[10] += byte(len(indices))
					props[k].body = append(b, indices...)
				}
			}
			children[j].body = packFixtureBoxes(props)
		}
		delta := len(packFixtureBoxes(children)) + 4 - oldSize
		for j := range children {
			if children[j].kind != "iloc" {
				continue
			}
			b := children[j].body
			require.GreaterOrEqual(t, len(b), 14)
			require.Equal(t, byte(0), b[0])
			offsetBytes, lengthBytes, baseBytes := int(b[4]>>4), int(b[4]&15), int(b[5]>>4)
			require.Contains(t, []int{0, 4}, offsetBytes)
			require.Equal(t, 4, lengthBytes)
			require.Contains(t, []int{0, 4}, baseBytes)
			require.True(t, baseBytes != 0 || offsetBytes != 0)
			n := int(binary.BigEndian.Uint16(b[6:]))
			pos := 8
			for entry := 0; entry < n; entry++ {
				require.GreaterOrEqual(t, len(b)-pos, 6+baseBytes)
				require.Zero(t, binary.BigEndian.Uint16(b[pos+2:]), "self-contained fixture")
				pos += 4
				if baseBytes == 4 {
					binary.BigEndian.PutUint32(b[pos:], binary.BigEndian.Uint32(b[pos:])+uint32(delta))
					pos += 4
				}
				extents := int(binary.BigEndian.Uint16(b[pos:]))
				pos += 2
				for extent := 0; extent < extents; extent++ {
					require.GreaterOrEqual(t, len(b)-pos, offsetBytes+lengthBytes)
					if baseBytes == 0 {
						offset := binary.BigEndian.Uint32(b[pos:])
						binary.BigEndian.PutUint32(b[pos:], offset+uint32(delta))
					}
					pos += offsetBytes + lengthBytes
				}
			}
		}
		top[i].body = append(append([]byte(nil), top[i].body[:4]...), packFixtureBoxes(children)...)
	}
	return packFixtureBoxes(top)
}

func TestActualAVIFICCAndPresentationAreConverted(t *testing.T) {
	be := NewWASM()
	raw, err := be.Encode(AVIF, photoLike(192, 128, 31), EncodeOptions{Quality: 100, Effort: 1})
	require.NoError(t, err)
	icc, err := os.ReadFile("../colorcms/testdata/display-p3.icc")
	require.NoError(t, err)
	var aperture []byte
	for _, n := range []uint32{160, 1, 96, 1, 0, 1, 0, 1} {
		aperture = binary.BigEndian.AppendUint32(aperture, n)
	}
	tagged := managedAVIFFixture(t, raw, []fixtureBox{{"clap", aperture}, {"irot", []byte{1}}, {"imir", []byte{1}}, {"colr", append([]byte("prof"), icc...)}})
	transforms, err := imgmeta.AVIFPresentation(tagged)
	require.NoError(t, err)
	require.Len(t, transforms, 3)
	originalPixels, err := be.Decode(AVIF, raw)
	require.NoError(t, err)
	taggedPixels, err := be.Decode(AVIF, tagged)
	require.NoError(t, err)
	require.True(t, samePixels(originalPixels, taggedPixels), "codec returns coded pixels; iloc remains correct")
	want, err := avifPresentation(originalPixels, transforms)
	require.NoError(t, err)
	want, err = colorcms.ToSRGB(context.Background(), want, icc)
	require.NoError(t, err)
	got, err := DecodeDisplay(context.Background(), be, AVIF, tagged)
	require.NoError(t, err)
	assert.Equal(t, image.Pt(96, 160), got.Bounds().Size())
	assert.True(t, samePixels(want, got), "ICC and all primary presentation properties are applied once")
	p := profile(t, ProfileExtreme)
	p.ColorPolicy = "srgb"
	p.Formats, p.ResizeToViewport = []Format{WebP}, false
	e := NewEngine(be)
	e.Metric = MetricSSIM
	result, err := e.Optimize(context.Background(), Input{Raw: tagged, Format: AVIF, Profile: p, Caps: Caps{WebP: true}})
	require.NoError(t, err)
	require.Equal(t, WebP, result.Format, "supported ICC + crop + rotation + mirror must really recode")
	decoded, err := be.Decode(result.Format, result.Data)
	require.NoError(t, err)
	sc, err := newScorer(MetricSSIM, want, p)
	require.NoError(t, err)
	score, err := sc.eval(decoded)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, score, sc.target)
}
