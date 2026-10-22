package img

import "fmt"

// Profile says how far an image may be pushed. Every profile keeps the image
// visually the same to a person looking at it; what changes is how strict the
// measurement is, and which tricks are allowed.
type Profile struct {
	Name string

	// Lossless allows only bit-exact work: stripping metadata, optimizing PNG,
	// lossless recoding. Nothing else is touched.
	Lossless bool

	// A lossy result must reach the target of the metric in use against the
	// decoded source, and the engine searches for the smallest file that does.
	// TargetDSSIM is the largest dssim distance, TargetSSIM the smallest
	// similarity in [0,1] and TargetSSIMULACRA2 the smallest score.
	TargetDSSIM       float64
	TargetSSIM        float64
	TargetSSIMULACRA2 float64
	// Attempts is how many encodes the search may spend on one format.
	Attempts int

	// Formats is the order in which modern formats are tried for lossy
	// results. A client has to accept a format for it to be used. JPEG is
	// always the last resort.
	Formats []Format

	// ResizeToViewport shrinks images that are much larger than the screen,
	// when the client says how big it is.
	ResizeToViewport bool
	// PhotoColors is how many distinct colours make a PNG a photograph, which
	// may be recoded lossily. Fewer colours is a graphic and stays lossless.
	PhotoColors int
	// QuantizePNG allows reducing a graphic to 256 colours when that is
	// much smaller.
	QuantizePNG bool
	// AnimatedToStill replaces an animation with its first frame.
	AnimatedToStill bool
	// AnimatedQuality is the quality of lossy animations, 0 for lossless.
	AnimatedQuality int

	// Text is the compression level used for text, by encoding.
	Text TextLevels
}

// TextLevels are the compression levels of a profile.
type TextLevels struct {
	Brotli, Gzip, Zstd int
	// BrotliStatic is used for responses that look cacheable, where a slow
	// level pays for itself because it is paid once.
	BrotliStatic int
}

// Built-in profile names.
const (
	ProfileLossless = "lossless"
	ProfileBalanced = "balanced"
	ProfileSaver    = "saver"
	ProfileExtreme  = "extreme"
)

// Profiles returns the built-in profiles by name.
func Profiles() map[string]*Profile {
	formats := []Format{JXL, AVIF, WebP}
	return map[string]*Profile{
		ProfileLossless: {
			Name: ProfileLossless, Lossless: true, TargetSSIM: 1, TargetSSIMULACRA2: 100, Formats: formats,
			PhotoColors: 1 << 30,
			Text:        TextLevels{Brotli: 6, Gzip: 6, Zstd: 3, BrotliStatic: 9},
		},
		ProfileBalanced: {
			Name: ProfileBalanced, TargetDSSIM: 0.002, TargetSSIM: 0.975, TargetSSIMULACRA2: 85, Attempts: 4, Formats: formats,
			PhotoColors: 8192,
			Text:        TextLevels{Brotli: 6, Gzip: 6, Zstd: 3, BrotliStatic: 9},
		},
		ProfileSaver: {
			Name: ProfileSaver, TargetDSSIM: 0.006, TargetSSIM: 0.95, TargetSSIMULACRA2: 75, Attempts: 3, Formats: formats,
			ResizeToViewport: true, PhotoColors: 4096, AnimatedQuality: 80,
			Text: TextLevels{Brotli: 7, Gzip: 7, Zstd: 5, BrotliStatic: 11},
		},
		ProfileExtreme: {
			Name: ProfileExtreme, TargetDSSIM: 0.015, TargetSSIM: 0.90, TargetSSIMULACRA2: 60, Attempts: 3, Formats: formats,
			ResizeToViewport: true, PhotoColors: 1024, QuantizePNG: true,
			AnimatedToStill: true, AnimatedQuality: 65,
			Text: TextLevels{Brotli: 7, Gzip: 9, Zstd: 7, BrotliStatic: 11},
		},
	}
}

// Lookup returns a built-in profile or an error naming the valid ones.
func Lookup(name string) (*Profile, error) {
	if p, ok := Profiles()[name]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("img: unknown profile %q, use lossless, balanced, saver or extreme", name)
}
