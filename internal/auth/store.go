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

	"github.com/goastian/midopress/internal/filelock"
	"github.com/goastian/midopress/internal/quota"
)

var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Device is one authorized device. Only the SHA-256 of its token is stored.
type Device struct {
	ID          string        `json:"id"`
	TokenSHA256 string        `json:"token_sha256"`
	Profile     string        `json:"profile,omitempty"`
	Created     time.Time     `json:"created"`
	Quotas      *quota.Limits `json:"quotas,omitempty"`
}

type deviceFile struct {
	Devices      []Device     `json:"devices"`
	Registration Registration `json:"registration"`
}

type Registration struct {
	MaxDevices int   `json:"max_devices"`
	PerMinute  int   `json:"per_minute"`
	Window     int64 `json:"window"`
	Created    int   `json:"created"`
}

var ErrRegistrationLimit = errors.New("device registration limit reached")

// Store is a JSON-file-backed device registry safe for concurrent use within a process.
type Store struct {
	mu           sync.Mutex
	path         string
	devices      []Device
	registration Registration
}

// OpenStore loads the devices file at path. When create is true a missing file
// is created empty; otherwise a missing file is an error.
func OpenStore(path string, create bool) (*Store, error) {
	s := &Store{path: path}
	if create {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
	}
	unlock, err := filelock.Acquire(path+".lock", false)
	if err != nil {
		return nil, err
	}
	defer unlock()
	state, err := readDeviceState(path)
	switch {
	case err == nil:
		s.devices, s.registration = state.Devices, state.Registration
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
	f, err := readDeviceState(path)
	return f.Devices, err
}

func readDeviceState(path string) (deviceFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return deviceFile{}, fmt.Errorf("read devices file: %w", err)
	}
	var f deviceFile
	if len(data) > 16<<20 {
		return f, errors.New("devices file exceeds limit")
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("parse devices file: %w", err)
	}
	if len(f.Devices) > 10000 {
		return f, errors.New("too many devices")
	}
	seen := make(map[string]struct{}, len(f.Devices))
	for _, d := range f.Devices {
		if !deviceIDPattern.MatchString(d.ID) {
			return f, fmt.Errorf("devices file: invalid device id %q", d.ID)
		}
		if _, dup := seen[d.ID]; dup {
			return f, fmt.Errorf("devices file: duplicate device id %q", d.ID)
		}
		seen[d.ID] = struct{}{}
		if _, err := decodeDigest(d.TokenSHA256); err != nil {
			return f, fmt.Errorf("devices file: device %q: %w", d.ID, err)
		}
		if d.Quotas != nil && !d.Quotas.Valid() {
			return f, errors.New("invalid device quotas")
		}
	}
	if f.Registration.MaxDevices == 0 {
		f.Registration.MaxDevices = 128
	}
	if f.Registration.PerMinute == 0 {
		f.Registration.PerMinute = 20
	}
	if f.Registration.MaxDevices < 1 || f.Registration.MaxDevices > 10000 || f.Registration.PerMinute < 1 || f.Registration.PerMinute > 10000 || f.Registration.Created < 0 {
		return f, errors.New("invalid registration limits")
	}
	return f, nil
}

func (s *Store) mutation() (func(), error) {
	unlock, err := filelock.Acquire(s.path+".lock", false)
	if err != nil {
		return nil, err
	}
	f, err := readDeviceState(s.path)
	if err != nil {
		unlock()
		return nil, err
	}
	s.devices, s.registration = f.Devices, f.Registration
	return unlock, nil
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
	return s.AddWithQuotas(id, profile, nil)
}

