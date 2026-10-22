package stats

import (
	"strings"
	"sync"
	"sync/atomic"
)

// nStripes is the number of independent sets of counters: each Record locks
// one of them, picked at random, and Snapshot sums them. A power of two.
const nStripes = 16

// maxLabels is the number of distinct values a map of labels keeps; the others
// fold into "other".
const maxLabels = 64

var kindNames = [...]string{"image", "text", "font", "other"}

func kindIndex(k string) int {
	switch k {
	case "image":
		return 0
	case "text":
		return 1
	case "font":
		return 2
	}
	return 3
}

type cell struct {
	requests    atomic.Uint64
	transformed atomic.Uint64
	bytesIn     atomic.Uint64
	bytesOut    atomic.Uint64
	_           [32]byte
}

func (c *cell) add(tr, in, out uint64) {
	c.requests.Add(1)
	if tr != 0 {
		c.transformed.Add(tr)
	}
	c.bytesIn.Add(in)
	c.bytesOut.Add(out)
}

func (c *cell) load() Bucket {
	return Bucket{
		Requests:    c.requests.Load(),
		Transformed: c.transformed.Load(),
		BytesIn:     c.bytesIn.Load(),
		BytesOut:    c.bytesOut.Load(),
	}
}

func (b Bucket) plus(o Bucket) Bucket {
	return Bucket{
		Requests:    b.Requests + o.Requests,
		Transformed: b.Transformed + o.Transformed,
		BytesIn:     b.BytesIn + o.BytesIn,
		BytesOut:    b.BytesOut + o.BytesOut,
	}
}

// labels gives each distinct label an index into the arrays of the stripes.
// The set is copy-on-write: the key sets are tiny and stable, so the hot path
// is one atomic load and one lookup.
type labels struct {
	set atomic.Pointer[labelSet]
	mu  sync.Mutex
}

type labelSet struct {
	idx   map[string]int
	names []string
}

func newLabels() *labels {
	l := &labels{}
	l.set.Store(&labelSet{idx: map[string]int{}})
	return l
}

func (l *labels) index(key string) int {
	if i, ok := l.set.Load().idx[key]; ok {
		return i
	}
	return l.add(key)
}

func (l *labels) add(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	cur := l.set.Load()
	if i, ok := cur.idx[key]; ok {
		return i
	}
	if len(cur.names) >= maxLabels {
		key = "other"
		if i, ok := cur.idx[key]; ok {
			return i
		}
	}
	next := &labelSet{idx: make(map[string]int, len(cur.idx)+1), names: append(cur.names[:len(cur.names):len(cur.names)], strings.Clone(key))}
	for k, v := range cur.idx {
		next.idx[k] = v
	}
	next.idx[next.names[len(next.names)-1]] = len(next.names) - 1
	l.set.Store(next)
	return len(next.names) - 1
}
