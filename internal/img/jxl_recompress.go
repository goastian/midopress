package img

import (
	"bytes"
	"errors"
	"fmt"
	stdjpeg "image/jpeg"
)

const (
	// maxJPEGRecompressBytes bounds the inputs and outputs handled here.
	maxJPEGRecompressBytes = 64 << 20
	// maxJPEGRecompressPixels keeps libjxl's working memory in check.
	maxJPEGRecompressPixels = 256 << 20
)

// JPEGRecompressAvailable reports whether libjxl could be loaded, which is what
// RecompressJPEG and ReconstructJPEG need.
func JPEGRecompressAvailable() bool { return jxlNativeAvailable() }

// RecompressJPEG stores the quantized DCT coefficients of a JPEG in a JPEG XL
// file, losslessly. The original is rebuilt bit for bit by ReconstructJPEG. The
// result is verified before it is returned, so a nil error guarantees the
// round trip. It returns ErrUnsupported when libjxl is not available.
func (*WASM) RecompressJPEG(raw []byte) ([]byte, error) {
	if !jxlNativeAvailable() {
		return nil, ErrUnsupported
	}
	if len(raw) == 0 || len(raw) > maxJPEGRecompressBytes {
		return nil, fmt.Errorf("%w: jpeg size %d out of range", ErrUnsupported, len(raw))
	}
	if cfg, err := stdjpeg.DecodeConfig(bytes.NewReader(raw)); err == nil && cfg.Width*cfg.Height > maxJPEGRecompressPixels {
		return nil, fmt.Errorf("%w: jpeg of %dx%d is too large", ErrUnsupported, cfg.Width, cfg.Height)
	}
	jxl, err := jxlNativeEncodeJPEG(raw)
	if err != nil {
		return nil, err
	}
	back, err := jxlNativeReconstructJPEG(jxl, len(raw))
	if err != nil {
		return nil, fmt.Errorf("img: verifying jpeg reconstruction: %w", err)
	}
	if !bytes.Equal(back, raw) {
		return nil, errors.New("img: jpeg reconstruction is not bit exact")
	}
	return jxl, nil
}

// ReconstructJPEG rebuilds the original JPEG from a JPEG XL file made by
// RecompressJPEG or by libjxl's own tools from a JPEG. Files that carry no
// reconstruction data are an error. It returns ErrUnsupported when libjxl is not
// available.
func ReconstructJPEG(jxl []byte) ([]byte, error) {
	if !jxlNativeAvailable() {
		return nil, ErrUnsupported
	}
	if len(jxl) == 0 || len(jxl) > maxJPEGRecompressBytes {
		return nil, fmt.Errorf("img: jxl size %d out of range", len(jxl))
	}
	return jxlNativeReconstructJPEG(jxl, len(jxl)+len(jxl)/4)
}