func (s *Store) AddWithQuotas(id, profile string, limits *quota.Limits) (string, error) {
	if limits != nil {
		if !limits.Valid() {
			return "", errors.New("invalid quotas")
		}
		copy := *limits
		limits = &copy
	}
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
	unlock, err := s.mutation()
	if err != nil {
		return "", err
	}
	defer unlock()
	for _, d := range s.devices {
		if d.ID == id {
			return "", fmt.Errorf("device %q already exists", id)
		}
	}
	now := time.Now().Unix() / 60
	if now > s.registration.Window {
		s.registration.Window = now
		s.registration.Created = 0
	}
	if len(s.devices) >= s.registration.MaxDevices || s.registration.Created >= s.registration.PerMinute {
		return "", ErrRegistrationLimit
	}
	previousRegistration := s.registration
	s.registration.Created++
	prev := s.devices
	s.devices = append(append([]Device(nil), prev...), Device{
		ID:          id,
		TokenSHA256: hex.EncodeToString(sum[:]),
		Profile:     profile,
		Created:     time.Now().UTC().Truncate(time.Second),
		Quotas:      limits,
	})
	if err := s.persist(); err != nil {
		s.devices = prev
		s.registration = previousRegistration
		return "", err
	}
	return token, nil
}

// Revoke removes a device.
func (s *Store) Revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.mutation()
	if err != nil {
		return err
	}
	defer unlock()
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
	for i := range out {
		if out[i].Quotas != nil {
			copy := *out[i].Quotas
			out[i].Quotas = &copy
		}
	}
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
	data, err := json.MarshalIndent(deviceFile{Devices: devices, Registration: s.registration}, "", "  ")
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

// SetProfile changes the profile of a device. The empty string means the default.
func (s *Store) SetProfile(id, profile string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.mutation()
	if err != nil {
		return err
	}
	defer unlock()
	for i := range s.devices {
		if s.devices[i].ID == id {
			old := s.devices[i].Profile
			s.devices[i].Profile = profile
			if err := s.persist(); err != nil {
				s.devices[i].Profile = old
				return err
			}
			return nil
		}
	}
	return fmt.Errorf("auth: no device called %q", id)
}

func (s *Store) ConfigureRegistration(maxDevices, perMinute int) error {
	if maxDevices < 1 || maxDevices > 10000 || perMinute < 1 || perMinute > 10000 {
		return errors.New("invalid registration limits")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.mutation()
	if err != nil {
		return err
	}
	defer unlock()
	if s.registration.MaxDevices == maxDevices && s.registration.PerMinute == perMinute {
		return nil
	}
	old := s.registration
	s.registration.MaxDevices = maxDevices
	s.registration.PerMinute = perMinute
	if err := s.persist(); err != nil {
		s.registration = old
		return err
	}
	return nil
}

func (s *Store) SetQuotas(id string, limits *quota.Limits) error {
	if limits != nil && !limits.Valid() {
		return errors.New("invalid device quotas")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.mutation()
	if err != nil {
		return err
	}
	defer unlock()
	for i := range s.devices {
		if s.devices[i].ID == id {
			old := s.devices[i].Quotas
			if limits != nil {
				copy := *limits
				limits = &copy
			}
			s.devices[i].Quotas = limits
			if err := s.persist(); err != nil {
				s.devices[i].Quotas = old
				return err
			}
			return nil
		}
	}
	return fmt.Errorf("auth: no device called %q", id)
}

func (s *Store) UpdateDevice(id string, profile *string, limits **quota.Limits) error {
	if limits != nil && *limits != nil && !(*limits).Valid() {
		return errors.New("invalid device quotas")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.mutation()
	if err != nil {
		return err
	}
	defer unlock()
	for i := range s.devices {
		if s.devices[i].ID == id {
			old := s.devices[i]
			if profile != nil {
				s.devices[i].Profile = *profile
			}
			if limits != nil {
				s.devices[i].Quotas = nil
				if *limits != nil {
					copy := **limits
					s.devices[i].Quotas = &copy
				}
			}
			if err := s.persist(); err != nil {
				s.devices[i] = old
				return err
			}
			return nil
		}
	}
	return fmt.Errorf("auth: no device called %q", id)
}
