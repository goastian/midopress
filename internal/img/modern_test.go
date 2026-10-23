package img

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func encodeModern(t testing.TB, f Format, w, h, q int) []byte {
	t.Helper()
	data, err := NewWASM().Encode(f, photoLike(w, h, 7), EncodeOptions{Quality: q})
	require.NoError(t, err)
	return data
}

func riffChunk(typ string, data []byte) []byte {
	b := append([]byte(typ), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(data)))
	b = append(b, data...)
	if len(data)%2 == 1 {
		b = append(b, 0)
	}
	return b
}

// webpWith adds a chunk right after the header of a simple WebP, in a VP8X
// container that says (with flags) what the file has.
func webpWith(simple []byte, flags byte, extra ...[]byte) []byte {
	vp8x := make([]byte, 10)
	vp8x[0] = flags
	w := binary.LittleEndian.Uint16(simple[26:]) & 0x3fff
	h := binary.LittleEndian.Uint16(simple[28:]) & 0x3fff
	vp8x[4], vp8x[5] = byte(w-1), byte((w-1)>>8)
	vp8x[7], vp8x[8] = byte(h-1), byte((h-1)>>8)
	body := []byte("WEBP")
	body = append(body, riffChunk("VP8X", vp8x)...)
	for _, e := range extra {
		body = append(body, e...)
	}
	body = append(body, simple[12:]...)
	b := append([]byte("RIFF"), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(body)))
	return append(b, body...)
}

func webpWithEXIF(raw, tiff []byte) []byte {
	raw = append(raw, riffChunk("EXIF", tiff)...)
	binary.LittleEndian.PutUint32(raw[4:], uint32(len(raw)-8))
	return raw
}

func optimizeModernInput(t *testing.T, raw []byte, f Format, prof string, caps Caps) (*Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return testEngine.Optimize(ctx, Input{Raw: raw, Format: f, Caps: caps, Profile: profile(t, prof), Budget: time.Minute})
}

