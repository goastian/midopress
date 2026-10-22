package img

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/color/palette"
	stdgif "image/gif"
	stdjpeg "image/jpeg"
	"image/png"
	"math"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/quality"
)

// The engine tests use the ssim metric: they are about the engine, and the
// metrics have their own tests. TestDSSIMMetric checks the default one.
var testEngine = func() *Engine {
	e := NewEngine(NewWASM())
	e.Metric = MetricSSIM
	return e
}()

// photoLike makes a picture with smooth areas, edges and texture, which is what
// image codecs have to deal with in photographs.
func photoLike(w, h int, seed uint64) *image.RGBA {
	rng := rand.New(rand.NewPCG(seed, seed+1))
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			fx, fy := float64(x)/float64(w), float64(y)/float64(h)
			r := 128 + 90*math.Sin(6*fx+2*fy) + 25*math.Sin(40*fx*fy)
			g := 120 + 80*math.Sin(5*fy-fx) + 20*math.Cos(31*fx)
			b := 110 + 70*math.Cos(4*fx+3*fy)
			if (x/23+y/17)%5 == 0 { // blocks with hard edges
				r, g, b = 255-r, g*0.7, b*1.2
			}
			n := float64(rng.IntN(15)) - 7
			m.SetRGBA(x, y, color.RGBA{R: clamp8(r + n), G: clamp8(g + n), B: clamp8(b + n), A: 255})
		}
	}
	return m
}

func clamp8(v float64) uint8 { return uint8(max(0, min(255, v))) }

func encodeJPEG(t testing.TB, m image.Image, q int) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, stdjpeg.Encode(&b, m, &stdjpeg.Options{Quality: q}))
	return b.Bytes()
}

func encodePNG(t testing.TB, m image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, m))
	return b.Bytes()
}

// withSegment inserts a JPEG segment right after the start-of-image marker.
func withSegment(jpg []byte, marker byte, payload []byte) []byte {
	seg := []byte{0xFF, marker, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	out := append([]byte{}, jpg[:2]...)
	out = append(out, seg...)
	out = append(out, payload...)
	return append(out, jpg[2:]...)
}

// exifOrientation builds the payload of an APP1 segment holding one orientation tag.
func exifOrientation(o int) []byte {
	p := []byte("Exif\x00\x00MM\x00\x2a\x00\x00\x00\x08\x00\x01\x01\x12\x00\x03\x00\x00\x00\x01\x00\x00\x00\x00\x00\x00")
	binary.BigEndian.PutUint16(p[8+8+8:], uint16(o))
	return p
}

func mustSimilar(t *testing.T, a, b image.Image) float64 {
	t.Helper()
	require.Equal(t, a.Bounds().Dx(), b.Bounds().Dx(), "width")
	require.Equal(t, a.Bounds().Dy(), b.Bounds().Dy(), "height")
	s, err := quality.Similarity(a, b)
	require.NoError(t, err)
	return s
}

func optimize(t *testing.T, in Input) *Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := testEngine.Optimize(ctx, in)
	require.NoError(t, err)
	return res
}

func profile(t *testing.T, name string) *Profile {
	t.Helper()
	p, err := Lookup(name)
	require.NoError(t, err)
	return p
}

func TestJPEGFormatFollowsWhatTheClientAccepts(t *testing.T) {
	src := photoLike(200, 150, 1)
	raw := encodeJPEG(t, src, 95)

	tests := []struct {
		name string
		caps Caps
		want Format
	}{
		{"nothing modern", Caps{}, JPEG},
		{"webp", Caps{WebP: true}, WebP},
		{"avif and webp", Caps{WebP: true, AVIF: true}, AVIF},
		{"everything", Caps{WebP: true, AVIF: true, JXL: true}, JXL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := profile(t, ProfileBalanced)
			res := optimize(t, Input{Raw: raw, Format: JPEG, Caps: tt.caps, Profile: p})
			// the engine keeps the smallest it tried, so any format the client
			// accepts may win, and a format it did not list must never
			assert.True(t, tt.caps.Has(res.Format), "%s was not offered", res.Format)
			assert.Equal(t, res.Format, Sniff(res.Data), "the bytes are what the result says")
			if tt.want == JPEG {
				assert.Equal(t, JPEG, res.Format)
			}
			assert.Less(t, len(res.Data), len(raw), "recoding must pay off")
			assert.False(t, res.Lossless)

			// measure the result again, independently of the engine
			dec, err := testEngine.Backend().Decode(res.Format, res.Data)
			require.NoError(t, err)
			orig, err := stdjpeg.Decode(bytes.NewReader(raw))
			require.NoError(t, err)
			s := mustSimilar(t, orig, dec)
			assert.GreaterOrEqual(t, s, p.TargetSSIM-0.002, "must reach the target")
			assert.InDelta(t, res.Measure, s, 0.01, "the reported similarity must be the real one")
		})
	}
}

