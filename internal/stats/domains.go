package stats

import (
	"hash/maphash"
	"strings"
	"sync"
)

// domainSet tracks the heaviest hosts with the Space-Saving algorithm, on
// shards chosen by the hash of the host. Each shard has cap counters; a host
// that is not tracked takes over the counter with the smallest score and
// inherits that score. The score is the bytes saved, so:
//
//   - memory is bounded by the number of counters whatever hosts are invented;
//   - the score of a tracked host overestimates its true saved bytes by at most
//     the smallest score of its shard, which is at most N/cap where N is the
//     saved bytes that went through the shard;
//   - every host that saved more than N/cap bytes is tracked.
//
// The Bucket numbers reported for a host count only what happened since it was
// last admitted, so they are lower bounds. Ties in the score (all zero when
// nothing is saved) break on the requests in the Bucket.
type domainSet struct {
	seed   maphash.Seed
	shards []domShard
	mask   uint64
}

type domEntry struct {
	host  string
	score uint64
	b     Bucket
	idx   int
}

type domShard struct {
	mu      sync.Mutex
	enabled bool
	m       map[string]*domEntry
	heap    []*domEntry
	cap     int
	_       [24]byte
}

func newDomainSet(total int, enabled bool) *domainSet {
	n := 1
	if total >= 256 {
		n = 16
	}
	d := &domainSet{seed: maphash.MakeSeed(), shards: make([]domShard, n), mask: uint64(n - 1)}
	for i := range d.shards {
		d.shards[i].cap = max(total/n, 1)
		d.shards[i].enabled = enabled
		d.shards[i].m = map[string]*domEntry{}
	}
	return d
}

func (d *domainSet) setEnabled(on bool) {
	for i := range d.shards {
		s := &d.shards[i]
		s.mu.Lock()
		s.enabled = on
		s.m = map[string]*domEntry{}
		s.heap = nil
		s.mu.Unlock()
	}
}

func (d *domainSet) count() int {
	n := 0
	for i := range d.shards {
		s := &d.shards[i]
		s.mu.Lock()
		n += len(s.m)
		s.mu.Unlock()
	}
	return n
}

func (d *domainSet) add(host string, tr, in, out uint64) {
	s := &d.shards[maphash.String(d.seed, host)&d.mask]
	var w uint64
	if in > out {
		w = in - out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return
	}
	if e, ok := s.m[host]; ok {
		e.score += w
		e.b = e.b.plus(Bucket{1, tr, in, out})
		s.down(e.idx)
		return
	}
	host = strings.Clone(host)
	nb := Bucket{1, tr, in, out}
	if len(s.heap) < s.cap {
		e := &domEntry{host: host, score: w, b: nb, idx: len(s.heap)}
		s.heap = append(s.heap, e)
		s.m[host] = e
		s.up(e.idx)
		return
	}
	e := s.heap[0]
	delete(s.m, e.host)
	e.host = host
	e.score += w
	e.b = nb
	s.m[host] = e
	s.down(0)
}

func domLess(a, b *domEntry) bool {
	if a.score != b.score {
		return a.score < b.score
	}
	return a.b.Requests < b.b.Requests
}

func (s *domShard) swap(i, j int) {
	s.heap[i], s.heap[j] = s.heap[j], s.heap[i]
	s.heap[i].idx = i
	s.heap[j].idx = j
}

func (s *domShard) up(i int) {
	for i > 0 {
		p := (i - 1) / 2
		if !domLess(s.heap[i], s.heap[p]) {
			return
		}
		s.swap(i, p)
		i = p
	}
}

func (s *domShard) down(i int) {
	n := len(s.heap)
	for {
		l := 2*i + 1
		if l >= n {
			return
		}
		m := l
		if r := l + 1; r < n && domLess(s.heap[r], s.heap[l]) {
			m = r
		}
		if !domLess(s.heap[m], s.heap[i]) {
			return
		}
		s.swap(i, m)
		i = m
	}
}

func betterDomain(a, b DomainStat) bool {
	sa, sb := int64(a.BytesIn)-int64(a.BytesOut), int64(b.BytesIn)-int64(b.BytesOut)
	if sa != sb {
		return sa > sb
	}
	if a.Requests != b.Requests {
		return a.Requests > b.Requests
	}
	return a.Domain < b.Domain
}

// top returns the n best hosts by bytes saved, then requests.
func (d *domainSet) top(n int) []DomainStat {
	out := make([]DomainStat, 0, max(n, 0))
	if n <= 0 {
		return out
	}
	for i := range d.shards {
		s := &d.shards[i]
		s.mu.Lock()
		for _, e := range s.heap {
			c := DomainStat{Domain: e.host, Bucket: e.b}
			if len(out) == n && !betterDomain(c, out[n-1]) {
				continue
			}
			if len(out) < n {
				out = append(out, c)
			} else {
				out[n-1] = c
			}
			for j := len(out) - 1; j > 0 && betterDomain(out[j], out[j-1]); j-- {
				out[j], out[j-1] = out[j-1], out[j]
			}
		}
		s.mu.Unlock()
	}
	return out
}
