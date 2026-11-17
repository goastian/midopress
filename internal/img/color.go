package img

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"

	"github.com/goastian/midopress/internal/colorcms"
	"github.com/goastian/midopress/internal/imgmeta"
)

func managedPixels(ctx context.Context, src image.Image, icc []byte) (image.Image, error) {
	if len(icc) == 0 {
		return src, nil
	}
	if imgmeta.ClassifyICC(icc) == imgmeta.ICCInvalid {
		return nil, whyNotModern("invalid colour profile")
	}
	converted, err := colorcms.ToSRGB(ctx, src, icc)
	if err != nil {
		return nil, fmt.Errorf("%w: colour conversion: %v", ErrUnsupported, err)
	}
	return converted, nil
}

// DecodeDisplay returns a still image in sRGB with its presentation geometry
// applied. The reference is shared by optimization and isolated comparison so
// an embedded ICC profile or orientation cannot silently change the score.
// Unsupported HDR and ambiguous metadata fail closed; the caller can keep the
// original resource. Animations require comparison of all frames instead.
func DecodeDisplay(ctx context.Context, be Backend, format Format, raw []byte) (image.Image, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var src image.Image
	var icc []byte
	var err error
	orientation := 1
	switch format {
	case JPEG:
		meta, err := imgmeta.ParseJPEG(raw)
		if err != nil || meta.Components == 4 && len(meta.ICC) == 0 {
			return nil, whyNotModern("unreadable JPEG or untagged CMYK")
		}
		if meta.HasICC && len(meta.ICC) == 0 {
			return nil, whyNotModern("incomplete JPEG colour profile")
		}
		src, err = jpeg.Decode(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		icc, orientation = meta.ICC, meta.Orientation
	case PNG:
		meta, err := imgmeta.ParsePNG(raw)
		if err != nil || meta.Animated {
			return nil, whyNotModern("animated or unreadable PNG")
		}
		src, err = png.Decode(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		icc, err = pngProfile(ctx, meta)
		if err != nil {
			return nil, err
		}
		orientation = meta.Orientation
	case WebP:
		meta, err := imgmeta.ParseWebP(raw)
		if err != nil || meta.Animated {
			return nil, whyNotModern("animated or unreadable header (WebP)")
		}
		icc, orientation = meta.ICC, meta.Orientation
	case AVIF:
		meta, err := imgmeta.ParseAVIF(raw)
		if err != nil || meta.Animated {
			return nil, whyNotModern("animated or unreadable AVIF")
		}
		icc = meta.ICC
		if len(icc) == 0 && len(meta.NCLX) > 0 {
			c := meta.NCLX[0]
			for _, other := range meta.NCLX[1:] {
				if other != c {
					return nil, whyNotModern("ambiguous colour space")
				}
			}
			icc, err = cicpProfile(ctx, c.Primaries, c.Transfer)
			if err != nil {
				return nil, whyNotModern("unsupported HDR or colour space")
			}
		}
	case JXL:
		meta, err := imgmeta.ParseJXL(raw)
		if err != nil || meta.Animated {
			return nil, whyNotModern("animated or unreadable JPEG XL")
		}
		if jxlNeedsToneMap(meta) {
			return nil, whyNotModern("JPEG XL HDR requires explicit tone mapping")
		}
		if jxlHasUnsupportedChannels(meta) {
			return nil, whyNotModern("JPEG XL auxiliary channels must be preserved")
		}
		if jxlHasUnclassifiedTransfer(meta) {
			return nil, whyNotModern("JPEG XL transfer function must be classified before color conversion")
		}
		// The portable JPEG XL decoder applies its color encoding and
		// orientation, and returns sRGB NRGBA64. Never transform it twice.
	default:
		return nil, ErrUnsupported
	}
	if src == nil {
		src, err = be.Decode(format, raw)
		if err != nil {
			return nil, err
		}
	}
	if format == AVIF {
		transforms, err := imgmeta.AVIFPresentation(raw)
		if err != nil {
			return nil, whyNotModern("ambiguous container transform")
		}
		src, err = avifPresentation(src, transforms)
		if err != nil {
			return nil, err
		}
	}
	src, err = managedPixels(ctx, src, icc)
	if err != nil {
		return nil, err
	}
	return Orient(src, orientation), nil
}
