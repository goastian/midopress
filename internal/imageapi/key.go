package imageapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func LoadKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		key, err := hex.DecodeString(strings.TrimSpace(string(data)))
		if err != nil || len(key) != 32 {
			return nil, errors.New("image signing key must contain 32 bytes encoded as hex")
		}
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return LoadKey(path)
	}
	if err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	_, err = f.WriteString(hex.EncodeToString(key) + "\n")
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	return key, closeErr
}
