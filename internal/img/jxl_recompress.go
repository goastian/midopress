package img

import (
	"bytes"
	"errors"
	"fmt"
	stdjpeg "image/jpeg"

	"github.com/goastian/midopress/internal/jxlcodec"
)

const (
	// maxJPEGRecompressBytes bounds the inputs and outputs handled here.
	maxJPEGRecompressBytes = 64 << 20
	// maxJPEGRecompressPixels keeps libjxl's working memory in check.
	maxJPEGRecompressPixels = 256 << 20
)

// JPEGRecompressAvailable includes the embedded portable implementation.
func JPEGRecompressAvailable() bool { return true }

func JPEGRecompressionExecutionPath() string {
	if jxlNativeAvailable() {
		return "native"
	}
	return "wasm"
}

// RecompressJPEG stores the quantized DCT coefficients of a JPEG in a JPEG XL
// file, losslessly. The original is rebuilt bit for bit by ReconstructJPEG. The
// result is verified before it is returned, so a nil error guarantees the
// round trip. The embedded WebAssembly implementation needs no shared library.
func (*WASM) RecompressJPEG(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > maxJPEGRecompressBytes {
		return nil, fmt.Errorf("%w: jpeg size %d out of range", ErrUnsupported, len(raw))
	}
	if cfg, err := stdjpeg.DecodeConfig(bytes.NewReader(raw)); err == nil && int64(cfg.Width)*int64(cfg.Height) > maxJPEGRecompressPixels {
		return nil, fmt.Errorf("%w: jpeg of %dx%d is too large", ErrUnsupported, cfg.Width, cfg.Height)
	}
	encode, reconstruct := jxlcodec.Recompress, jxlcodec.Reconstruct
	if jxlNativeAvailable() {
		encode = jxlNativeEncodeJPEG
		reconstruct = func(b []byte) ([]byte, error) { return jxlNativeReconstructJPEG(b, len(raw)) }
	}
	jxl, err := encode(raw)
	if err != nil {
		return nil, err
	}
	back, err := reconstruct(jxl)
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
// reconstruction data are an error. An embedded WebAssembly fallback is available
// when the native library cannot be loaded.
func ReconstructJPEG(jxl []byte) ([]byte, error) {
	if len(jxl) == 0 || len(jxl) > maxJPEGRecompressBytes {
		return nil, fmt.Errorf("img: jxl size %d out of range", len(jxl))
	}
	if jxlNativeAvailable() {
		return jxlNativeReconstructJPEG(jxl, len(jxl)+len(jxl)/4)
	}
	return jxlcodec.Reconstruct(jxl)
}