func TestTheSearchStopsNearTheTarget(t *testing.T) {
	raw := encodeJPEG(t, photoLike(200, 150, 2), 95)
	p := profile(t, ProfileBalanced)
	res := optimize(t, Input{Raw: raw, Format: JPEG, Caps: Caps{WebP: true}, Profile: p})
	assert.LessOrEqual(t, res.Attempts, p.Attempts)
	assert.GreaterOrEqual(t, res.Measure, p.TargetSSIM)
	assert.Less(t, res.Measure, p.TargetSSIM+0.03, "no point in spending bytes on quality nobody sees")
}

func TestProfilesTradeQualityForSize(t *testing.T) {
	raw := encodeJPEG(t, photoLike(220, 160, 3), 95)
	caps := Caps{WebP: true}
	sizes := map[string]int{}
	ssims := map[string]float64{}
	for _, name := range []string{ProfileBalanced, ProfileSaver, ProfileExtreme} {
		res := optimize(t, Input{Raw: raw, Format: JPEG, Caps: caps, Profile: profile(t, name)})
		sizes[name], ssims[name] = len(res.Data), res.Measure
		assert.GreaterOrEqual(t, res.Measure, profile(t, name).TargetSSIM, name)
	}
	assert.GreaterOrEqual(t, sizes[ProfileBalanced], sizes[ProfileSaver])
	assert.Greater(t, sizes[ProfileSaver], sizes[ProfileExtreme])
	assert.Greater(t, ssims[ProfileBalanced], ssims[ProfileExtreme])
	t.Logf("balanced %d bytes (ssim %.3f), saver %d (%.3f), extreme %d (%.3f), original %d",
		sizes[ProfileBalanced], ssims[ProfileBalanced], sizes[ProfileSaver], ssims[ProfileSaver], sizes[ProfileExtreme], ssims[ProfileExtreme], len(raw))
}

func TestLosslessProfileNeverChangesPixels(t *testing.T) {
	src := photoLike(120, 90, 4)
	raw := withSegment(encodeJPEG(t, src, 92), 0xE1, append([]byte("Exif\x00\x00"), bytes.Repeat([]byte{7}, 3000)...))
	res := optimize(t, Input{Raw: raw, Format: JPEG, Caps: Caps{WebP: true, AVIF: true}, Profile: profile(t, ProfileLossless)})
	assert.True(t, res.Lossless)
	assert.Equal(t, JPEG, res.Format)
	assert.Less(t, len(res.Data), len(raw), "the metadata went")

	before, err := stdjpeg.Decode(bytes.NewReader(raw))
	require.NoError(t, err)
	after, err := stdjpeg.Decode(bytes.NewReader(res.Data))
	require.NoError(t, err)
	assert.Equal(t, before, after, "stripping metadata must not change one pixel")
}

