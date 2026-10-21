package xcache

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func entry(n int) *Entry { return &Entry{Data: make([]byte, n)} }

func TestGetAdd(t *testing.T) {
	c := New(10_000)
	_, ok := c.Get("a")
	assert.False(t, ok)
	c.Add("a", entry(100))
	e, ok := c.Get("a")
	assert.True(t, ok)
	assert.Len(t, e.Data, 100)
	c.Add("a", entry(200))
	e, _ = c.Get("a")
	assert.Len(t, e.Data, 200)
	assert.Equal(t, 1, c.Len())
	assert.Equal(t, int64(200+128), c.Bytes())
}

func TestEvictsLeastRecentlyUsedByBytes(t *testing.T) {
	c := New(3_000)
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		c.Add(k, entry(400))
	}
	c.Get("a")
	c.Add("f", entry(400))
	_, okA := c.Get("a")
	_, okB := c.Get("b")
	assert.True(t, okA, "recently used stays")
	assert.False(t, okB, "the oldest goes")
	assert.LessOrEqual(t, c.Bytes(), int64(3_000))
}

func TestOversizedAndDisabled(t *testing.T) {
	c := New(1_000)
	c.Add("big", entry(500))
	_, ok := c.Get("big")
	assert.False(t, ok, "larger than a quarter is not stored")

	off := New(0)
	off.Add("a", entry(1))
	_, ok = off.Get("a")
	assert.False(t, ok)
	var nilCache *Cache
	nilCache.Add("a", entry(1))
	_, ok = nilCache.Get("a")
	assert.False(t, ok)
}

func TestKeepEntry(t *testing.T) {
	c := New(10_000)
	c.Add("k", &Entry{Keep: true})
	e, ok := c.Get("k")
	assert.True(t, ok)
	assert.True(t, e.Keep)
}

func TestConcurrent(t *testing.T) {
	c := New(50_000)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 500 {
				k := fmt.Sprint((g*31 + i) % 40)
				c.Add(k, entry(100+i%50))
				c.Get(k)
			}
		}()
	}
	wg.Wait()
	assert.LessOrEqual(t, c.Bytes(), int64(50_000))
}
