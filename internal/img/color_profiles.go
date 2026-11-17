package img

import (
	"context"

	"github.com/goastian/midopress/internal/colorcms"
	"github.com/goastian/midopress/internal/imgmeta"
)

var srgbChroma = [8]float64{.3127, .3290, .64, .33, .30, .60, .15, .06}

func cicpProfile(ctx context.Context, primaries, transfer int) ([]byte, error) {
	chroma := srgbChroma
	switch primaries {
	case 1, 2:
	case 9:
		chroma = [8]float64{.3127, .3290, .708, .292, .170, .797, .131, .046}
	case 12:
		chroma = [8]float64{.3127, .3290, .68, .32, .265, .69, .15, .06}
	default:
		return nil, whyNotModern("unsupported colour primaries")
	}
	if transfer == 2 {
		transfer = 13
	}
	return colorcms.RGBProfile(ctx, chroma, transfer, 0)
}

func pngProfile(ctx context.Context, meta *imgmeta.PNG) ([]byte, error) {
	if meta.InvalidColor {
		return nil, whyNotModern("invalid PNG colour metadata")
	}
	if meta.HasCICP {
		if meta.CICP[2] != 0 || meta.CICP[3] != 1 {
			return nil, whyNotModern("invalid PNG colour coding")
		}
		return cicpProfile(ctx, int(meta.CICP[0]), int(meta.CICP[1]))
	}
	if len(meta.ICC) > 0 {
		return meta.ICC, nil
	}
	if meta.HasSRGB || !meta.HasGamma && !meta.HasChroma {
		return nil, nil
	}
	chroma := srgbChroma
	if meta.HasChroma {
		chroma = meta.Chromaticities
	}
	if meta.HasGamma {
		return colorcms.RGBProfile(ctx, chroma, 0, meta.Gamma)
	}
	return colorcms.RGBProfile(ctx, chroma, 13, 0)
}
