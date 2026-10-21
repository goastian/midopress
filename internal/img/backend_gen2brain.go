package img

import (
	"bytes"
	"fmt"
	"image"
	stdjpeg "image/jpeg"

	"github.com/gen2brain/avif"
	"github.com/gen2brain/jpegli"
	"github.com/gen2brain/jpegxl"
	"github.com/gen2brain/webp"
)

// WASM is the default backend. It needs no C library and no cgo: the codecs run
// as WebAssembly inside the process. If the shared libraries of libwebp,
// libavif or libjxl happen to be installed, the codec packages load them and run
// native code instead, with no change on our side.
type WASM struct{}

// NewWASM returns the default backend.
func NewWASM() *WASM { return &WASM{} }

func (*WASM) Name() string { return "wasm" }

func (*WASM) Supports(f Format) bool {
	switch f {
	case JPEG, WebP, AVIF, JXL:
		return true
	}
	return false
}

func (*WASM) Native(f Format) bool {
	switch f {
	case WebP:
		return webp.Dynamic() == nil
	case AVIF:
		return avif.Dynamic() == nil
	case JXL:
		return jpegxl.Dynamic() == nil
	}
	return false
}

// webpMethod maps the engine's effort (1 fastest, 10 slowest) to libwebp's
// method (0 fastest, 6 slowest).
func webpMethod(effort int) int {
	if effort <= 0 {
		return 4
	}
	return min((effort-1)*6/9, 6)
}

// avifSpeed maps the effort to libavif's speed, where 10 is the fastest.
func avifSpeed(effort int) int {
	if effort <= 0 {
		return 8
	}
	return max(10-(effort-1)*10/9, 0)
}

func jxlEffort(effort int) int {
	if effort <= 0 {
		return 5
	}
	return min(effort, 10)
}

func (*WASM) Encode(f Format, m image.Image, o EncodeOptions) ([]byte, error) {
	var b bytes.Buffer
	var err error
	switch f {
	case WebP:
		err = webp.Encode(&b, m, webp.Options{Quality: o.Quality, Lossless: o.Lossless, Method: webpMethod(o.Effort)})
	case AVIF:
		sub := o.Subsampling
		if sub == 0 {
			sub = image.YCbCrSubsampleRatio420
		}
		err = avif.Encode(&b, m, avif.Options{Quality: o.Quality, QualityAlpha: o.Quality, Speed: avifSpeed(o.Effort), ChromaSubsampling: sub, Lossless: o.Lossless})
	case JXL:
		err = jpegxl.Encode(&b, m, jpegxl.Options{Quality: o.Quality, Effort: jxlEffort(o.Effort), Lossless: o.Lossless})
	case JPEG:
		sub := o.Subsampling
		if sub == 0 {
			sub = image.YCbCrSubsampleRatio420
		}
		level := 0
		if o.Progressive {
			level = 2
		}
		err = jpegli.Encode(&b, m, &jpegli.EncodingOptions{
			Quality:              o.Quality,
			ChromaSubsampling:    sub,
			ProgressiveLevel:     level,
			OptimizeCoding:       true,
			AdaptiveQuantization: true,
			FancyDownsampling:    true,
		})
	default:
		return nil, fmt.Errorf("img: the %s backend can not encode %s", "wasm", f)
	}
	if err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (*WASM) EncodeAnimation(f Format, a *Animation, o EncodeOptions) ([]byte, error) {
	var b bytes.Buffer
	var err error
	switch f {
	case WebP:
		err = webp.EncodeAll(&b, &webp.WEBP{Image: a.Frames, Delay: a.DelaysMS, LoopCount: a.LoopCount},
			webp.Options{Quality: o.Quality, Lossless: o.Lossless, Method: webpMethod(o.Effort)})
	case AVIF:
		delays := make([]float64, len(a.DelaysMS))
		for i, d := range a.DelaysMS {
			delays[i] = float64(d) / 1000
		}
		sub := o.Subsampling
		if sub == 0 {
			sub = image.YCbCrSubsampleRatio420
		}
		err = avif.EncodeAll(&b, &avif.AVIF{Image: a.Frames, Delay: delays, LoopCount: a.LoopCount},
			avif.Options{Quality: o.Quality, QualityAlpha: o.Quality, Speed: avifSpeed(o.Effort), ChromaSubsampling: sub, Lossless: o.Lossless})
	default:
		return nil, fmt.Errorf("img: the %s backend can not encode animated %s", "wasm", f)
	}
	if err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (*WASM) Decode(f Format, data []byte) (image.Image, error) {
	r := bytes.NewReader(data)
	switch f {
	case JPEG:
		return stdjpeg.Decode(r)
	case WebP:
		return webp.Decode(r)
	case AVIF:
		return avif.Decode(r)
	case JXL:
		return jpegxl.Decode(r)
	}
	return nil, fmt.Errorf("img: can not decode %s", f)
}
