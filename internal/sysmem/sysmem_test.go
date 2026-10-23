package sysmem

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseLimit(t *testing.T) {
	for in, want := range map[string]int64{
		"1073741824\n": 1 << 30, "max\n": 0, "": 0, "0": 0, "-1": 0, "junk": 0,
		"9223372036854771712": 0, // cgroups v1: no limit
	} {
		assert.Equal(t, want, parseLimit(in), in)
	}
}

func TestParseKB(t *testing.T) {
	assert.Equal(t, int64(16384<<10), parseKB("   16384 kB"))
	assert.Zero(t, parseKB(""))
	assert.Zero(t, parseKB("x kB"))
}

func TestMeminfo(t *testing.T) {
	p := filepath.Join(t.TempDir(), "meminfo")
	assert.NoError(t, os.WriteFile(p, []byte("MemFree:  100 kB\nMemTotal:   2048 kB\nBuffers: 1 kB\n"), 0o600))
	assert.Equal(t, int64(2048<<10), meminfo(p))
	assert.Zero(t, meminfo(filepath.Join(t.TempDir(), "missing")))
}

func TestTotalOfThisMachine(t *testing.T) {
	if _, err := os.Stat("/proc/meminfo"); err != nil {
		t.Skip("no /proc/meminfo")
	}
	assert.Greater(t, Total(), int64(64<<20))
}

func TestBudget(t *testing.T) {
	const mib = 1 << 20
	assert.Equal(t, int64(256*mib), Budget(0, 256*mib, 8<<30))
	assert.Equal(t, int64(256*mib), Budget(512*mib, 256*mib, 8<<30), "a quarter would be 128 MiB")
	assert.Equal(t, int64(1<<30), Budget(4<<30, 256*mib, 8<<30))
	assert.Equal(t, int64(8<<30), Budget(256<<30, 256*mib, 8<<30))
}
