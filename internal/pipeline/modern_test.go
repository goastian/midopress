package pipeline

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/img"
)

func encoded(t testing.TB, f img.Format, w, h, q int) []byte {
	t.Helper()
	data, err := img.NewWASM().Encode(f, noisy(w, h), img.EncodeOptions{Quality: q})
	require.NoError(t, err)
	return data
}

func extremePipeline(mod ...func(*Config)) *Pipeline {
	return newPipeline(append([]func(*Config){func(c *Config) { c.Profile = img.ProfileExtreme; c.Image.Metric = "dssim" }}, mod...)...)
}

func TestModernInputs(t *testing.T) {
	const accept = "image/avif,image/webp,image/png,image/*;q=0.8"

	t.Run("a WebP a site sends is recoded as AVIF and measured", func(t *testing.T) {
		src := encoded(t, img.WebP, 160, 120, 95)
		rec, out := run(extremePipeline(), newReq("Accept", accept), newResp("image/webp", src))
		require.Equal(t, Transformed, out.Reason)
		assert.Equal(t, "image", out.Kind)
		assert.Equal(t, "image/avif", rec.Header().Get("Content-Type"))
		assert.Less(t, rec.Body.Len(), len(src))
		assert.Equal(t, "dssim", out.Metric)
		assert.Positive(t, out.Measure)
		assert.LessOrEqual(t, out.Measure, 0.015, "within the target of the profile, measured against what the site sent")
		assert.Contains(t, rec.Header().Get("Vary"), "Accept")
	})

	t.Run("an AVIF a site sends is recoded too", func(t *testing.T) {
		src := encoded(t, img.AVIF, 160, 120, 85)
		rec, out := run(extremePipeline(), newReq("Accept", accept), newResp("image/avif", src))
		require.Equal(t, Transformed, out.Reason)
		assert.Equal(t, "image/avif", rec.Header().Get("Content-Type"))
		assert.Less(t, rec.Body.Len(), len(src))
	})

	t.Run("a client that only says WebP gets WebP, the format it was already sent", func(t *testing.T) {
		src := encoded(t, img.WebP, 160, 120, 98)
		rec, out := run(extremePipeline(), newReq("Accept", "image/webp,*/*"), newResp("image/webp", src))
		require.Equal(t, Transformed, out.Reason)
		assert.Equal(t, "image/webp", rec.Header().Get("Content-Type"))
		assert.Less(t, rec.Body.Len(), len(src))
	})

	t.Run("when nothing smaller keeps the picture the original is sent", func(t *testing.T) {
		src := encoded(t, img.WebP, 160, 120, 20) // already squeezed hard
		rec, out := run(newPipeline(), newReq("Accept", accept), newResp("image/webp", src))
		assert.NotEqual(t, Transformed, out.Reason)
		assert.Equal(t, src, rec.Body.Bytes())
		assert.Equal(t, "image/webp", rec.Header().Get("Content-Type"))
	})

	t.Run("a JPEG XL with an alpha plane is left alone", func(t *testing.T) {
		src := encoded(t, img.JXL, 160, 120, 90)
		p := extremePipeline()
		rec, out := run(p, newReq("Accept", accept+",image/jxl"), newResp("image/jxl", src))
		assert.NotEqual(t, Transformed, out.Reason)
		assert.Equal(t, src, rec.Body.Bytes())
		assert.Equal(t, "image/jxl", rec.Header().Get("Content-Type"))
		rec, out = run(p, newReq("Accept", accept+",image/jxl"), newResp("image/jxl", src))
		assert.NotEqual(t, Transformed, out.Reason, "the second time too")
		assert.Equal(t, src, rec.Body.Bytes())
	})

	t.Run("it can be turned off", func(t *testing.T) {
		src := encoded(t, img.WebP, 160, 120, 95)
		p := extremePipeline(func(c *Config) { c.Image.Modern = false })
		rec, out := run(p, newReq("Accept", accept), newResp("image/webp", src))
		assert.Equal(t, PassDisabled, out.Reason)
		assert.Equal(t, src, rec.Body.Bytes())
	})

	t.Run("an animated WebP is never flattened", func(t *testing.T) {
		src := withVP8X(encoded(t, img.WebP, 160, 120, 95), 0x02)
		rec, out := run(extremePipeline(), newReq("Accept", accept), newResp("image/webp", src))
		assert.NotEqual(t, Transformed, out.Reason)
		assert.Equal(t, "left alone: webp: animated", out.Cause, "the dashboard says why")
		assert.Equal(t, src, rec.Body.Bytes())
	})

	t.Run("lossless profile has nothing to do with them", func(t *testing.T) {
		src := encoded(t, img.WebP, 160, 120, 95)
		p := newPipeline(func(c *Config) { c.Profile = img.ProfileLossless })
		rec, out := run(p, newReq("Accept", accept), newResp("image/webp", src))
		assert.NotEqual(t, Transformed, out.Reason)
		assert.Equal(t, src, rec.Body.Bytes())
	})

	t.Run("a result that is cached is not computed twice", func(t *testing.T) {
		src := encoded(t, img.WebP, 160, 120, 95)
		p := extremePipeline()
		_, first := run(p, newReq("Accept", accept), newResp("image/webp", src))
		require.Equal(t, Transformed, first.Reason)
		assert.False(t, first.CacheHit)
		_, second := run(p, newReq("Accept", accept), newResp("image/webp", src))
		assert.True(t, second.CacheHit)
	})
}

// withVP8X puts the flags in a VP8X chunk in front of the image data of a
// simple WebP, which is how an extended file says it is animated or has alpha.
func withVP8X(simple []byte, flags byte) []byte {
	chunk := make([]byte, 18)
	copy(chunk, "VP8X")
	binary.LittleEndian.PutUint32(chunk[4:], 10)
	chunk[8] = flags
	chunk[12], chunk[15] = 0x3f, 0xef // 320x240 as width-1 and height-1 in 24 bits
	chunk[13], chunk[16] = 0x01, 0x00
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(4+len(chunk)+len(simple)-12))
	b.WriteString("WEBP")
	b.Write(chunk)
	b.Write(simple[12:])
	return b.Bytes()
}
