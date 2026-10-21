package codec

import (
	"bytes"
	"errors"
	"image"
	stdgif "image/gif"
	stdjpeg "image/jpeg"
	"image/png"
	"sync"

	"github.com/chai2010/webp"
	"github.com/pixiv/go-libjpeg/jpeg"
)

// ImageKind is an image format the proxy can recode.
type ImageKind uint8

const (
	KindUnknown ImageKind = iota
	KindJPEG
	KindPNG
	KindGIF
)

// SniffImage identifies the format from the magic number.
func SniffImage(b []byte) ImageKind {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return KindJPEG
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return KindPNG
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return KindGIF
	}
	return KindUnknown
}

// ImageSize reads the dimensions from the header without decoding the pixels.
func ImageSize(kind ImageKind, raw []byte) (width, height int, err error) {
	var cfg image.Config
	r := bytes.NewReader(raw)
	switch kind {
	case KindJPEG:
		cfg, err = stdjpeg.DecodeConfig(r)
	case KindPNG:
		cfg, err = png.DecodeConfig(r)
	case KindGIF:
		cfg, err = stdgif.DecodeConfig(r)
	default:
		err = errors.New("codec: unknown image format")
	}
	return cfg.Width, cfg.Height, err
}

var errBadGIF = errors.New("codec: malformed GIF")

// GIFFrames counts the image frames of a GIF by walking its block structure,
// without decompressing any pixel data. It stops early once limit frames have
// been seen.
func GIFFrames(b []byte, limit int) (int, error) {
	if len(b) < 13 || (string(b[:6]) != "GIF87a" && string(b[:6]) != "GIF89a") {
		return 0, errBadGIF
	}
	pos := 13
	if b[10]&0x80 != 0 {
		pos += 3 << (b[10]&7 + 1)
	}
	frames := 0
	for pos < len(b) {
		switch b[pos] {
		case 0x3B:
			return frames, nil
		case 0x21:
			pos += 2
			var ok bool
			if pos, ok = skipSubBlocks(b, pos); !ok {
				return frames, errBadGIF
			}
		case 0x2C:
			frames++
			if frames >= limit {
				return frames, nil
			}
			if pos+10 > len(b) {
				return frames, errBadGIF
			}
			flags := b[pos+9]
			pos += 10
			if flags&0x80 != 0 {
				pos += 3 << (flags&7 + 1)
			}
			pos++ // LZW minimum code size
			var ok bool
			if pos, ok = skipSubBlocks(b, pos); !ok {
				return frames, errBadGIF
			}
		default:
			return frames, errBadGIF
		}
	}
	return frames, errBadGIF
}

func skipSubBlocks(b []byte, pos int) (int, bool) {
	for pos < len(b) {
		n := int(b[pos])
		pos++
		if n == 0 {
			return pos, true
		}
		pos += n
	}
	return pos, false
}

// Image is the result of recoding an image.
type Image struct {
	Data        []byte
	ContentType string
}

const webpType = "image/webp"

var pngBuffers = &pngPool{}

type pngPool struct{ p sync.Pool }

func (b *pngPool) Get() *png.EncoderBuffer {
	buf, _ := b.p.Get().(*png.EncoderBuffer)
	return buf
}

func (b *pngPool) Put(buf *png.EncoderBuffer) { b.p.Put(buf) }

// RecodeJPEG decodes a JPEG and encodes it again at the given quality, as
// WebP when toWebP is set.
func RecodeJPEG(raw []byte, quality int, toWebP bool) (Image, error) {
	img, err := jpeg.Decode(bytes.NewReader(raw), &jpeg.DecoderOptions{})
	if err != nil {
		return Image{}, err
	}
	var out bytes.Buffer
	out.Grow(len(raw) / 2)
	if toWebP {
		err = webp.Encode(&out, img, &webp.Options{Quality: float32(quality)})
		return Image{Data: out.Bytes(), ContentType: webpType}, err
	}
	err = jpeg.Encode(&out, img, &jpeg.EncoderOptions{Quality: quality, OptimizeCoding: true})
	return Image{Data: out.Bytes(), ContentType: "image/jpeg"}, err
}

// RecodePNG decodes a PNG and encodes it again, as lossless WebP when toWebP
// is set.
func RecodePNG(raw []byte, toWebP bool) (Image, error) {
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return Image{}, err
	}
	var out bytes.Buffer
	out.Grow(len(raw))
	if toWebP {
		err = webp.Encode(&out, img, &webp.Options{Lossless: true})
		return Image{Data: out.Bytes(), ContentType: webpType}, err
	}
	enc := png.Encoder{CompressionLevel: png.BestCompression, BufferPool: pngBuffers}
	err = enc.Encode(&out, img)
	return Image{Data: out.Bytes(), ContentType: "image/png"}, err
}

// RecodeGIF decodes the first frame of a GIF and encodes it again, as lossless
// WebP when toWebP is set. Animated GIFs must be filtered out beforehand.
func RecodeGIF(raw []byte, toWebP bool) (Image, error) {
	img, err := stdgif.Decode(bytes.NewReader(raw))
	if err != nil {
		return Image{}, err
	}
	var out bytes.Buffer
	out.Grow(len(raw))
	if toWebP {
		err = webp.Encode(&out, img, &webp.Options{Lossless: true})
		return Image{Data: out.Bytes(), ContentType: webpType}, err
	}
	err = stdgif.Encode(&out, img, nil)
	return Image{Data: out.Bytes(), ContentType: "image/gif"}, err
}
