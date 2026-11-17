package quota

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDurableQuotasSurviveRestartAndSeparateDevices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	limits := Limits{Requests: 2, Bytes: 100, Concurrency: 1}
	l, err := Open(path, time.Hour, limits, nil)
	require.NoError(t, err)
	a, err := l.Begin("a")
	require.NoError(t, err)
	_, err = l.Begin("a")
	require.ErrorIs(t, err, ErrConcurrency)
	b, err := l.Begin("b")
	require.NoError(t, err)
	b.Close()
	r, err := a.Reserve(80)
	require.NoError(t, err)
	assert.Equal(t, 80, r.Size)
	a.Refund(r, 60)
	a.Close()
	assert.Equal(t, uint64(60), l.View("a").Bytes)
	l.Close()
	l, err = Open(path, time.Hour, limits, nil)
	require.NoError(t, err)
	defer l.Close()
	assert.Zero(t, l.View("a").Active)
	assert.Equal(t, uint64(1), l.View("a").Requests)
	a, err = l.Begin("a")
	require.NoError(t, err)
	r, err = a.Reserve(100)
	require.NoError(t, err)
	assert.Equal(t, 40, r.Size)
	_, err = a.Reserve(1)
	assert.ErrorIs(t, err, ErrBytes)
	a.Close()
	_, err = l.Begin("a")
	assert.ErrorIs(t, err, ErrRequests)
	b, err = l.Begin("b")
	require.NoError(t, err)
	b.Close()
	assert.Zero(t, l.View("b").Bytes)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

func TestConcurrentRequestsCannotOverspendQuota(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "usage.json"), time.Hour, Limits{Requests: 50, Bytes: 1000}, nil)
	require.NoError(t, err)
	defer l.Close()
	var wg sync.WaitGroup
	var accepted atomic.Int64
	var sent atomic.Int64
	for range 100 {
		wg.Go(func() {
			lease, err := l.Begin("a")
			if err != nil {
				assert.True(t, errors.Is(err, ErrRequests) || errors.Is(err, ErrBytes))
				return
			}
			accepted.Add(1)
			defer lease.Close()
			r, err := lease.Reserve(100)
			if errors.Is(err, ErrBytes) {
				return
			}
			require.NoError(t, err)
			sent.Add(int64(r.Size))
		})
	}
	wg.Wait()
	assert.LessOrEqual(t, accepted.Load(), int64(50))
	assert.GreaterOrEqual(t, accepted.Load(), int64(10))
	assert.Equal(t, int64(1000), sent.Load())
	assert.Equal(t, uint64(accepted.Load()), l.View("a").Requests)
	assert.Equal(t, uint64(1000), l.View("a").Bytes)
}

func TestReservationsCannotRefundANewerWindow(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "usage.json"), time.Minute, Limits{Bytes: 100}, nil)
	require.NoError(t, err)
	defer l.Close()
	now := time.Unix(l.state.Start, 0)
	l.now = func() time.Time { return now }
	a, err := l.Begin("a")
	require.NoError(t, err)
	defer a.Close()
	old, err := a.Reserve(100)
	require.NoError(t, err)
	now = now.Add(time.Minute)
	b, err := l.Begin("a")
	require.NoError(t, err)
	defer b.Close()
	r, err := b.Reserve(100)
	require.NoError(t, err)
	assert.Equal(t, 100, r.Size)
	a.Refund(old, 0)
	assert.Equal(t, uint64(100), l.View("a").Bytes)
	_, err = b.Reserve(1)
	assert.ErrorIs(t, err, ErrBytes)
}

func TestStorageFailureAndConcurrentOwnersFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	l, err := Open(path, time.Hour, Limits{Bytes: 100}, nil)
	require.NoError(t, err)
	defer l.Close()
	_, err = Open(path, time.Hour, Limits{}, nil)
	assert.ErrorIs(t, err, ErrUnavailable)
	a, err := l.Begin("a")
	require.NoError(t, err)
	defer a.Close()
	l.path = filepath.Join(t.TempDir(), "absent", "usage.json")
	_, err = a.Reserve(100)
	assert.ErrorIs(t, err, ErrUnavailable)
	assert.Zero(t, l.View("a").Bytes)
	_, err = l.Begin("b")
	assert.ErrorIs(t, err, ErrUnavailable)
	assert.Zero(t, l.View("b").Requests)
}

func TestTunnelConcurrencyDoesNotDeadlockInterceptedRequests(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "usage.json"), time.Hour, Limits{Concurrency: 1}, nil)
	require.NoError(t, err)
	defer l.Close()
	tunnel, err := l.BeginTunnel("a")
	require.NoError(t, err)
	defer tunnel.Close()
	_, err = l.BeginTunnel("a")
	assert.ErrorIs(t, err, ErrConcurrency)
	request, err := l.Begin("a")
	require.NoError(t, err)
	_, err = l.Begin("a")
	assert.ErrorIs(t, err, ErrConcurrency)
	request.Close()
	request.Close()
	assert.Equal(t, 1, l.View("a").Tunnels)
	assert.Zero(t, l.View("a").Active)
}

func TestAbruptExitKeepsReservations(t *testing.T) {
	if path := os.Getenv("MIDOPRESS_QUOTA_CRASH_FIXTURE"); path != "" {
		l, err := Open(path, time.Hour, Limits{Requests: 2, Bytes: 100}, nil)
		if err != nil {
			os.Exit(40)
		}
		a, err := l.Begin("a")
		if err != nil {
			os.Exit(41)
		}
		if _, err = a.Reserve(100); err != nil {
			os.Exit(42)
		}
		os.Exit(0)
	}
	path := filepath.Join(t.TempDir(), "usage.json")
	cmd := exec.Command(os.Args[0], "-test.run=^TestAbruptExitKeepsReservations$")
	cmd.Env = append(os.Environ(), "MIDOPRESS_QUOTA_CRASH_FIXTURE="+path)
	require.NoError(t, cmd.Run())
	l, err := Open(path, time.Hour, Limits{Requests: 2, Bytes: 100}, nil)
	require.NoError(t, err)
	defer l.Close()
	assert.Equal(t, uint64(100), l.View("a").Bytes)
	_, err = l.Begin("a")
	assert.ErrorIs(t, err, ErrBytes)
}
