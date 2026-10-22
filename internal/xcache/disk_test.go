package xcache

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newClock() *clock { return &clock{t: time.Unix(1_800_000_000, 0)} }

func open(t *testing.T, dir string, o Options) *Cache {
	t.Helper()
	o.Dir = dir
	if o.SweepEvery == 0 {
		o.SweepEvery = time.Hour
	}
	c, err := Open(o)
	require.NoError(t, err)
	t.Cleanup(c.Close)
	return c
}

func TestDiskSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	c := open(t, dir, Options{MemoryBytes: 1 << 20, DiskBytes: 1 << 20})
	c.Add("a", &Entry{Data: []byte("hello"), ContentType: "image/webp"})
	c.Add("keep", &Entry{Keep: true})
	c.Flush()
	c.Close()

	again := open(t, dir, Options{MemoryBytes: 1 << 20, DiskBytes: 1 << 20})
	e, ok := again.Get("a")
	require.True(t, ok)
	assert.Equal(t, []byte("hello"), e.Data)
	assert.Equal(t, "image/webp", e.ContentType)
	k, ok := again.Get("keep")
	require.True(t, ok)
	assert.True(t, k.Keep)
	st := again.Stats()
	assert.Equal(t, uint64(2), st.DiskHits)
	assert.Equal(t, 2, st.DiskItems)

	_, ok = again.Get("a")
	assert.True(t, ok)
	assert.Equal(t, uint64(1), again.Stats().MemoryHits, "promoted to memory after the first read")
}

func TestDiskOnlyWhenMemoryIsOff(t *testing.T) {
	c := open(t, t.TempDir(), Options{DiskBytes: 1 << 20})
	c.Add("a", &Entry{Data: []byte("x")})
	c.Flush()
	e, ok := c.Get("a")
	require.True(t, ok)
	assert.Equal(t, []byte("x"), e.Data)
	assert.Equal(t, 0, c.Len())
}

func TestEntriesExpire(t *testing.T) {
	clk := newClock()
	dir := t.TempDir()
	c := open(t, dir, Options{MemoryBytes: 1 << 20, DiskBytes: 1 << 20, Now: clk.now})
	c.Add("short", &Entry{Data: []byte("1"), Expires: clk.now().Add(time.Minute)})
	c.Add("long", &Entry{Data: []byte("2"), Expires: clk.now().Add(time.Hour)})
	c.Add("never", &Entry{Data: []byte("3")})
	c.Flush()

	clk.advance(2 * time.Minute)
	_, ok := c.Get("short")
	assert.False(t, ok, "memory tier honours the time")
	_, ok = c.Get("long")
	assert.True(t, ok)

	// the disk tier on its own, after a restart
	c.Close()
	clk.advance(2 * time.Hour)
	again := open(t, dir, Options{MemoryBytes: 1 << 20, DiskBytes: 1 << 20, Now: clk.now})
	_, ok = again.Get("long")
	assert.False(t, ok)
	_, ok = again.Get("never")
	assert.True(t, ok)
	assert.Positive(t, again.Stats().Expired)
	assert.Equal(t, 1, again.Stats().DiskItems, "expired files are gone from the index")
}

func TestSweepRemovesExpiredFiles(t *testing.T) {
	clk := newClock()
	dir := t.TempDir()
	c := open(t, dir, Options{DiskBytes: 1 << 20, Now: clk.now})
	for i := range 5 {
		c.Add(fmt.Sprint(i), &Entry{Data: []byte("x"), Expires: clk.now().Add(time.Minute)})
	}
	c.Flush()
	assert.Equal(t, 5, countFiles(t, dir))
	clk.advance(time.Hour)
	c.Sweep()
	assert.Equal(t, 0, countFiles(t, dir))
	assert.Equal(t, uint64(5), c.Stats().Expired)
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	require.NoError(t, filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			n++
		}
		return err
	}))
	return n
}

