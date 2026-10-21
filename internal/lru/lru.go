// Package lru implements a sharded, thread-safe LRU cache with per-entry TTL.
package lru

import (
	"hash/maphash"
	"sync"
	"time"
)

const shardCount = 16

type entry[K comparable, V any] struct {
	key        K
	value      V
	expires    time.Time
	prev, next *entry[K, V]
}

type shard[K comparable, V any] struct {
	mu    sync.Mutex
	items map[K]*entry[K, V]
	head  *entry[K, V] // most recently used
	tail  *entry[K, V] // least recently used
	max   int
}

// Cache is a sharded LRU cache. The zero value is not usable, use New.
type Cache[K comparable, V any] struct {
	shards [shardCount]shard[K, V]
	seed   maphash.Seed
	ttl    time.Duration
	now    func() time.Time
}

// New returns a cache holding at most max entries (spread across shards).
// ttl is the default lifetime of an entry, zero means entries never expire.
func New[K comparable, V any](max int, ttl time.Duration) *Cache[K, V] {
	return newWithClock[K, V](max, ttl, time.Now)
}

func newWithClock[K comparable, V any](max int, ttl time.Duration, now func() time.Time) *Cache[K, V] {
	if max < shardCount {
		max = shardCount
	}
	c := &Cache[K, V]{seed: maphash.MakeSeed(), ttl: ttl, now: now}
	per := (max + shardCount - 1) / shardCount
	for i := range c.shards {
		c.shards[i].items = make(map[K]*entry[K, V], per)
		c.shards[i].max = per
	}
	return c
}

func (c *Cache[K, V]) shardFor(k K) *shard[K, V] {
	return &c.shards[maphash.Comparable(c.seed, k)%shardCount]
}

// Get returns the value for k if it is present and has not expired.
func (c *Cache[K, V]) Get(k K) (V, bool) {
	s := c.shardFor(k)
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.items[k]
	if !ok {
		var zero V
		return zero, false
	}
	if !e.expires.IsZero() && !c.now().Before(e.expires) {
		s.remove(e)
		var zero V
		return zero, false
	}
	s.moveToFront(e)
	return e.value, true
}

// Add stores v under k using the default TTL.
func (c *Cache[K, V]) Add(k K, v V) {
	c.AddWithTTL(k, v, c.ttl)
}

// AddWithTTL stores v under k, expiring after ttl (zero means never).
func (c *Cache[K, V]) AddWithTTL(k K, v V, ttl time.Duration) {
	var expires time.Time
	if ttl > 0 {
		expires = c.now().Add(ttl)
	}
	s := c.shardFor(k)
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.items[k]; ok {
		e.value = v
		e.expires = expires
		s.moveToFront(e)
		return
	}
	e := &entry[K, V]{key: k, value: v, expires: expires}
	s.items[k] = e
	s.pushFront(e)
	for len(s.items) > s.max {
		s.remove(s.tail)
	}
}

// Remove deletes k from the cache.
func (c *Cache[K, V]) Remove(k K) {
	s := c.shardFor(k)
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.items[k]; ok {
		s.remove(e)
	}
}

// Len returns the number of entries, including ones that expired but have not
// been looked up yet.
func (c *Cache[K, V]) Len() int {
	n := 0
	for i := range c.shards {
		s := &c.shards[i]
		s.mu.Lock()
		n += len(s.items)
		s.mu.Unlock()
	}
	return n
}

func (s *shard[K, V]) pushFront(e *entry[K, V]) {
	e.prev = nil
	e.next = s.head
	if s.head != nil {
		s.head.prev = e
	}
	s.head = e
	if s.tail == nil {
		s.tail = e
	}
}

func (s *shard[K, V]) unlink(e *entry[K, V]) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		s.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		s.tail = e.prev
	}
	e.prev, e.next = nil, nil
}

func (s *shard[K, V]) moveToFront(e *entry[K, V]) {
	if s.head == e {
		return
	}
	s.unlink(e)
	s.pushFront(e)
}

func (s *shard[K, V]) remove(e *entry[K, V]) {
	s.unlink(e)
	delete(s.items, e.key)
}
