package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Device is one authorized device. Only the SHA-256 of its token is stored.
type Device struct {
	ID          string    `json:"id"`
	TokenSHA256 string    `json:"token_sha256"`
	Profile     string    `json:"profile,omitempty"`
	Created     time.Time `json:"created"`
}

type deviceFile struct {
	Devices []Device `json:"devices"`
}

// Store is a JSON-file-backed device registry safe for concurrent use within a process.
type Store struct {
	mu      sync.Mutex
	path    string
	devices []Device
}

// OpenStore loads the devices file at path. When create is true a missing file
// is created empty; otherwise a missing file is an error.
func OpenStore(path string, create bool) (*Store, error) {
	s := &Store{path: path}
	devices, err := readDevices(path)
	switch {
	case err == nil:
		s.devices = devices
	case create && errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create devices directory: %w", err)
		}
		if err := s.persist(); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	return s, nil
}

func readDevices(path string) ([]Device, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read devices file: %w", err)
	}
	var f deviceFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse devices file: %w", err)
	}
	seen := make(map[string]struct{}, len(f.Devices))
	for _, d := range f.Devices {
		if !deviceIDPattern.MatchString(d.ID) {
			return nil, fmt.Errorf("devices file: invalid device id %q", d.ID)
		}
		if _, dup := seen[d.ID]; dup {
			return nil, fmt.Errorf("devices file: duplicate device id %q", d.ID)
		}
		seen[d.ID] = struct{}{}
		if _, err := decodeDigest(d.TokenSHA256); err != nil {
			return nil, fmt.Errorf("devices file: device %q: %w", d.ID, err)
		}
	}
	return f.Devices, nil
}

func decodeDigest(s string) ([sha256.Size]byte, error) {
	var out [sha256.Size]byte
	if len(s) != hex.EncodedLen(sha256.Size) {
		return out, errors.New("token_sha256 must be 64 hex characters")
	}
	if _, err := hex.Decode(out[:], []byte(s)); err != nil {
		return out, fmt.Errorf("token_sha256: %w", err)
	}
	return out, nil
}

// Add registers a new device and returns its freshly generated token, which is
// not recoverable afterwards.
func (s *Store) Add(id, profile string) (string, error) {
	if !deviceIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid device id %q: must match %s", id, deviceIDPattern)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if d.ID == id {
			return "", fmt.Errorf("device %q already exists", id)
		}
	}
	prev := s.devices
	s.devices = append(append([]Device(nil), prev...), Device{
		ID:          id,
		TokenSHA256: hex.EncodeToString(sum[:]),
		Profile:     profile,
		Created:     time.Now().UTC().Truncate(time.Second),
	})
	if err := s.persist(); err != nil {
		s.devices = prev
		return "", err
	}
	return token, nil
}

// Revoke removes a device.
func (s *Store) Revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, d := range s.devices {
		if d.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("device %q not found", id)
	}
	prev := s.devices
	next := make([]Device, 0, len(prev)-1)
	next = append(next, prev[:idx]...)
	next = append(next, prev[idx+1:]...)
	s.devices = next
	if err := s.persist(); err != nil {
		s.devices = prev
		return err
	}
	return nil
}

// List returns a copy of the devices sorted by ID.
func (s *Store) List() []Device {
	s.mu.Lock()
	out := append([]Device(nil), s.devices...)
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// persist must be called with s.mu held (or before the store is shared).
func (s *Store) persist() error {
	devices := s.devices
	if devices == nil {
		devices = []Device{}
	}
	data, err := json.MarshalIndent(deviceFile{Devices: devices}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode devices: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".devices-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		return cleanup(fmt.Errorf("chmod temp file: %w", err))
	}
	if _, err := tmp.Write(data); err != nil {
		return cleanup(fmt.Errorf("write temp file: %w", err))
	}
	if err := tmp.Sync(); err != nil {
		return cleanup(fmt.Errorf("sync temp file: %w", err))
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename devices file: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
