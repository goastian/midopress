// Package xcache is a cache of computed results addressed by content. It keeps
// them in memory, bounded in bytes, and optionally on disk, where they survive
// restarts. Entries can expire.
package xcache

import (
	"container/list"
	"sync"
	"sync/atomic"
	"time"
)

// Entry is a cached result.
type Entry struct {
	Data        []byte
	ContentType string
	// Keep marks a result that says "the original is better": it holds no
	// data, and saves recomputing that conclusion.
	Keep bool
	// Expires is when the entry stops being served, zero for never.
	Expires time.Time
	// Metric and Measure say how close an image is to the original. They live
	// in memory only: an entry read from disk has none.
	Metric  string
	Measure float64
}

func (e *Entry) size() int64 { return int64(len(e.Data)) + 128 }

func (e *Entry) expired(now time.Time) bool {
	return !e.Expires.IsZero() && !now.Before(e.Expires)
}

type item struct {
	key string
	val *Entry
}

// Stats counts what the cache did since it was opened.
type Stats struct {
	MemoryHits  uint64
	DiskHits    uint64
	Misses      uint64
	Expired     uint64 // entries dropped because their time was up
	Evictions   uint64 // entries dropped to make room, in memory or on disk
	DiskWrites  uint64
	DiskErrors  uint64 // failed writes, corrupt or unreadable files, writes dropped when the queue was full
	MemoryBytes int64
	MemoryItems int
	DiskBytes   int64
	DiskItems   int
}

// Hits is the total number of lookups answered from the cache.
func (s Stats) Hits() uint64 { return s.MemoryHits + s.DiskHits }

// HitRate is the share of lookups answered from the cache, 0 when there were
// none.
func (s Stats) HitRate() float64 {
	total := s.Hits() + s.Misses
	if total == 0 {
		return 0
	}
	return float64(s.Hits()) / float64(total)
}

// Options configures a cache.
type Options struct {
	// MemoryBytes bounds the memory tier, zero or negative disables it.
	MemoryBytes int64
	// Dir is the directory of the disk tier, empty for none. It is created.
	Dir string
	// DiskBytes bounds the disk tier.
	DiskBytes int64
	// SweepEvery is how often expired entries are removed from disk, by
	// default every minute.
	SweepEvery time.Duration
	// Now is the clock, for tests.
	Now func() time.Time
}

// Cache is a two tier cache. It is safe for concurrent use. The zero value and
// a nil *Cache store nothing.
type Cache struct {
	mu    sync.Mutex
	max   int64
	bytes int64
	ll    *list.List
	items map[string]*list.Element
	now   func() time.Time

	disk *diskTier

	memHits, diskHits, misses, expired, evictions atomic.Uint64
}

// New returns a cache that only uses memory and holds at most maxBytes.
func New(maxBytes int64) *Cache {
	c, _ := Open(Options{MemoryBytes: maxBytes})
	return c
}

// Open returns a cache with the tiers the options ask for. When the disk tier
// is on it reads the files that are already there.
func Open(o Options) (*Cache, error) {
	c := &Cache{max: o.MemoryBytes, ll: list.New(), items: map[string]*list.Element{}, now: o.Now}
	if c.now == nil {
		c.now = time.Now
	}
	if o.Dir != "" && o.DiskBytes > 0 {
		d, err := openDisk(o, c.now, c)
		if err != nil {
			return nil, err
		}
		c.disk = d
	}
	return c, nil
}

// Get returns the entry for key and marks it as recently used. An entry whose
// time is up is dropped and reported as missing.
func (c *Cache) Get(key string) (*Entry, bool) {
	if c == nil {
		return nil, false
	}
	if e, ok := c.getMemory(key); ok {
		c.memHits.Add(1)
		return e, true
	}
	if c.disk != nil {
		if e, ok := c.disk.get(key); ok {
			c.diskHits.Add(1)
			c.addMemory(key, e)
			return e, true
		}
	}
	c.misses.Add(1)
	return nil, false
}

func (c *Cache) getMemory(key string) (*Entry, bool) {
	if c.max <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	it := el.Value.(*item)
	if it.val.expired(c.now()) {
		c.removeLocked(el)
		c.expired.Add(1)
		return nil, false
	}
	c.ll.MoveToFront(el)
	return it.val, true
}

// Add stores e under key in every tier. Entries larger than a quarter of a
// tier are not stored in it. The entry must not be modified afterwards.
func (c *Cache) Add(key string, e *Entry) {
	if c == nil {
		return
	}
	c.addMemory(key, e)
	if c.disk != nil {
		c.disk.put(key, e)
	}
}

func (c *Cache) addMemory(key string, e *Entry) {
	if c.max <= 0 || e.size() > c.max/4 {
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
		c.removeLocked(c.ll.Back())
		c.evictions.Add(1)
	}
}

func (c *Cache) removeLocked(el *list.Element) {
	it := el.Value.(*item)
	c.ll.Remove(el)
	delete(c.items, it.key)
	c.bytes -= it.val.size()
}

// Clear drops everything, in memory and on disk.
func (c *Cache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.ll.Init()
	c.items = map[string]*list.Element{}
	c.bytes = 0
	c.mu.Unlock()
	if c.disk != nil {
		c.disk.clear()
	}
}

// Len returns the number of entries in memory.
func (c *Cache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// Bytes returns the bytes held in memory, counting a small overhead per entry.
func (c *Cache) Bytes() int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}

// Stats returns the counters.
func (c *Cache) Stats() Stats {
	if c == nil {
		return Stats{}
	}
	s := Stats{
		MemoryHits: c.memHits.Load(), DiskHits: c.diskHits.Load(), Misses: c.misses.Load(),
		Expired: c.expired.Load(), Evictions: c.evictions.Load(),
	}
	c.mu.Lock()
	s.MemoryBytes, s.MemoryItems = c.bytes, c.ll.Len()
	c.mu.Unlock()
	if c.disk != nil {
		c.disk.fill(&s)
	}
	return s
}

// Sweep drops the entries whose time is up from both tiers and makes the disk
// tier fit its limit. It runs by itself in the background; calling it is only
// useful in tests.
func (c *Cache) Sweep() {
	if c == nil {
		return
	}
	c.mu.Lock()
	now := c.now()
	for el := c.ll.Back(); el != nil; {
		prev := el.Prev()
		if el.Value.(*item).val.expired(now) {
			c.removeLocked(el)
			c.expired.Add(1)
		}
		el = prev
	}
	c.mu.Unlock()
	if c.disk != nil {
		c.disk.sweep()
	}
}

// Flush waits until every write queued for the disk is done.
func (c *Cache) Flush() {
	if c != nil && c.disk != nil {
		c.disk.flush()
	}
}

// Close flushes the disk tier and stops the background work. The cache can
// still be read afterwards but no longer writes to disk.
func (c *Cache) Close() {
	if c != nil && c.disk != nil {
		c.disk.close()
	}
}
