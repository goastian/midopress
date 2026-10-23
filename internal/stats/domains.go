package stats

import (
	"cmp"
	"hash/maphash"
	"slices"
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
//
// A set built with missed counts, instead of saved bytes, the bytes of images,
// text and fonts that were left alone, and remembers for each host why. It
// answers "where did the bytes that were not saved come from".
type domainSet struct {
	seed   maphash.Seed
	shards []domShard
	mask   uint64
	missed bool
}

// domEvent is what a set needs to know about one response.
type domEvent struct {
	tr, in, out uint64
	kind        int
	reason      string // why it was left alone, empty when it was transformed
}

type domEntry struct {
	host  string
	score uint64
	b     Bucket
	idx   int

	// only in a set of missed bytes
	missed  uint64
	media   uint64
	reasons []hostReason
}

// hostReason is the responses of a host left alone for one reason and kind.
type hostReason struct {
	reason   string
	kind     int
	requests uint64
	bytes    uint64
}

// maxHostReasons bounds what a host remembers: reasons times kinds that ever
// happen are far fewer.
const maxHostReasons = 64

func (e *domEntry) note(ev domEvent) {
	if ev.kind == kindMedia {
		e.media += ev.in
	}
	if ev.reason == "" {
		return
	}
	if compressible(ev.kind) {
		e.missed += ev.in
	}
	for i := range e.reasons {
		r := &e.reasons[i]
		if r.kind == ev.kind && r.reason == ev.reason {
			r.requests++
			r.bytes += ev.in
			return
		}
	}
	if len(e.reasons) < maxHostReasons {
		e.reasons = append(e.reasons, hostReason{strings.Clone(ev.reason), ev.kind, 1, ev.in})
	}
}

type domShard struct {
	mu      sync.Mutex
	enabled bool
	m       map[string]*domEntry
	heap    []*domEntry
	cap     int
	_       [24]byte
}

func newDomainSet(total int, enabled, missed bool) *domainSet {
	n := 1
	if total >= 256 {
		n = 16
	}
	d := &domainSet{seed: maphash.MakeSeed(), shards: make([]domShard, n), mask: uint64(n - 1), missed: missed}
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

func (d *domainSet) add(host string, ev domEvent) {
	s := &d.shards[maphash.String(d.seed, host)&d.mask]
	var w uint64
	switch {
	case !d.missed && ev.in > ev.out:
		w = ev.in - ev.out
	case d.missed && ev.reason != "" && compressible(ev.kind):
		w = ev.in
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return
	}
	nb := Bucket{1, ev.tr, ev.in, ev.out}
	if e, ok := s.m[host]; ok {
		e.score += w
		e.b = e.b.plus(nb)
		if d.missed {
			e.note(ev)
		}
		s.down(e.idx)
		return
	}
	host = strings.Clone(host)
	if len(s.heap) < s.cap {
		e := &domEntry{host: host, score: w, b: nb, idx: len(s.heap)}
		if d.missed {
			e.note(ev)
		}
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
	e.missed, e.media, e.reasons = 0, 0, nil
	if d.missed {
		e.note(ev)
	}
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

func betterMissed(a, b MissedStat) bool {
	if a.Missed != b.Missed {
		return a.Missed > b.Missed
	}
	if a.Requests != b.Requests {
		return a.Requests > b.Requests
	}
	return a.Domain < b.Domain
}

func (e *domEntry) view() MissedStat {
	m := MissedStat{Domain: e.host, Missed: e.missed, Media: e.media, Bucket: e.b, Reasons: make([]HostReason, 0, len(e.reasons))}
	for _, r := range e.reasons {
		m.Reasons = append(m.Reasons, HostReason{Reason: r.reason, Kind: kindNames[r.kind], Requests: r.requests, BytesIn: r.bytes})
	}
	slices.SortFunc(m.Reasons, func(a, b HostReason) int {
		if a.BytesIn != b.BytesIn {
			return cmp.Compare(b.BytesIn, a.BytesIn)
		}
		return cmp.Or(strings.Compare(a.Reason, b.Reason), strings.Compare(a.Kind, b.Kind))
	})
	return m
}

// topMissed returns the n hosts with the most bytes left alone.
func (d *domainSet) topMissed(n int) []MissedStat {
	out := make([]MissedStat, 0, max(n, 0))
	if n <= 0 {
		return out
	}
	for i := range d.shards {
		s := &d.shards[i]
		s.mu.Lock()
		for _, e := range s.heap {
			if e.missed == 0 && e.media == 0 {
				continue
			}
			c := MissedStat{Domain: e.host, Missed: e.missed, Media: e.media, Bucket: e.b}
			if len(out) == n && !betterMissed(c, out[n-1]) {
				continue
			}
			v := e.view()
			if len(out) < n {
				out = append(out, v)
			} else {
				out[n-1] = v
			}
			for j := len(out) - 1; j > 0 && betterMissed(out[j], out[j-1]); j-- {
				out[j], out[j-1] = out[j-1], out[j]
			}
		}
		s.mu.Unlock()
	}
	return out
}

// byReason returns the hosts that had responses left alone for a reason, the
// ones with the most bytes first.
func (d *domainSet) byReason(reason string, n int) []ReasonHost {
	var out []ReasonHost
	for i := range d.shards {
		s := &d.shards[i]
		s.mu.Lock()
		for _, e := range s.heap {
			h := ReasonHost{Domain: e.host}
			for _, r := range e.reasons {
				if r.reason != reason {
					continue
				}
				h.Requests += r.requests
				h.BytesIn += r.bytes
				h.Kinds = append(h.Kinds, HostReason{Reason: r.reason, Kind: kindNames[r.kind], Requests: r.requests, BytesIn: r.bytes})
			}
			if h.Requests > 0 {
				out = append(out, h)
			}
		}
		s.mu.Unlock()
	}
	slices.SortFunc(out, func(a, b ReasonHost) int {
		if a.BytesIn != b.BytesIn {
			return cmp.Compare(b.BytesIn, a.BytesIn)
		}
		if a.Requests != b.Requests {
			return cmp.Compare(b.Requests, a.Requests)
		}
		return strings.Compare(a.Domain, b.Domain)
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}
