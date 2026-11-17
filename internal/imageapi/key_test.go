package imageapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSigningKeyIsPrivatePersistentAndValidated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "image.key")
	key, err := LoadKey(path)
	require.NoError(t, err)
	assert.Len(t, key, 32)
	stored, err := LoadKey(path)
	require.NoError(t, err)
	assert.Equal(t, key, stored)
	info, err := os.Stat(path)
	require.NoError(t, err)
	if os.PathSeparator == '/' {
		assert.EqualValues(t, 0600, info.Mode().Perm())
	}
	require.NoError(t, os.WriteFile(path, []byte("invalid-private-key"), 0600))
	_, err = LoadKey(path)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "invalid-private-key")
}