func TestExifOrientationIsAppliedToThePixels(t *testing.T) {
	src := photoLike(160, 100, 5)
	raw := withSegment(encodeJPEG(t, src, 95), 0xE1, exifOrientation(6))
	res := optimize(t, Input{Raw: raw, Format: JPEG, Caps: Caps{WebP: true}, Profile: profile(t, ProfileBalanced)})

	dec, err := testEngine.Backend().Decode(res.Format, res.Data)
	require.NoError(t, err)
	assert.Equal(t, 100, dec.Bounds().Dx(), "rotated: the old height is the new width")
	assert.Equal(t, 160, dec.Bounds().Dy())

	// the picture must be the source turned a quarter, not a different one
	turned := Orient(src, 6)
	right := mustSimilar(t, turned, dec)
	assert.GreaterOrEqual(t, right, 0.95)
	assert.Less(t, mustSimilar(t, Orient(src, 8), dec), right-0.2, "and not turned the other way")
}

func TestAnUnusualColourProfileIsNeverRecoded(t *testing.T) {
	src := photoLike(120, 90, 6)
	// a profile that is not sRGB: the colours would shift if it were dropped
	icc := make([]byte, 400)
	binary.BigEndian.PutUint32(icc[0:], uint32(len(icc)))
	copy(icc[12:], "mntr")
	copy(icc[16:], "RGB ")
	copy(icc[36:], "acsp")
	payload := append([]byte("ICC_PROFILE\x00\x01\x01"), icc...)
	raw := withSegment(encodeJPEG(t, src, 90), 0xE2, payload)

	res := optimize(t, Input{Raw: raw, Format: JPEG, Caps: Caps{WebP: true, AVIF: true, JXL: true}, Profile: profile(t, ProfileExtreme)})
	assert.True(t, res.Lossless, "pixels kept")
	assert.Equal(t, JPEG, res.Format)
	assert.Contains(t, string(res.Data), "ICC_PROFILE", "the profile must survive")
}

func TestResizeToTheViewport(t *testing.T) {
	raw := encodeJPEG(t, photoLike(1200, 800, 7), 92)
	hints := Hints{ViewportWidth: 400, DPR: 1.5}

	res := optimize(t, Input{Raw: raw, Format: JPEG, Caps: Caps{WebP: true}, Profile: profile(t, ProfileSaver), Hints: hints})
	assert.True(t, res.Resized)
	dec, err := testEngine.Backend().Decode(res.Format, res.Data)
	require.NoError(t, err)
	assert.Equal(t, 600, dec.Bounds().Dx())
	assert.Equal(t, 400, dec.Bounds().Dy())

	// balanced never changes the dimensions, whatever the hints say
	res = optimize(t, Input{Raw: raw, Format: JPEG, Caps: Caps{WebP: true}, Profile: profile(t, ProfileBalanced), Hints: hints})
	assert.False(t, res.Resized)
	dec, err = testEngine.Backend().Decode(res.Format, res.Data)
	require.NoError(t, err)
	assert.Equal(t, 1200, dec.Bounds().Dx())
}

func TestFixedQuality(t *testing.T) {
	raw := encodeJPEG(t, photoLike(200, 150, 8), 95)
	p := profile(t, ProfileBalanced)
	low := optimize(t, Input{Raw: raw, Format: JPEG, Profile: p, Fixed: 20})
	high := optimize(t, Input{Raw: raw, Format: JPEG, Profile: p, Fixed: 90})
	assert.Less(t, len(low.Data), len(high.Data))
	assert.Equal(t, 1, low.Attempts, "a fixed quality is encoded once")
	assert.Equal(t, 20, low.Quality)
}

func TestTheBudgetSteersToAFasterFormat(t *testing.T) {
	raw := encodeJPEG(t, photoLike(400, 300, 9), 95)
	p := profile(t, ProfileBalanced)
	everything := Caps{WebP: true, AVIF: true, JXL: true}

	roomy := optimize(t, Input{Raw: raw, Format: JPEG, Caps: everything, Profile: p, Budget: time.Minute})
	assert.NotEqual(t, JPEG, roomy.Format, "with time to spare a modern format is tried")

	// with 30 ms nothing modern fits for 0.12 megapixels, so it must not even try
	tight := optimize(t, Input{Raw: raw, Format: JPEG, Caps: everything, Profile: p, Budget: 30 * time.Millisecond})
	assert.Equal(t, JPEG, tight.Format, "falls back to the cheap format instead of timing out")
}