func TestDiskSizeLimitEvictsTheLeastRecentlyUsed(t *testing.T) {
	c := open(t, t.TempDir(), Options{DiskBytes: 10_000})
	payload := make([]byte, 800)
	for i := range 8 {
		c.Add(fmt.Sprint(i), &Entry{Data: payload})
		c.Flush()
		if i == 3 {
			_, ok := c.Get("0")
			require.True(t, ok)
		}
	}
	for i := 8; i < 14; i++ {
		c.Add(fmt.Sprint(i), &Entry{Data: payload})
		c.Flush()
	}
	st := c.Stats()
	assert.LessOrEqual(t, st.DiskBytes, int64(10_000))
	assert.Positive(t, st.Evictions)
	_, newest := c.Get("13")
	assert.True(t, newest)
	_, oldest := c.Get("1")
	assert.False(t, oldest, "an old entry that was not used went first")
}

func TestCorruptFilesAreMisses(t *testing.T) {
	dir := t.TempDir()
	c := open(t, dir, Options{DiskBytes: 1 << 20})
	c.Add("a", &Entry{Data: []byte("payload payload")})
	c.Flush()
	c.Close()

	path := filepath.Join(dir, nameOf("a")[:2], nameOf("a"))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	raw[len(raw)/2] ^= 0xFF
	require.NoError(t, os.WriteFile(path, raw, 0o600))

	again := open(t, dir, Options{DiskBytes: 1 << 20})
	_, ok := again.Get("a")
	assert.False(t, ok)
	assert.Positive(t, again.Stats().DiskErrors)
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "the bad file is removed")
}

func TestLeftoversAreCleanedOnOpen(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "ab")
	require.NoError(t, os.MkdirAll(sub, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "w123.tmp"), []byte("half written"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "not-a-cache-file"), []byte("x"), 0o600))
	c := open(t, dir, Options{DiskBytes: 1 << 20})
	assert.Equal(t, 0, c.Stats().DiskItems)
	assert.Equal(t, 0, countFiles(t, dir))
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	exp := time.Unix(1_900_000_000, 0)
	for _, e := range []*Entry{
		{Data: []byte("abc"), ContentType: "image/avif", Expires: exp},
		{Keep: true},
		{Data: nil, ContentType: ""},
		{Data: make([]byte, 100_000), ContentType: "font/woff2"},
	} {
		got, err := decode(encode(e))
		require.NoError(t, err)
		assert.Equal(t, e.ContentType, got.ContentType)
		assert.Equal(t, e.Keep, got.Keep)
		assert.True(t, e.Expires.Equal(got.Expires))
		if !e.Keep {
			assert.Equal(t, len(e.Data), len(got.Data))
		}
	}
	for n := range len(encode(&Entry{Data: []byte("abc"), ContentType: "x"})) {
		_, err := decode(encode(&Entry{Data: []byte("abc"), ContentType: "x"})[:n])
		assert.Error(t, err, "truncated at %d", n)
	}
}

func TestHitRateAndStats(t *testing.T) {
	c := open(t, t.TempDir(), Options{MemoryBytes: 1 << 20, DiskBytes: 1 << 20})
	assert.Equal(t, 0.0, c.Stats().HitRate())
	c.Add("a", &Entry{Data: []byte("x")})
	c.Get("a")
	c.Get("a")
	c.Get("b")
	st := c.Stats()
	assert.Equal(t, uint64(2), st.Hits())
	assert.Equal(t, uint64(1), st.Misses)
	assert.InDelta(t, 2.0/3.0, st.HitRate(), 1e-9)
}

func TestFullQueueNeverBlocks(t *testing.T) {
	c := open(t, t.TempDir(), Options{MemoryBytes: 1 << 24, DiskBytes: 1 << 24})
	start := time.Now()
	for i := range 3000 {
		c.Add(fmt.Sprint(i), &Entry{Data: make([]byte, 512)})
	}
	assert.Less(t, time.Since(start), 2*time.Second)
	c.Flush()
}

func TestDiskConcurrent(t *testing.T) {
	c := open(t, t.TempDir(), Options{MemoryBytes: 20_000, DiskBytes: 100_000})
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 300 {
				k := fmt.Sprint((g*7 + i) % 60)
				c.Add(k, &Entry{Data: make([]byte, 200+i%100)})
				c.Get(k)
			}
		}()
	}
	wg.Wait()
	c.Flush()
	assert.LessOrEqual(t, c.Stats().DiskBytes, int64(100_000))
}