func TestModernInputsAreRecodedAgainstWhatTheSiteSent(t *testing.T) {
	webp := encodeModern(t, WebP, 200, 150, 95)
	avif := encodeModern(t, AVIF, 200, 150, 90)
	jxl := encodeModern(t, JXL, 200, 150, 95)

	t.Run("a WebP becomes a smaller picture that meets the target of the profile", func(t *testing.T) {
		res, err := optimizeModernInput(t, webp, WebP, ProfileSaver, Caps{WebP: true, AVIF: true})
		require.NoError(t, err)
		assert.Less(t, len(res.Data), len(webp))
		assert.False(t, res.Lossless)
		assert.Equal(t, MetricSSIM, res.Metric)
		assert.GreaterOrEqual(t, res.Measure, profile(t, ProfileSaver).TargetSSIM, "ssim of the result against the decoded source")
		src, err := NewWASM().Decode(WebP, webp)
		require.NoError(t, err)
		got, err := NewWASM().Decode(res.Format, res.Data)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, mustSimilar(t, src, got), profile(t, ProfileSaver).TargetSSIM-0.01, "measured again, outside the engine")
	})

	t.Run("an AVIF too", func(t *testing.T) {
		res, err := optimizeModernInput(t, avif, AVIF, ProfileExtreme, Caps{WebP: true, AVIF: true})
		require.NoError(t, err)
		assert.Less(t, len(res.Data), len(avif))
	})

	t.Run("a JPEG XL that carries an alpha plane is left alone, as the wasm encoder always writes one", func(t *testing.T) {
		_, err := optimizeModernInput(t, jxl, JXL, ProfileExtreme, Caps{WebP: true, AVIF: true, JXL: true})
		assert.ErrorIs(t, err, ErrUnsupported)
	})

	t.Run("a JPEG XL made by libjxl whose header says plain sRGB is recoded", func(t *testing.T) {
		if !JPEGRecompressAvailable() {
			t.Skip("no native libjxl to make the file with")
		}
		jpg := encodeJPEG(t, photoLike(200, 150, 7), 95)
		real, err := NewWASM().RecompressJPEG(jpg)
		require.NoError(t, err)
		require.NoError(t, checkModern(JXL, real))
		res, err := optimizeModernInput(t, real, JXL, ProfileExtreme, Caps{WebP: true, AVIF: true, JXL: true})
		require.NoError(t, err)
		assert.Less(t, len(res.Data), len(real))
		assert.NotEqual(t, JPEG, res.Format)
	})

	t.Run("a JPEG XL the header of which can not be read to the end is left alone", func(t *testing.T) {
		_, err := optimizeModernInput(t, jxl[:3], JXL, ProfileExtreme, Caps{JXL: true})
		assert.ErrorIs(t, err, ErrUnsupported)
		_, err = optimizeModernInput(t, []byte("\xff\x0anothing a header can be read from here"), JXL, ProfileExtreme, Caps{JXL: true})
		assert.ErrorIs(t, err, ErrUnsupported)
	})

	t.Run("JPEG is never what a WebP becomes", func(t *testing.T) {
		res, err := optimizeModernInput(t, webp, WebP, ProfileExtreme, Caps{})
		require.NoError(t, err)
		assert.Equal(t, WebP, res.Format, "the client was sent a WebP, so it is a WebP")
	})

	t.Run("an animated WebP, one with a profile that is not sRGB or with an orientation, is left alone and says why", func(t *testing.T) {
		tiff := []byte("MM\x00*\x00\x00\x00\x08\x00\x01\x01\x12\x00\x03\x00\x00\x00\x01\x00\x06\x00\x00\x00\x00\x00\x00")
		for name, tc := range map[string]struct {
			raw []byte
			why string
		}{
			"animated":    {webpWith(webp, 0x02, riffChunk("ANIM", make([]byte, 6))), "animated"},
			"bad profile": {webpWith(webp, 0x20, riffChunk("ICCP", []byte("not a colour profile at all, nothing sRGB here"))), "colour profile"},
			"orientation": {webpWithEXIF(webpWith(webp, 0x08), tiff), "orientation"},
			"truncated":   {webp[:20], "unreadable header"},
		} {
			_, err := optimizeModernInput(t, tc.raw, WebP, ProfileExtreme, Caps{WebP: true, AVIF: true})
			assert.ErrorIs(t, err, ErrUnsupported, name)
			require.Error(t, err, name)
			assert.Contains(t, err.Error(), tc.why, name)
		}
	})

	t.Run("lossless profile has nothing to do with them", func(t *testing.T) {
		_, err := optimizeModernInput(t, webp, WebP, ProfileLossless, Caps{WebP: true, AVIF: true})
		assert.ErrorIs(t, err, ErrUnsupported)
		assert.Contains(t, err.Error(), "lossless profile")
	})

	t.Run("not an image", func(t *testing.T) {
		_, err := optimizeModernInput(t, []byte("RIFF....WEBPnothing here"), WebP, ProfileExtreme, Caps{WebP: true})
		assert.ErrorIs(t, err, ErrUnsupported)
		_, err = optimizeModernInput(t, []byte("\x00\x00\x00\x18ftypavif\x00\x00\x00\x00avifmif1"), AVIF, ProfileExtreme, Caps{AVIF: true})
		assert.ErrorIs(t, err, ErrUnsupported)
	})
}

// The real files are not in the repository. With them there, the rules that
// keep colour and orientation intact are checked against files that sites and
// the libavif samples really have.
func TestRealFilesThatCarryWhatAPixelCanNot(t *testing.T) {
	dir := "../../artifacts/corpus-modern"
	if _, err := os.Stat(dir); err != nil {
		t.Skip("no corpus of real WebP and AVIF files in " + dir)
	}
	for name, f := range map[string]Format{
		"linku-kimono-rot90.avif": AVIF, "linku-kimono-mirror.avif": AVIF, "linku-kimono-crop.avif": AVIF,
		"unsplash-1600.avif": AVIF, "unsplash-1600.webp": WebP, // a colour profile that is not sRGB
	} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		assert.Error(t, checkModern(f, raw), name)
	}
	for name, f := range map[string]Format{"gstatic-1.webp": WebP, "ytimg-hq.webp": WebP, "linku-kimono.avif": AVIF, "linku-hato.avif": AVIF} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		assert.NoError(t, checkModern(f, raw), name)
	}
}
