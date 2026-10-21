// Package xcache is a cache of computed results bounded by the bytes it holds.
package xcache

import (
	"container/list"
	"sync"
)

// Entry is a cached result.
type Entry struct {
	Data        []byte
	ContentType string
	// Keep marks a result that says "the original is better": it holds no
	// data, and saves recomputing that conclusion.
	Keep bool
}

func (e *Entry) size() int64 { return int64(len(e.Data)) + 128 }

type item struct {
	key string
	val *Entry
}

// Cache is a least recently used cache with a limit on the bytes it holds.
// It is safe for concurrent use.
type Cache struct {
	mu    sync.Mutex
	max   int64
	bytes int64
	ll    *list.List
	items map[string]*list.Element
}

// New returns a cache that holds at most maxBytes. A cache of zero or negative
// size stores nothing.
func New(maxBytes int64) *Cache {
	return &Cache{max: maxBytes, ll: list.New(), items: map[string]*list.Element{}}
}

// Get returns the entry for key and marks it as recently used.
func (c *Cache) Get(key string) (*Entry, bool) {
	if c == nil || c.max <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*item).val, true
}

// Add stores e under key, replacing any previous entry, and evicts the least
// recently used entries until the cache fits. Entries larger than a quarter of
// the cache are not stored. The entry must not be modified afterwards.
func (c *Cache) Add(key string, e *Entry) {
	if c == nil || c.max <= 0 || e.size() > c.max/4 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.bytes -= el.Value.(*item).val.size()
		el.Value.(*item).val = e
		c.ll.MoveToFront(el)
	} else {
		c.items[key] = c.ll.PushFront(&item{key, e})
	}
	c.bytes += e.size()
	for c.bytes > c.max {
		last := c.ll.Back()
		it := last.Value.(*item)
		c.ll.Remove(last)
		delete(c.items, it.key)
		c.bytes -= it.val.size()
	}
}

// Len returns the number of entries.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// Bytes returns the bytes held, counting a small overhead per entry.
func (c *Cache) Bytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}
