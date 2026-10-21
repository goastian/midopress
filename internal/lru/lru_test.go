package lru

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetAdd(t *testing.T) {
	c := New[string, int](100, 0)
	_, ok := c.Get("a")
	assert.False(t, ok)

	c.Add("a", 1)
	v, ok := c.Get("a")
	require.True(t, ok)
	assert.Equal(t, 1, v)

	c.Add("a", 2)
	v, _ = c.Get("a")
	assert.Equal(t, 2, v)
	assert.Equal(t, 1, c.Len())
}

func TestEvictsLeastRecentlyUsed(t *testing.T) {
	// a single key space that fits one shard: max 16 means one entry per shard
	c := New[int, int](shardCount, 0)
	for i := range 10000 {
		c.Add(i, i)
	}
	assert.LessOrEqual(t, c.Len(), shardCount)

	_, ok := c.Get(9999)
	assert.True(t, ok, "most recent entry must survive")
	_, ok = c.Get(0)
	assert.False(t, ok, "oldest entry must be gone")
}

func TestRecentUseProtectsFromEviction(t *testing.T) {
	c := newWithClock[int, int](shardCount*2, 0, time.Now)
	s := c.shardFor(0)
	var keys []int
	for k := 0; len(keys) < 3; k++ {
		if c.shardFor(k) == s {
			keys = append(keys, k)
		}
	}
	c.Add(keys[0], 0)
	c.Add(keys[1], 1)
	_, _ = c.Get(keys[0]) // keys[1] is now the least recently used
	c.Add(keys[2], 2)     // shard holds 2 entries, so one must go

	_, ok := c.Get(keys[0])
	assert.True(t, ok)
	_, ok = c.Get(keys[1])
	assert.False(t, ok)
}

func TestTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	c := newWithClock[string, string](100, time.Minute, func() time.Time { return now })

	c.Add("k", "v")
	c.AddWithTTL("short", "v", time.Second)
	c.AddWithTTL("forever", "v", 0)

	now = now.Add(30 * time.Second)
	_, ok := c.Get("k")
	assert.True(t, ok)
	_, ok = c.Get("short")
	assert.False(t, ok)

	now = now.Add(time.Hour)
	_, ok = c.Get("k")
	assert.False(t, ok)
	_, ok = c.Get("forever")
	assert.True(t, ok)
}

func TestRemove(t *testing.T) {
	c := New[string, int](100, 0)
	c.Add("a", 1)
	c.Remove("a")
	c.Remove("missing")
	_, ok := c.Get("a")
	assert.False(t, ok)
}

func TestConcurrent(t *testing.T) {
	c := New[string, int](256, time.Minute)
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 5000 {
				k := strconv.Itoa((g*7 + i) % 500)
				c.Add(k, i)
				c.Get(k)
				if i%11 == 0 {
					c.Remove(k)
				}
			}
		}()
	}
	wg.Wait()
	assert.LessOrEqual(t, c.Len(), 256+shardCount)
}

func BenchmarkGetParallel(b *testing.B) {
	c := New[string, int](10000, time.Minute)
	for i := range 1000 {
		c.Add(strconv.Itoa(i), i)
	}
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			c.Get(strconv.Itoa(i % 1000))
			i++
		}
	})
}
