package server_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/quality"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertServedImagePolicy(t *testing.T, original, result []byte, profileName string) {
	t.Helper()
	if bytes.Equal(original, result) {
		return
	}
	profile, err := img.Lookup(profileName)
	require.NoError(t, err)
	backend := img.NewWASM()
	a, err := img.DecodeDisplay(context.Background(), backend, img.Sniff(original), original)
	require.NoError(t, err)
	b, err := img.DecodeDisplay(context.Background(), backend, img.Sniff(result), result)
	require.NoError(t, err)
	if a.Bounds().Size() != b.Bounds().Size() {
		b = img.Resize(b, a.Bounds().Dx(), a.Bounds().Dy())
	}
	ref, err := quality.NewDSSIMReference(a, quality.Options{MaxPixels: a.Bounds().Dx() * a.Bounds().Dy()})
	require.NoError(t, err)
	distance, err := ref.Distance(b)
	require.NoError(t, err)
	assert.LessOrEqual(t, distance, profile.TargetDSSIM, "the served variant must satisfy the profile at full resolution")
	assert.Less(t, len(result), len(original))
}
