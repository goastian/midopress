// Package pngopt optimizes PNG images without cgo or external binaries.
//
// Optimize is lossless: the output decodes to exactly the same pixels. In
// 8-bit images the colour of fully transparent pixels is normalized to black
// because it is not visible; 16-bit images keep it, as decoders report it.
// Quantize is lossy palette reduction and Encode writes any image through the
// same optimized pipeline.
//
// Memory use is proportional to the pixel count. An 8-bit image needs about
// 12 bytes per pixel at peak (canonical raster, packed scanlines and a
// filtered copy), a 16-bit image about 24 bytes per pixel, so the largest
// accepted image (MaxPixels, 64 Mpixel) can need up to about 1.5 GiB. Images
// larger than MaxPixels are refused, and the pixel count a decoder accepts is
// also bounded by the compressed size (a deflate stream cannot expand by more
// than about 1032:1). Callers facing hostile traffic should limit the input
// size. Running time is linear in the pixel count; the deflate search
// effort shrinks as images grow.
package pngopt

import "image"

// Level selects the effort spent searching for a smaller file.
type Level int

const (
	// Fast makes one pass with a single heuristic filter strategy and a
	// fast deflate level.
	Fast Level = iota
	// Default ranks several filter strategies on samples of the image and
	// compresses the winners with a strong deflate level.
	Default
	// Best tries every strategy, several palette orders and the strongest
	// deflate setting.
	Best
)

// Optimize returns an equivalent PNG that decodes to exactly the same pixels
// (same colours, same alpha) and is not larger than the input. When it can
// not make the file smaller it returns the input slice itself.
//
// Lossless reductions applied: truecolour or grey to palette (<= 256 colours
// including alpha, using tRNS), palette to 1/2/4 bits, truecolour to grey,
// grey to low bit depth when exact, 16-bit to 8-bit when every sample is v*257,
// removal of an opaque alpha channel and of unused palette entries, removal of
// ancillary chunks other than sRGB, gAMA, cHRM, iCCP, cICP, mDCV, cLLI, sBIT,
// pHYs and eXIf, filter and deflate search, and Adam7 de-interlacing. sBIT is
// dropped when the new pixel layout makes it invalid. When an iCCP chunk is
// present the grey/colour model is not changed, as the profile is tied to it.
// Animated PNG (acTL) is returned untouched. Malformed input yields
// (nil, err).
func Optimize(raw []byte, level Level) ([]byte, error) {
	out, err := optimize(raw, level)
	if err != nil {
		return nil, err
	}
	if out == nil || len(out) >= len(raw) {
		return raw, nil
	}
	return out, nil
}

// optimize re-encodes raw without the not-larger guarantee. It returns a nil
// slice for animated PNG.
func optimize(raw []byte, level Level) ([]byte, error) {
	p, err := parse(raw)
	if err != nil {
		return nil, err
	}
	if p.animated {
		return nil, nil
	}
	r, err := decodeRaster(p)
	if err != nil {
		return nil, err
	}
	return build(r, metaFrom(p), level)
}

// Encode writes img as a PNG using the same optimized filter and deflate
// pipeline as Optimize. Paletted images get the smallest bit depth that fits
// their used colours. Images with premultiplied alpha such as *image.RGBA are
// stored as non-premultiplied 8-bit, which rounds colours of low alpha.
func Encode(img image.Image, level Level) ([]byte, error) {
	r, err := fromImage(img)
	if err != nil {
		return nil, err
	}
	return build(r, &meta{}, level)
}

// UniqueColors counts the distinct non-premultiplied colours of img, with all
// fully transparent pixels counting as one colour. If more than limit
// distinct colours exist it stops and returns (limit, true); otherwise it
// returns the exact count and false. A limit <= 0 means no limit.
func UniqueColors(img image.Image, limit int) (n int, truncated bool) {
	b := img.Bounds()
	if b.Empty() {
		return 0, false
	}
	seen := make(map[uint64]struct{})
	add := func(k uint64) bool {
		seen[k] = struct{}{}
		return limit > 0 && len(seen) > limit
	}
	over := false
	switch im := img.(type) {
	case *image.NRGBA:
		w := b.Dx()
		for y := 0; y < b.Dy() && !over; y++ {
			row := im.Pix[y*im.Stride : y*im.Stride+4*w]
			for i := 0; i < len(row); i += 4 {
				k := uint64(row[i])<<24 | uint64(row[i+1])<<16 | uint64(row[i+2])<<8 | uint64(row[i+3])
				if row[i+3] == 0 {
					k = 0
				}
				if add(k) {
					over = true
					break
				}
			}
		}
	default:
		r, err := fromImage(img)
		if err != nil {
			return 0, false
		}
		ps := r.pixSize()
		for i := 0; i+ps <= len(r.pix) && !over; i += ps {
			var k uint64
			for _, v := range r.pix[i : i+ps] {
				k = k<<8 | uint64(v)
			}
			over = add(k)
		}
	}
	if over {
		return limit, true
	}
	return len(seen), false
}
