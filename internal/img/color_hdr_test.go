package img

import (
	"context"
	"image"
	"testing"

	"github.com/goastian/midopress/internal/imgmeta"
	"github.com/goastian/midopress/internal/jxlcodec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJXLHDRRequiresAnExplicitToneMappingPolicy(t *testing.T) {
	for _, transfer := range []uint32{16, 18} {
		raw, err := jxlcodec.Encode([]image.Image{photoLike(32, 32, 3)}, nil, 0,
			jxlcodec.Options{Quality: 90, Effort: 1, Primaries: 9, TransferFunction: transfer})
		require.NoError(t, err)
		require.True(t, NeedsColorConversion(raw, JXL))
		p := profile(t, ProfileBalanced)
		in := Input{Raw: raw, Format: JXL, Profile: p, Caps: Caps{WebP: true}}
		e := NewEngine(NewWASM())
		result, err := e.Optimize(context.Background(), in)
		require.NoError(t, err)
		assert.Equal(t, raw, result.Data)
		assert.False(t, result.ColorConverted)
		p.ColorPolicy = "srgb"
		_, err = e.Optimize(context.Background(), in)
		assert.ErrorIs(t, err, ErrUnsupported, "SDR sRGB opt-in does not authorize HDR tone mapping")
		_, err = DecodeDisplay(context.Background(), e.be, JXL, raw)
		assert.ErrorIs(t, err, ErrUnsupported)
	}
}

func TestJXLUnknownTransferRequiresPreservation(t *testing.T) {
	// Synthetic metadata tests the policy boundary; the generated Display P3
	// fixture is recognized by libjxl and is not a non-enumerable ICC corpus.
	for _, test := range []struct {
		originalSRGB bool
		transfer     uint32
		preserve     bool
	}{
		{false, 0, true}, {true, 0, false}, {true, 13, false}, {false, 13, false},
	} {
		meta := &imgmeta.JXL{OriginalSRGB: test.originalSRGB, TransferFunction: test.transfer}
		assert.Equal(t, test.preserve, jxlHasUnclassifiedTransfer(meta))
	}
}

func TestJXLAuxiliaryChannelPolicyPreservesAnythingBeyondRGBA(t *testing.T) {
	for _, test := range []struct {
		alpha       bool
		extra       int
		unsupported bool
	}{
		{false, 0, false}, {true, 1, false}, {false, 1, true}, {true, 2, true}, {false, 3, true},
	} {
		meta := &imgmeta.JXL{Alpha: test.alpha, ExtraChannels: test.extra}
		assert.Equal(t, test.unsupported, jxlHasUnsupportedChannels(meta))
	}
}