func TestACancelledJobStopsEarly(t *testing.T) {
	raw := encodeJPEG(t, photoLike(400, 300, 10), 95)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := testEngine.Optimize(ctx, Input{Raw: raw, Format: JPEG, Caps: Caps{WebP: true, AVIF: true}, Profile: profile(t, ProfileBalanced)})
	assert.Error(t, err)
	assert.Less(t, time.Since(start), 200*time.Millisecond)
}

func TestUnsupportedInput(t *testing.T) {
	ctx := context.Background()
	for name, in := range map[string]Input{
		"not an image":   {Raw: []byte("<html>"), Format: JPEG},
		"unknown format": {Raw: []byte("xx"), Format: Unknown},
		"already modern": {Raw: []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), Format: WebP},
		"truncated jpeg": {Raw: []byte{0xFF, 0xD8, 0xFF}, Format: JPEG},
		"truncated png":  {Raw: []byte("\x89PNG\r\n\x1a\n"), Format: PNG},
		"truncated gif":  {Raw: []byte("GIF89a"), Format: GIF},
	} {
		_, err := testEngine.Optimize(ctx, in)
		assert.ErrorIs(t, err, ErrUnsupported, name)
	}
}

func TestPNGGraphicStaysLossless(t *testing.T) {
	// flat colours and hard edges: a graphic, which must never be smudged
	g := image.NewNRGBA(image.Rect(0, 0, 180, 120))
	for y := range 120 {
		for x := range 180 {
			c := color.NRGBA{R: 240, G: 240, B: 240, A: 255}
			if (x/30+y/30)%2 == 0 {
				c = color.NRGBA{R: 20, G: 90, B: 200, A: 255}
			}
			if x > 60 && x < 120 && y > 40 && y < 80 {
				c = color.NRGBA{R: 230, G: 50, B: 40, A: 128}
			}
			g.SetNRGBA(x, y, c)
		}
	}
	raw := encodePNG(t, g)

	for _, name := range []string{ProfileLossless, ProfileBalanced, ProfileSaver} {
		t.Run(name, func(t *testing.T) {
			res := optimize(t, Input{Raw: raw, Format: PNG, Caps: Caps{WebP: true, AVIF: true, JXL: true}, Profile: profile(t, name)})
			assert.True(t, res.Lossless, "a graphic is never recoded lossily")
			assert.LessOrEqual(t, len(res.Data), len(raw))
			if res.Format == PNG || res.Format == JXL {
				dec, err := decodeAny(res)
				require.NoError(t, err)
				assertSamePixels(t, g, dec)
			}
		})
	}
}

func decodeAny(res *Result) (image.Image, error) {
	if res.Format == PNG {
		return png.Decode(bytes.NewReader(res.Data))
	}
	return testEngine.Backend().Decode(res.Format, res.Data)
}

func assertSamePixels(t *testing.T, a, b image.Image) {
	t.Helper()
	require.Equal(t, a.Bounds().Size(), b.Bounds().Size())
	for y := range a.Bounds().Dy() {
		for x := range a.Bounds().Dx() {
			ar, ag, ab, aa := color.NRGBA64Model.Convert(a.At(x, y)).RGBA()
			br, bg, bb, ba := color.NRGBA64Model.Convert(b.At(x, y)).RGBA()
			if aa != ba || (aa != 0 && (ar != br || ag != bg || ab != bb)) {
				require.Failf(t, "pixel differs", "at (%d,%d): %v vs %v", x, y, a.At(x, y), b.At(x, y))
			}
		}
	}
}

func TestPNGPhotoIsRecodedAgainstTheTarget(t *testing.T) {
	src := photoLike(200, 150, 11)
	raw := encodePNG(t, src)
	p := profile(t, ProfileBalanced)
	res := optimize(t, Input{Raw: raw, Format: PNG, Caps: Caps{WebP: true}, Profile: p})
	assert.False(t, res.Lossless)
	assert.Less(t, len(res.Data), len(raw)/2, "a photograph as PNG is mostly wasted bytes")
	assert.GreaterOrEqual(t, res.Measure, p.TargetSSIM)

	// the lossless profile must not touch it that way
	lossless := optimize(t, Input{Raw: raw, Format: PNG, Caps: Caps{WebP: true}, Profile: profile(t, ProfileLossless)})
	assert.True(t, lossless.Lossless)
}

