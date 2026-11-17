package codecworker

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"

	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/imgmeta"
)

func Dimensions(raw []byte, f img.Format) (int, int, error) {
	switch f {
	case img.JPEG:
		c, err := jpeg.DecodeConfig(bytes.NewReader(raw))
		return c.Width, c.Height, err
	case img.PNG:
		c, err := png.DecodeConfig(bytes.NewReader(raw))
		return c.Width, c.Height, err
	case img.GIF:
		c, err := gif.DecodeConfig(bytes.NewReader(raw))
		return c.Width, c.Height, err
	case img.WebP:
		c, err := imgmeta.ParseWebP(raw)
		if err == nil {
			return c.Width, c.Height, nil
		}
		return 0, 0, err
	case img.AVIF:
		c, err := imgmeta.ParseAVIF(raw)
		if err == nil {
			return c.Width, c.Height, nil
		}
		return 0, 0, err
	case img.JXL:
		c, err := imgmeta.ParseJXL(raw)
		if err == nil {
			return c.Width, c.Height, nil
		}
		return 0, 0, err
	}
	return 0, 0, img.ErrUnsupported
}

func CheckPixels(raw []byte, f img.Format, limit int) error {
	var w, h int
	var err error
	switch f {
	case img.JPEG:
		var c image.Config
		c, err = jpeg.DecodeConfig(bytes.NewReader(raw))
		w, h = c.Width, c.Height
	case img.PNG:
		var c image.Config
		c, err = png.DecodeConfig(bytes.NewReader(raw))
		w, h = c.Width, c.Height
	case img.GIF:
		var c image.Config
		c, err = gif.DecodeConfig(bytes.NewReader(raw))
		w, h = c.Width, c.Height
		if err == nil {
			err = checkGIF(raw, limit)
		}
	case img.WebP:
		m, e := imgmeta.ParseWebP(raw)
		err = e
		if e == nil {
			w, h = m.Width, m.Height
		}
	case img.AVIF:
		m, e := imgmeta.ParseAVIF(raw)
		err = e
		if e == nil {
			w, h = m.Width, m.Height
		}
	case img.JXL:
		m, e := imgmeta.ParseJXL(raw)
		err = e
		if e == nil {
			w, h = m.Width, m.Height
		}
	default:
		return img.ErrUnsupported
	}
	if err != nil || w < 1 || h < 1 || w > limit/h {
		return errors.New("invalid image dimensions or pixel limit exceeded")
	}
	return nil
}

func checkGIF(raw []byte, limit int) error {
	if len(raw) < 13 {
		return img.ErrUnsupported
	}
	w := int(binary.LittleEndian.Uint16(raw[6:]))
	h := int(binary.LittleEndian.Uint16(raw[8:]))
	pos := 13
	if raw[10]&128 != 0 {
		pos += 3 * (2 << (raw[10] & 7))
	}
	frames := 0
	for pos < len(raw) {
		b := raw[pos]
		pos++
		switch b {
		case 0x3b:
			if frames > 0 {
				return nil
			}
			return img.ErrUnsupported
		case 0x21:
			pos++
		case 0x2c:
			if pos+9 >= len(raw) {
				return img.ErrUnsupported
			}
			frames++
			if int64(frames)*int64(w)*int64(h) > int64(limit) {
				return ErrWorker
			}
			packed := raw[pos+8]
			pos += 9
			if packed&128 != 0 {
				pos += 3 * (2 << (packed & 7))
			}
			pos++
		default:
			return img.ErrUnsupported
		}
		for {
			if pos >= len(raw) {
				return img.ErrUnsupported
			}
			n := int(raw[pos])
			pos++
			if n == 0 {
				break
			}
			pos += n
		}
	}
	return img.ErrUnsupported
}
