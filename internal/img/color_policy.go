package img

import (
	"bytes"

	"github.com/goastian/midopress/internal/imgmeta"
)

// NeedsColorConversion conservatively identifies resources whose color would
// need normalization before an sRGB encoder or comparison. preserve leaves
// these resources original; srgb explicitly permits the conversion. A standard
// ICC tag is also retained rather than guessing equivalence from a profile name.
func NeedsColorConversion(raw []byte, f Format) bool {
	switch f {
	case JPEG:
		meta, err := imgmeta.ParseJPEG(raw)
		return err == nil && (meta.HasICC || len(meta.ICC) > 0 || meta.Components == 4)
	case PNG:
		meta, err := imgmeta.ParsePNG(raw)
		if err != nil {
			return false
		}
		return meta.InvalidColor || meta.HasICC || len(meta.ICC) > 0 ||
			!meta.HasSRGB && (meta.HasGamma || meta.HasChroma) ||
			meta.HasCICP && meta.CICP != [4]uint8{1, 13, 0, 1}
	case WebP:
		meta, err := imgmeta.ParseWebP(raw)
		return err == nil && len(meta.ICC) > 0
	case AVIF:
		meta, err := imgmeta.ParseAVIF(raw)
		if err != nil {
			return false
		}
		if len(meta.ICC) > 0 {
			return true
		}
		for _, c := range meta.NCLX {
			if c.Primaries != 1 && c.Primaries != 2 || c.Transfer != 13 && c.Transfer != 2 {
				return true
			}
		}
	case JXL:
		meta, err := imgmeta.ParseJXL(raw)
		return err == nil && (!meta.OriginalSRGB || jxlNeedsToneMap(meta) || meta.ExponentBits > 0)
	case GIF:
		return bytes.Contains(raw, []byte("ICCRGBG1012"))
	}
	return false
}

func jxlNeedsToneMap(meta *imgmeta.JXL) bool {
	return meta.IntensityTarget > 255 || meta.TransferFunction == 16 || meta.TransferFunction == 18
}

func jxlHasUnsupportedChannels(meta *imgmeta.JXL) bool {
	allowed := 0
	if meta.Alpha {
		allowed = 1
	}
	return meta.ExtraChannels > allowed
}

func jxlHasUnclassifiedTransfer(meta *imgmeta.JXL) bool {
	return !meta.OriginalSRGB && meta.TransferFunction == 0
}
