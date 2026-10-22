package stats

import (
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type clientInfo struct {
	device, addr, userAgent, profile string
}

type client struct {
	key       string
	firstSeen int64
	lastSeen  atomic.Int64
	notified  atomic.Int64
	inFlight  atomic.Int64
	info      atomic.Pointer[clientInfo]
	kinds     [len(kindNames)]cell
}

// touch moves last_seen forward, with a resolution of a millisecond so that a
// busy client does not write the same cache line on every request.
func (c *client) touch(ns int64) {
	for {
		cur := c.lastSeen.Load()
		if ns-cur < int64(time.Millisecond) || c.lastSeen.CompareAndSwap(cur, ns) {
			return
		}
	}
}

func (c *client) setInfo(device, addr, ua, profile string) {
	cur := c.info.Load()
	if (device == "" || device == cur.device) && (addr == "" || addr == cur.addr) &&
		(ua == "" || ua == cur.userAgent) && (profile == "" || profile == cur.profile) {
		return
	}
	n := *cur
	if device != "" {
		n.device = strings.Clone(device)
	}
	if addr != "" {
		n.addr = strings.Clone(addr)
	}
	if ua != "" {
		n.userAgent = strings.Clone(ua)
	}
	if profile != "" {
		n.profile = strings.Clone(profile)
	}
	c.info.Store(&n)
}

func (c *client) view(now time.Time, within time.Duration) Client {
	info := c.info.Load()
	v := Client{
		Key:       c.key,
		Device:    info.device,
		Addr:      info.addr,
		UserAgent: info.userAgent,
		FirstSeen: time.Unix(0, c.firstSeen),
		LastSeen:  time.Unix(0, c.lastSeen.Load()),
		InFlight:  c.inFlight.Load(),
		Profile:   info.profile,
		ByKind:    map[string]Bucket{},
	}
	v.Active = now.Sub(v.LastSeen) <= within
	var t Bucket
	for i := range c.kinds {
		b := c.kinds[i].load()
		if b.Requests > 0 {
			v.ByKind[kindNames[i]] = b
		}
		t = t.plus(b)
	}
	v.Requests, v.BytesIn, v.BytesOut = t.Requests, t.BytesIn, t.BytesOut
	v.Saved = int64(t.BytesIn) - int64(t.BytesOut)
	return v
}

// clientSet holds the clients and devices; it is bounded and evicts the least
// recently seen clients in batches, so the cost of eviction is amortized.
type clientSet struct {
	max     int
	clients sync.Map
	n       atomic.Int64
	evictMu sync.Mutex
	devMu   sync.Mutex
	folded  map[string]DeviceStat
}

func addDevice(m map[string]DeviceStat, v Client) {
	if v.Requests == 0 {
		return
	}
	d := m[v.Device]
	d.Requests += v.Requests
	d.BytesIn += v.BytesIn
	d.BytesOut += v.BytesOut
	d.Saved += v.Saved
	if v.LastSeen.After(d.LastSeen) {
		d.LastSeen = v.LastSeen
	}
	m[v.Device] = d
}

func (s *clientSet) foldedDevices() map[string]DeviceStat {
	s.devMu.Lock()
	defer s.devMu.Unlock()
	out := make(map[string]DeviceStat, len(s.folded))
	for k, v := range s.folded {
		out[k] = v
	}
	return out
}

// fold keeps the totals of an evicted client that has a device, so that the
// numbers of a device do not go back when its client is forgotten. The number
// of devices kept is bounded like the clients.
func (s *clientSet) fold(c *client) {
	v := c.view(time.Unix(0, c.lastSeen.Load()), 0)
	if v.Device == "" {
		return
	}
	s.devMu.Lock()
	defer s.devMu.Unlock()
	if _, ok := s.folded[v.Device]; !ok && len(s.folded) >= s.max {
		return
	}
	if s.folded == nil {
		s.folded = map[string]DeviceStat{}
	}
	addDevice(s.folded, v)
}

func (s *clientSet) get(key string, ns int64) (*client, bool) {
	if v, ok := s.clients.Load(key); ok {
		return v.(*client), false
	}
	key = strings.Clone(key)
	c := &client{key: key, firstSeen: ns}
	c.info.Store(&clientInfo{})
	c.lastSeen.Store(ns)
	v, loaded := s.clients.LoadOrStore(key, c)
	if loaded {
		return v.(*client), false
	}
	if s.n.Add(1) > int64(s.max) {
		s.evict()
	}
	return c, true
}

func (s *clientSet) evict() {
	s.evictMu.Lock()
	defer s.evictMu.Unlock()
	over := int(s.n.Load()) - s.max
	if over <= 0 {
		return
	}
	type kv struct {
		key  string
		last int64
	}
	all := make([]kv, 0, s.n.Load())
	s.clients.Range(func(k, v any) bool {
		all = append(all, kv{k.(string), v.(*client).lastSeen.Load()})
		return true
	})
	slices.SortFunc(all, func(a, b kv) int {
		switch {
		case a.last < b.last:
			return -1
		case a.last > b.last:
			return 1
		}
		return 0
	})
	for _, e := range all[:min(len(all), over+s.max/16)] {
		if v, ok := s.clients.LoadAndDelete(e.key); ok {
			s.n.Add(-1)
			s.fold(v.(*client))
		}
	}
}
