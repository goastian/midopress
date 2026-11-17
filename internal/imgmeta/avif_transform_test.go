package imgmeta

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func associatedAVIF(props [][]byte, indices []byte) []byte {
	pitm := fullBox("pitm", []byte{0, 7})
	ipma := fullBox("ipma", u32(1), []byte{0, 7, byte(len(indices))}, indices)
	return append(ftyp("avif"), fullBox("meta", pitm, box("iprp", box("ipco", props...), ipma))...)
}

func TestAVIFPresentationUsesPrimaryAssociationOrder(t *testing.T) {
	props := [][]byte{ispe(6, 4), box("irot", []byte{1}), box("imir", []byte{0}), box("clap", u32(4, 1, 2, 1, 0, 1, 0, 1))}
	raw := associatedAVIF(props, []byte{1, 4, 2, 3})
	got, err := AVIFPresentation(raw)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []string{"clap", "irot", "imir"}, []string{got[0].Kind, got[1].Kind, got[2].Kind})
	assert.Equal(t, [8]int64{4, 1, 2, 1, 0, 1, 0, 1}, got[0].Aperture)
	meta, err := ParseAVIF(raw)
	require.NoError(t, err)
	assert.True(t, meta.Transformed)
	assert.Equal(t, 6, meta.Width)
}

func TestAVIFPrimaryColorDoesNotComeFromUnusedProperties(t *testing.T) {
	props := [][]byte{ispe(6, 4), colrNCLX(12, 13, 1, true), colrNCLX(9, 16, 9, true), box("colr", []byte("profignored")), box("irot", []byte{2})}
	raw := associatedAVIF(props, []byte{1, 2})
	meta, err := ParseAVIF(raw)
	require.NoError(t, err)
	assert.Empty(t, meta.ICC)
	assert.Equal(t, []AVIFColour{{Primaries: 12, Transfer: 13, Matrix: 1, FullRange: true}}, meta.NCLX)
	assert.False(t, meta.Transformed)
	transforms, err := AVIFPresentation(raw)
	require.NoError(t, err)
	assert.Empty(t, transforms)
}

func TestAVIFPresentationRejectsAmbiguousAndMalformedProperties(t *testing.T) {
	for name, raw := range map[string][]byte{
		"missing association": avif("avif", ispe(6, 4), box("irot", []byte{1})),
		"invalid index":       associatedAVIF([][]byte{ispe(6, 4)}, []byte{1, 2}),
		"invalid rotation":    associatedAVIF([][]byte{ispe(6, 4), box("irot", []byte{4})}, []byte{1, 2}),
		"invalid mirror":      associatedAVIF([][]byte{ispe(6, 4), box("imir", []byte{2})}, []byte{1, 2}),
		"truncated aperture":  associatedAVIF([][]byte{ispe(6, 4), box("clap", u32(1))}, []byte{1, 2}),
		"repeated transform":  associatedAVIF([][]byte{ispe(6, 4), box("irot", []byte{1})}, []byte{1, 2, 2}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := AVIFPresentation(raw)
			assert.Error(t, err)
		})
	}
	// Version 1 uses 32-bit item IDs; flag 1 uses 16-bit property indices.
	ipma := box("ipma", []byte{1, 0, 0, 1}, u32(1, 70000), []byte{2, 0x80, 1, 0, 2})
	full := append(ftyp("avif"), fullBox("meta", box("pitm", []byte{1, 0, 0, 0}, u32(70000)), box("iprp", box("ipco", ispe(6, 4), box("irot", []byte{3})), ipma))...)
	got, err := AVIFPresentation(full)
	require.NoError(t, err)
	assert.Equal(t, []AVIFTransform{{Kind: "irot", Value: 3}}, got)
	for n := 0; n < len(full); n++ {
		assert.NotPanics(t, func() { _, _ = AVIFPresentation(full[:n]) })
	}
}
