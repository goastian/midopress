package img

import (
	"runtime"
	"sync"
	"testing"

	"github.com/gen2brain/jpegxl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnsafeNativeJXLPixelEncoderCannotBeSelectedOrCalled(t *testing.T) {
	if jpegxl.Dynamic() != nil {
		t.Skip("native JPEG XL library is not loaded")
	}
	be := NewWASM()
	require.True(t, be.Native(JXL), "the decoder still uses the loaded native library")
	require.False(t, be.Supports(JXL))
	for _, lossless := range []bool{false, true} {
		data, err := be.Encode(JXL, photoLike(96, 64, 2), EncodeOptions{Quality: 80, Lossless: lossless})
		require.ErrorIs(t, err, ErrUnsupported)
		assert.Empty(t, data)
	}
	engine := NewEngine(be)
	in := Input{Raw: make([]byte, avifMinBytes), Caps: Caps{JXL: true, AVIF: true, WebP: true}, Profile: profile(t, ProfileBalanced)}
	assert.NotContains(t, engine.formatsFor(in, false), JXL)
	assert.Contains(t, engine.formatsFor(in, false), AVIF)
	assert.Contains(t, engine.formatsFor(in, false), WebP)
	info := RuntimeInfo(be)[JXL.String()]
	assert.Equal(t, "disabled", info.Encoder)
	assert.Equal(t, "native", info.Decoder)
	assert.Contains(t, info.Reason, "garbage collection")
}

func TestNativeJXLDecoderAndReversibleBindingUnderGC(t *testing.T) {
	skipWithoutJXL(t)
	if jpegxl.Dynamic() != nil {
		t.Skip("native JPEG XL decoder is not loaded")
	}
	be := NewWASM()
	jpg := baselineJPEG(t, 257, 193, 13)
	jxl, err := be.RecompressJPEG(jpg)
	require.NoError(t, err)
	workers := min(runtime.GOMAXPROCS(0), 4)
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Go(func() {
			for range 20 {
				runtime.GC()
				got, err := be.Decode(JXL, jxl)
				if err != nil {
					errs <- err
					return
				}
				if got.Bounds().Dx() != 257 || got.Bounds().Dy() != 193 {
					errs <- assert.AnError
					return
				}
				back, err := ReconstructJPEG(jxl)
				if err != nil {
					errs <- err
					return
				}
				if string(back) != string(jpg) {
					errs <- assert.AnError
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}