func TestPNGWithTransparencyKeepsItWhenLossy(t *testing.T) {
	src := photoLike(160, 120, 12)
	n := image.NewNRGBA(src.Bounds())
	for y := range 120 {
		for x := range 160 {
			c := src.RGBAAt(x, y)
			a := uint8(255)
			if x < 40 {
				a = uint8(x * 255 / 40)
			}
			n.SetNRGBA(x, y, color.NRGBA{R: c.R, G: c.G, B: c.B, A: a})
		}
	}
	raw := encodePNG(t, n)

	res := optimize(t, Input{Raw: raw, Format: PNG, Caps: Caps{WebP: true}, Profile: profile(t, ProfileBalanced)})
	assert.Equal(t, WebP, res.Format)
	dec, err := testEngine.Backend().Decode(WebP, res.Data)
	require.NoError(t, err)
	_, _, _, a := dec.At(0, 60).RGBA()
	_, _, _, aMid := dec.At(20, 60).RGBA()
	_, _, _, aFull := dec.At(100, 60).RGBA()
	assert.Less(t, a, uint32(0x2000), "fully transparent at the left edge")
	assert.Greater(t, aMid, a)
	assert.Equal(t, uint32(0xffff), aFull)

	// without a modern format, JPEG is not an option because it has no alpha
	res = optimize(t, Input{Raw: raw, Format: PNG, Profile: profile(t, ProfileBalanced)})
	assert.Equal(t, PNG, res.Format, "stays a PNG")
}

func TestOpaqueAlphaChannelIsDropped(t *testing.T) {
	src := photoLike(160, 120, 16)
	n := image.NewNRGBA(src.Bounds())
	for y := range 120 {
		for x := range 160 {
			c := src.RGBAAt(x, y)
			n.SetNRGBA(x, y, color.NRGBA{R: c.R, G: c.G, B: c.B, A: 255})
		}
	}
	raw := encodePNG(t, n)
	res := optimize(t, Input{Raw: raw, Format: PNG, Profile: profile(t, ProfileBalanced)})
	assert.Equal(t, JPEG, res.Format, "no transparency, so a JPEG is possible even though the PNG had an alpha channel")

	n.SetNRGBA(3, 3, color.NRGBA{R: 1, G: 2, B: 3, A: 254})
	res = optimize(t, Input{Raw: encodePNG(t, n), Format: PNG, Profile: profile(t, ProfileBalanced)})
	assert.Equal(t, PNG, res.Format, "one translucent pixel and it must stay a PNG")
}

func TestAnimatedPNGIsLeftAlone(t *testing.T) {
	raw := encodePNG(t, photoLike(40, 40, 13))
	// insert an acTL chunk after IHDR: the file is now declared animated
	ihdrEnd := 8 + 8 + 13 + 4
	actl := []byte{0, 0, 0, 8, 'a', 'c', 'T', 'L', 0, 0, 0, 2, 0, 0, 0, 0, 0, 0, 0, 0}
	apng := append(append(append([]byte{}, raw[:ihdrEnd]...), actl...), raw[ihdrEnd:]...)
	_, err := testEngine.Optimize(context.Background(), Input{Raw: apng, Format: PNG, Caps: Caps{WebP: true}, Profile: profile(t, ProfileBalanced)})
	assert.ErrorIs(t, err, ErrUnsupported, "an animated PNG must not be flattened to one frame")
}

func animatedGIF(t testing.TB, frames, w, h int) []byte {
	t.Helper()
	g := &stdgif.GIF{LoopCount: 0}
	for f := range frames {
		img := image.NewPaletted(image.Rect(0, 0, w, h), palette.Plan9)
		for y := range h {
			for x := range w {
				img.SetColorIndex(x, y, uint8((x+f*9)/8+(y/8))) //nolint:gosec // small values
			}
		}
		g.Image = append(g.Image, img)
		g.Delay = append(g.Delay, 5)
		g.Disposal = append(g.Disposal, stdgif.DisposalNone)
	}
	var b bytes.Buffer
	require.NoError(t, stdgif.EncodeAll(&b, g))
	return b.Bytes()
}

