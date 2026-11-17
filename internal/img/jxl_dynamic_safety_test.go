package img

import (
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJXLPixelCodecUsesPortableModuleInsteadOfUnsafeNativeBinding(t *testing.T) {
	be := NewWASM()
	require.False(t, be.Native(JXL))
	require.True(t, be.Supports(JXL))
	for _, lossless := range []bool{false, true} {
		data, err := be.Encode(JXL, photoLike(96, 64, 2), EncodeOptions{Quality: 80, Lossless: lossless})
		require.NoError(t, err)
		assert.NotEmpty(t, data)
		got, err := be.Decode(JXL, data)
		require.NoError(t, err)
		assert.Equal(t, photoLike(96, 64, 2).Bounds(), got.Bounds())
	}
	engine := NewEngine(be)
	in := Input{Raw: make([]byte, avifMinBytes), Caps: Caps{JXL: true, AVIF: true, WebP: true}, Profile: profile(t, ProfileBalanced)}
	assert.Contains(t, engine.formatsFor(in, false), JXL)
	assert.Contains(t, engine.formatsFor(in, false), AVIF)
	assert.Contains(t, engine.formatsFor(in, false), WebP)
	info := RuntimeInfo(be)[JXL.String()]
	assert.Equal(t, "wasm", info.Encoder)
	assert.Equal(t, "wasm", info.Decoder)
	assert.Contains(t, info.Reason, "WASI")
}

func TestNativeJXLDecoderAndReversibleBindingUnderGC(t *testing.T) {
	skipWithoutJXL(t)
	be := NewWASM()
	jpg := baselineJPEG(t, 257, 193, 13)
	jxl, err := be.RecompressJPEG(jpg)
	require.NoError(t, err)
	workers := min(runtime.GOMAXPROCS(0), 2)
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Go(func() {
			for range 3 {
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
