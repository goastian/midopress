package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/goastian/midopress/internal/quota"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistrationCapIsAtomicAcrossStoreHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	first, err := OpenStore(path, true)
	require.NoError(t, err)
	require.NoError(t, first.ConfigureRegistration(8, 100))
	var wg sync.WaitGroup
	var added atomic.Int64
	for i := range 40 {
		wg.Go(func() {
			store, err := OpenStore(path, false)
			require.NoError(t, err)
			_, err = store.Add(fmt.Sprintf("device-%d", i), "")
			if err == nil {
				added.Add(1)
			} else {
				assert.ErrorIs(t, err, ErrRegistrationLimit)
			}
		})
	}
	wg.Wait()
	assert.Equal(t, int64(8), added.Load())
	last, err := OpenStore(path, false)
	require.NoError(t, err)
	assert.Len(t, last.List(), 8)
}

func TestRegistrationRateAndDeviceOverridesPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	s, err := OpenStore(path, true)
	require.NoError(t, err)
	require.NoError(t, s.ConfigureRegistration(10, 1))
	_, err = s.AddWithQuotas("a", "saver", &quota.Limits{Requests: 10, Bytes: 100, Concurrency: 2})
	require.NoError(t, err)
	require.NoError(t, s.Revoke("a"))
	s, err = OpenStore(path, false)
	require.NoError(t, err)
	_, err = s.Add("b", "")
	assert.ErrorIs(t, err, ErrRegistrationLimit)
	require.NoError(t, s.ConfigureRegistration(10, 5))
	token, err := s.AddWithQuotas("a", "saver", &quota.Limits{Requests: 10, Bytes: 100, Concurrency: 2})
	require.NoError(t, err)
	a, err := New(Options{Mode: ModeToken, DevicesFile: path, ReloadInterval: -1})
	require.NoError(t, err)
	defer a.Close()
	assert.True(t, a.Check("127.0.0.1:1", "Bearer "+token).Allowed)
	limits := a.DeviceQuotas("a")
	require.NotNil(t, limits)
	assert.Equal(t, uint64(10), limits.Requests)
	limits.Requests = 999
	assert.Equal(t, uint64(10), a.DeviceQuotas("a").Requests)
	var inherit *quota.Limits
	require.NoError(t, s.UpdateDevice("a", nil, &inherit))
	require.NoError(t, a.Reload())
	assert.Nil(t, a.DeviceQuotas("a"))
	assert.Equal(t, "saver", a.DeviceProfile("a"))
}

func TestAdministratorSecretCannotBecomeADeviceCredential(t *testing.T) {
	a, s, _ := newTokenSetup(t, nil)
	deviceToken, err := s.Add("normal", "")
	require.NoError(t, err)
	_, err = s.Add("alias", "")
	require.NoError(t, err)
	admin := "administrator-secret-only-for-this-fixture"
	hash := sha256.Sum256([]byte(admin))
	s.mu.Lock()
	for i := range s.devices {
		if s.devices[i].ID == "alias" {
			s.devices[i].TokenSHA256 = hex.EncodeToString(hash[:])
		}
	}
	require.NoError(t, s.persist())
	s.mu.Unlock()
	a.ReserveAdminToken(admin)
	require.NoError(t, a.Reload())
	assert.False(t, a.Check("127.0.0.1:1", "Bearer "+admin).Allowed)
	assert.False(t, a.Check("127.0.0.1:1", basicHeader("alias", admin)).Allowed)
	assert.False(t, a.DeviceAllowed("alias"))
	assert.True(t, a.Check("127.0.0.1:1", "Bearer "+deviceToken).Allowed)
	require.NoError(t, s.Revoke("normal"))
	require.NoError(t, a.Reload())
	assert.False(t, a.DeviceAllowed("normal"))
}