// riffFrames counts the frames of an animated WebP.
func riffFrames(b []byte) int {
	return bytes.Count(b, []byte("ANMF"))
}

func TestAnimatedGIFBecomesAnimatedWebP(t *testing.T) {
	raw := animatedGIF(t, 6, 96, 96)
	res := optimize(t, Input{Raw: raw, Format: GIF, Caps: Caps{WebP: true}, Profile: profile(t, ProfileBalanced)})
	assert.Equal(t, WebP, res.Format)
	assert.Equal(t, 6, riffFrames(res.Data), "every frame must be there")
	assert.True(t, res.Lossless, "a 256 colour animation is kept exactly")

	// a client that can not show WebP keeps the GIF
	_, err := testEngine.Optimize(context.Background(), Input{Raw: raw, Format: GIF, Profile: profile(t, ProfileBalanced)})
	assert.ErrorIs(t, err, ErrUnsupported)
}

// noisyGIF is a photographic animation: dithered, so lossless coding hardly helps.
func noisyGIF(t testing.TB, frames, w, h int) []byte {
	t.Helper()
	g := &stdgif.GIF{}
	rng := rand.New(rand.NewPCG(9, 9))
	for f := range frames {
		src := photoLike(w, h, uint64(f+1))
		img := image.NewPaletted(image.Rect(0, 0, w, h), palette.Plan9)
		draw := func() {
			for y := range h {
				for x := range w {
					c := src.RGBAAt(x, y)
					img.Set(x, y, color.RGBA{R: c.R, G: c.G, B: c.B, A: 255})
				}
			}
		}
		draw()
		_ = rng
		g.Image = append(g.Image, img)
		g.Delay = append(g.Delay, 5)
	}
	var b bytes.Buffer
	require.NoError(t, stdgif.EncodeAll(&b, g))
	return b.Bytes()
}

func TestLossyAnimationInSaver(t *testing.T) {
	raw := noisyGIF(t, 4, 120, 90)
	lossless := optimize(t, Input{Raw: raw, Format: GIF, Caps: Caps{WebP: true}, Profile: profile(t, ProfileBalanced)})
	lossy := optimize(t, Input{Raw: raw, Format: GIF, Caps: Caps{WebP: true}, Profile: profile(t, ProfileSaver)})
	assert.True(t, lossless.Lossless)
	assert.False(t, lossy.Lossless, "a photographic animation is worth coding with loss in saver")
	assert.Equal(t, 4, riffFrames(lossy.Data))
	assert.Less(t, len(lossy.Data), len(lossless.Data))
}

func TestFlatAnimationStaysLosslessEvenInSaver(t *testing.T) {
	raw := animatedGIF(t, 6, 96, 96)
	res := optimize(t, Input{Raw: raw, Format: GIF, Caps: Caps{WebP: true}, Profile: profile(t, ProfileSaver)})
	assert.True(t, res.Lossless, "when lossless is the smaller, it wins")
	assert.Equal(t, 6, riffFrames(res.Data))
}

func TestAnimationBecomesStillOnlyInExtreme(t *testing.T) {
	raw := animatedGIF(t, 6, 96, 96)
	res := optimize(t, Input{Raw: raw, Format: GIF, Caps: Caps{WebP: true}, Profile: profile(t, ProfileExtreme)})
	assert.NotEqual(t, 6, riffFrames(res.Data))
	assert.False(t, res.Lossless, "dropping frames is a loss and is reported as one")
	assert.Less(t, len(res.Data), len(raw))
}

