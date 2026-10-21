package img

import (
	"errors"
	"image"
)

// EncodeOptions tune one encoder call. Quality means different things to
// different formats, which is why the engine searches for it by measuring.
type EncodeOptions struct {
	Quality  int  // 1..100, ignored when Lossless is set
	Lossless bool // bit-exact (JPEG has no lossless mode and ignores it)
	// Effort trades CPU for size: 0 picks the format's default. The scale is
	// the engine's own, 1 (fastest) to 10 (slowest), mapped to each format.
	Effort int
	// Subsampling is the chroma subsampling of JPEG and AVIF, 4:2:0 when zero.
	Subsampling image.YCbCrSubsampleRatio
	// Progressive asks for a progressive JPEG.
	Progressive bool
}

// Animation is a sequence of full-canvas frames.
type Animation struct {
	Frames    []image.Image
	DelaysMS  []int // per frame, milliseconds
	LoopCount int   // 0 loops forever
}

// Backend implements the encoders and decoders of the modern formats. The
// default one is built on WebAssembly and needs no C libraries; others can be
// plugged in, for example one on libvips.
type Backend interface {
	Name() string
	// Supports reports whether the backend can encode f.
	Supports(f Format) bool
	Encode(f Format, img image.Image, o EncodeOptions) ([]byte, error)
	EncodeAnimation(f Format, a *Animation, o EncodeOptions) ([]byte, error)
	// Decode turns an encoded image back into pixels, to measure what an
	// encoder did to it.
	Decode(f Format, data []byte) (image.Image, error)
	// Native reports whether encoding f runs native code, which is an order of
	// magnitude faster than the WebAssembly one.
	Native(f Format) bool
}

// JPEGRecompressor is implemented by backends that can turn a JPEG into a JPEG
// XL file without losing a bit, and back.
type JPEGRecompressor interface {
	RecompressJPEG(raw []byte) ([]byte, error)
}

var (
	// ErrUnsupported means the image is of a kind that is left alone.
	ErrUnsupported = errors.New("img: unsupported image")
	// ErrNotSmaller means nothing smaller than the original was found.
	ErrNotSmaller = errors.New("img: no smaller representation found")
	// ErrBudget means the image is too costly to recode within the time budget.
	ErrBudget = errors.New("img: recoding does not fit in the time budget")
)
