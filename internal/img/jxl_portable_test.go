//go:build nodynamic

package img

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJPEGRecompressionWithoutDynamicLibraries(t *testing.T) {
	require.False(t, jxlNativeAvailable())
	require.True(t, JPEGRecompressAvailable())
	require.Equal(t, "wasm", JPEGRecompressionExecutionPath())
	be := NewWASM()
	require.False(t, be.Native(JXL))
	for _, raw := range [][]byte{baselineJPEG(t, 33, 77, 2), withEXIF(t, baselineJPEG(t, 321, 243, 3))} {
		encoded, err := be.RecompressJPEG(raw)
		require.NoError(t, err)
		back, err := ReconstructJPEG(encoded)
		require.NoError(t, err)
		assert.Equal(t, raw, back)
	}
}
