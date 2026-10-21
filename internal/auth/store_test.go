package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreAddRevokeList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "devices.json")
	s, err := OpenStore(path, true)
	require.NoError(t, err)
	assert.Empty(t, s.List())

	tokB, err := s.Add("b-phone", "mobile")
	require.NoError(t, err)
	tokA, err := s.Add("a.laptop", "")
	require.NoError(t, err)
	assert.Len(t, tokA, 43)
	assert.NotEqual(t, tokA, tokB)

	list := s.List()
	require.Len(t, list, 2)
	assert.Equal(t, "a.laptop", list[0].ID)
	assert.Equal(t, "b-phone", list[1].ID)
	sum := sha256.Sum256([]byte(tokB))
	assert.Equal(t, hex.EncodeToString(sum[:]), list[1].TokenSHA256)
	assert.Equal(t, "mobile", list[1].Profile)

	list[0].ID = "mutated"
	assert.Equal(t, "a.laptop", s.List()[0].ID)

	s2, err := OpenStore(path, false)
	require.NoError(t, err)
	assert.Equal(t, s.List(), s2.List())

	require.NoError(t, s.Revoke("a.laptop"))
	assert.Error(t, s.Revoke("a.laptop"))
	s3, err := OpenStore(path, false)
	require.NoError(t, err)
	require.Len(t, s3.List(), 1)
	assert.Equal(t, "b-phone", s3.List()[0].ID)
}

func TestStorePermissionsAndNoTempLeftovers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg")
	path := filepath.Join(dir, "devices.json")
	s, err := OpenStore(path, true)
	require.NoError(t, err)
	_, err = s.Add("x", "")
	require.NoError(t, err)

	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	di, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), di.Mode().Perm())

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestStoreInvalidInput(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "d.json"), true)
	require.NoError(t, err)
	for _, id := range []string{"", "has space", "a/b", "ü", string(make([]byte, 65)), "a:b"} {
		_, err := s.Add(id, "")
		assert.Error(t, err, "id %q", id)
	}
	_, err = s.Add("dup", "")
	require.NoError(t, err)
	_, err = s.Add("dup", "")
	assert.Error(t, err)
	assert.Len(t, s.List(), 1)
}

func TestOpenStoreErrors(t *testing.T) {
	dir := t.TempDir()
	_, err := OpenStore(filepath.Join(dir, "missing.json"), false)
	assert.Error(t, err)

	bad := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte("{nope"), 0o600))
	_, err = OpenStore(bad, true)
	assert.Error(t, err)

	invalid := filepath.Join(dir, "invalid.json")
	require.NoError(t, os.WriteFile(invalid, []byte(`{"devices":[{"id":"a","token_sha256":"zz"}]}`), 0o600))
	_, err = OpenStore(invalid, false)
	assert.Error(t, err)
}

func TestStoreConcurrentAdd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.json")
	s, err := OpenStore(path, true)
	require.NoError(t, err)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Add(fmt.Sprintf("dev-%d", i), "")
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	assert.Len(t, s.List(), 20)
	s2, err := OpenStore(path, false)
	require.NoError(t, err)
	assert.Len(t, s2.List(), 20)
}
