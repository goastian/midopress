package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startWatch(t *testing.T, path string, calls *atomic.Int32) {
	t.Helper()
	old := settleTime
	settleTime = 50 * time.Millisecond
	t.Cleanup(func() { settleTime = old })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watchConfigFile(ctx, path, func() error { calls.Add(1); return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	time.Sleep(100 * time.Millisecond)
}

func TestConfigFileChangesReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "midopress.yaml")
	require.NoError(t, os.WriteFile(path, []byte("log: {level: info}\n"), 0o600))
	var calls atomic.Int32
	startWatch(t, path, &calls)

	require.NoError(t, os.WriteFile(path, []byte("log: {level: debug}\n"), 0o600))
	require.Eventually(t, func() bool { return calls.Load() >= 1 }, 3*time.Second, 20*time.Millisecond, "a write reloads")

	before := calls.Load()
	tmp := filepath.Join(dir, "editor.tmp")
	require.NoError(t, os.WriteFile(tmp, []byte("log: {level: warn}\n"), 0o600))
	require.NoError(t, os.Rename(tmp, path))
	require.Eventually(t, func() bool { return calls.Load() > before }, 3*time.Second, 20*time.Millisecond, "an editor that replaces the file reloads")
}

func TestOtherFilesAndBurstsDoNotReloadTwice(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "midopress.yaml")
	require.NoError(t, os.WriteFile(path, []byte("a: 1\n"), 0o600))
	var calls atomic.Int32
	startWatch(t, path, &calls)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "other.yaml"), []byte("x"), 0o600))
	time.Sleep(300 * time.Millisecond)
	assert.Zero(t, calls.Load(), "a neighbour is not the configuration")

	for i := range 5 {
		require.NoError(t, os.WriteFile(path, []byte("a: "+string(rune('2'+i))+"\n"), 0o600))
		time.Sleep(5 * time.Millisecond)
	}
	require.Eventually(t, func() bool { return calls.Load() >= 1 }, 3*time.Second, 20*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, int32(1), calls.Load(), "a burst of writes is one reload")
}