func TestGIFFramesComposeLikeABrowser(t *testing.T) {
	// frame 0 paints everything red; frame 1 is a small blue patch that is
	// removed afterwards (restore background); frame 2 is a green patch on
	// what remained.
	red := color.RGBA{R: 255, A: 255}
	blue := color.RGBA{B: 255, A: 255}
	green := color.RGBA{G: 255, A: 255}
	pal := color.Palette{color.Transparent, red, blue, green}

	f0 := image.NewPaletted(image.Rect(0, 0, 8, 8), pal)
	for i := range f0.Pix {
		f0.Pix[i] = 1
	}
	f1 := image.NewPaletted(image.Rect(2, 2, 4, 4), pal)
	for i := range f1.Pix {
		f1.Pix[i] = 2
	}
	f2 := image.NewPaletted(image.Rect(5, 5, 7, 7), pal)
	for i := range f2.Pix {
		f2.Pix[i] = 3
	}
	g := &stdgif.GIF{
		Image:    []*image.Paletted{f0, f1, f2},
		Delay:    []int{1, 1, 1},
		Disposal: []byte{stdgif.DisposalNone, stdgif.DisposalBackground, stdgif.DisposalNone},
		Config:   image.Config{Width: 8, Height: 8},
	}
	frames := composeFrames(g, 3)
	require.Len(t, frames, 3)

	at := func(f int, x, y int) color.NRGBA { return color.NRGBAModel.Convert(frames[f].At(x, y)).(color.NRGBA) }
	assert.Equal(t, color.NRGBA{R: 255, A: 255}, at(0, 3, 3))
	assert.Equal(t, color.NRGBA{B: 255, A: 255}, at(1, 3, 3), "the patch shows on frame 1")
	assert.Equal(t, color.NRGBA{A: 0}, at(2, 3, 3), "and its area is cleared to transparent afterwards")
	assert.Equal(t, color.NRGBA{R: 255, A: 255}, at(2, 0, 0), "while the rest stays")
	assert.Equal(t, color.NRGBA{G: 255, A: 255}, at(2, 5, 5))
}

func TestDelaysAndLoopsMatchWhatBrowsersDo(t *testing.T) {
	assert.Equal(t, []int{100, 100, 20, 500}, browserDelays([]int{0, 1, 2, 50}), "0 and 1 mean 100 ms")
	assert.Equal(t, 1, webpLoops(-1), "once")
	assert.Equal(t, 0, webpLoops(0), "forever")
	assert.Equal(t, 4, webpLoops(3), "n+1 plays")
}

func TestStillGIFBecomesPNGOrWebP(t *testing.T) {
	still := animatedGIF(t, 1, 100, 100)
	res := optimize(t, Input{Raw: still, Format: GIF, Profile: profile(t, ProfileBalanced)})
	assert.Contains(t, []Format{PNG, GIF}, res.Format)
	assert.LessOrEqual(t, len(res.Data), len(still))
	assert.True(t, res.Lossless)
}

func TestConcurrentUse(t *testing.T) {
	raw := encodeJPEG(t, photoLike(120, 90, 14), 95)
	pngRaw := encodePNG(t, photoLike(100, 80, 15))
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 2 {
				in := Input{Raw: raw, Format: JPEG, Caps: Caps{WebP: true}, Profile: profile(t, ProfileBalanced)}
				if g%2 == 1 {
					in = Input{Raw: pngRaw, Format: PNG, Caps: Caps{WebP: true}, Profile: profile(t, ProfileSaver)}
				}
				res, err := testEngine.Optimize(context.Background(), in)
				if assert.NoError(t, err) {
					assert.NotEmpty(t, res.Data)
				}
			}
		}()
	}
	wg.Wait()
}

func BenchmarkOptimizeJPEG(b *testing.B) {
	raw := encodeJPEG(b, photoLike(768, 512, 20), 90)
	for _, c := range []struct {
		name string
		caps Caps
	}{{"jpegli", Caps{}}, {"webp", Caps{WebP: true}}, {"avif", Caps{WebP: true, AVIF: true}}, {"jxl", Caps{WebP: true, AVIF: true, JXL: true}}} {
		b.Run(c.name, func(b *testing.B) {
			p, _ := Lookup(ProfileBalanced)
			b.ReportAllocs()
			for b.Loop() {
				if _, err := testEngine.Optimize(context.Background(), Input{Raw: raw, Format: JPEG, Caps: c.caps, Profile: p}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
