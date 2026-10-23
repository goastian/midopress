// Package stats is the live aggregator behind the dashboard of the
// administration interface: counters by kind, codec, profile and decision, time
// series, latency quantiles, top domains and clients.
//
// Record is meant for the hot path of the proxy: it takes no global lock and
// does not allocate once the labels it sees have been seen before.
package stats

import (
	"cmp"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ActiveWindow is how long a client counts as active after its last request
// when no other window is given.
const ActiveWindow = 2 * time.Minute

const notifyEvery = 2 * time.Second

// Options configures a Collector.
type Options struct {
	// MaxDomains is the number of hosts tracked, default 2000.
	MaxDomains int
	// MaxClients is the number of clients tracked, default 2000.
	MaxClients int
	// Now is the clock, default time.Now.
	Now func() time.Time
}

type state struct {
	stripes  [nStripes]stripe
	codecs   *labels
	profiles *labels
	reasons  *labels
	causes   *labels
	domains  *domainSet
	missed   *domainSet
	clients  *clientSet
}

// Collector aggregates Events. All its methods are safe for concurrent use.
type Collector struct {
	opts      Options
	st        atomic.Pointer[state]
	domainsOn atomic.Bool
	onClient  atomic.Pointer[func(Client)]
	mu        sync.Mutex
}

// New returns an empty Collector. The aggregation of domains starts off.
func New(o Options) *Collector {
	if o.MaxDomains <= 0 {
		o.MaxDomains = 2000
	}
	if o.MaxClients <= 0 {
		o.MaxClients = 2000
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	c := &Collector{opts: o}
	c.st.Store(c.newState())
	return c
}

func (c *Collector) newState() *state {
	return &state{
		codecs:   newLabels(),
		profiles: newLabels(),
		reasons:  newLabels(),
		causes:   newLabels(),
		domains:  newDomainSet(c.opts.MaxDomains, c.domainsOn.Load(), false),
		missed:   newDomainSet(c.opts.MaxDomains, c.domainsOn.Load(), true),
		clients:  &clientSet{max: c.opts.MaxClients},
	}
}

func nonNeg(n int64) uint64 {
	if n < 0 {
		return 0
	}
	return uint64(n)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// Record accounts for one response.
func (c *Collector) Record(e Event) {
	s := c.st.Load()
	t := e.Time
	if t.IsZero() {
		t = c.opts.Now()
	}
	unix := t.Unix()
	in, out := nonNeg(e.BytesIn), nonNeg(e.BytesOut)
	var tr uint64
	if e.Transformed {
		tr = 1
	}
	ki := kindIndex(e.Kind)
	ci := s.codecs.index(orDefault(e.Codec, "identity"))
	pi := s.profiles.index(orDefault(e.Profile, NoProfile))
	ri := s.reasons.index(orDefault(e.Reason, "unknown"))
	cai := -1
	if e.Cause != "" {
		cai = s.causes.index(orDefault(e.Reason, "unknown") + causeSep + e.Cause)
	}
	lat := -1
	if e.Duration > 0 {
		lat = latIndex(int64(e.Duration))
	}

	start := int(rand.Uint32())
	var st *stripe
	for i := range nStripes {
		if c := &s.stripes[(start+i)&(nStripes-1)]; c.mu.TryLock() {
			st = c
			break
		}
	}
	if st == nil {
		st = &s.stripes[start&(nStripes-1)]
		st.mu.Lock()
	}
	st.record(unix, ki, ci, pi, ri, cai, tr, in, out, lat)
	st.mu.Unlock()

	if e.Host != "" && c.domainsOn.Load() {
		ev := domEvent{tr: tr, in: in, out: out, kind: ki}
		if !e.Transformed {
			ev.reason = orDefault(e.Reason, "unknown")
		}
		s.domains.add(e.Host, ev)
		s.missed.add(e.Host, ev)
	}
	if e.ClientKey != "" {
		ns := t.UnixNano()
		cl, created := s.clients.get(e.ClientKey, ns)
		cl.touch(ns)
		cl.setInfo(e.Device, e.Addr, e.UserAgent, e.Profile)
		cl.kinds[ki].add(tr, in, out)
		c.notify(cl, ns, created)
	}
}

func (c *Collector) notify(cl *client, ns int64, created bool) {
	fn := c.onClient.Load()
	if fn == nil {
		return
	}
	if !created {
		last := cl.notified.Load()
		if last != 0 && ns-last < int64(notifyEvery) {
			return
		}
		if !cl.notified.CompareAndSwap(last, ns) {
			return
		}
	} else {
		cl.notified.Store(ns)
	}
	(*fn)(cl.view(c.opts.Now(), ActiveWindow))
}

// SetDomains switches the aggregation by host on or off. Switching it off
// drops what was tracked and makes Record ignore Event.Host.
func (c *Collector) SetDomains(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.domainsOn.Store(on)
	st := c.st.Load()
	st.domains.setEnabled(on)
	st.missed.setEnabled(on)
}

// Domains reports whether the aggregation by host is on.
func (c *Collector) Domains() bool { return c.domainsOn.Load() }

// NoProfile is the profile shown for the responses that no profile applied to,
// the ones that were left alone before a profile was chosen.
const NoProfile = "-"

// causeSep separates the reason from the cause in the labels of causes.
const causeSep = "\x00"

// Snapshot returns the traffic numbers; TopDomains has up to topDomains hosts,
// the ones that saved the most bytes.
func (c *Collector) Snapshot(topDomains int) Snapshot {
	s := c.st.Load()
	now := c.opts.Now()
	snap := Snapshot{
		ByKind:     make(map[string]Bucket, len(kindNames)),
		ByCodec:    map[string]Bucket{},
		ByProfile:  map[string]Bucket{},
		ByDecision: map[string]uint64{},
	}
	var kinds [len(kindNames)]Bucket
	var codecs, profiles [nLabels]Bucket
	var reasons [nLabels]uint64
	var reasonKinds [nLabels][len(kindNames)]Bucket
	var causes [nLabels]Bucket
	var hist [latBuckets]uint64
	var samples uint64
	unix := now.Unix()
	snap.Series = Series{Seconds: newPoints(secondPoints, unix, 1), Minutes: newPoints(minutePoints, unix/60, 60)}
	for i := range s.stripes {
		st := &s.stripes[i]
		st.mu.Lock()
		for k := range kinds {
			kinds[k] = kinds[k].plus(st.kinds[k])
		}
		for k := range nLabels {
			codecs[k] = codecs[k].plus(st.codecs[k])
			profiles[k] = profiles[k].plus(st.profiles[k])
			reasons[k] += st.reasons[k]
			causes[k] = causes[k].plus(st.causes[k])
			for ki := range reasonKinds[k] {
				reasonKinds[k][ki] = reasonKinds[k][ki].plus(st.reasonKinds[k][ki])
			}
		}
		samples += st.addHist(&hist, unix/60)
		addPoints(snap.Series.Seconds, st.seconds[:], unix)
		addPoints(snap.Series.Minutes, st.minutes[:], unix/60)
		st.mu.Unlock()
	}
	var total Bucket
	for i, b := range kinds {
		total = total.plus(b)
		if b.Requests > 0 {
			snap.ByKind[kindNames[i]] = b
		}
	}
	snap.Totals = newTotals(total)
	for i, n := range s.codecs.set.Load().names {
		snap.ByCodec[n] = codecs[i]
	}
	for i, n := range s.profiles.set.Load().names {
		snap.ByProfile[n] = profiles[i]
	}
	snap.ByReason = []ReasonStat{}
	for i, n := range s.reasons.set.Load().names {
		snap.ByDecision[n] = reasons[i]
		for ki, b := range reasonKinds[i] {
			if b.Requests > 0 {
				snap.ByReason = append(snap.ByReason, ReasonStat{Reason: n, Kind: kindNames[ki], Requests: b.Requests, BytesIn: b.BytesIn, BytesOut: b.BytesOut})
			}
		}
	}
	slices.SortFunc(snap.ByReason, func(a, b ReasonStat) int {
		if a.BytesIn != b.BytesIn {
			return cmp.Compare(b.BytesIn, a.BytesIn)
		}
		return cmp.Or(strings.Compare(a.Reason, b.Reason), strings.Compare(a.Kind, b.Kind))
	})
	snap.ByCause = []CauseStat{}
	for i, n := range s.causes.set.Load().names {
		if causes[i].Requests == 0 {
			continue
		}
		reason, cause, _ := strings.Cut(n, causeSep)
		snap.ByCause = append(snap.ByCause, CauseStat{Reason: reason, Cause: cause, Requests: causes[i].Requests, BytesIn: causes[i].BytesIn})
	}
	slices.SortFunc(snap.ByCause, func(a, b CauseStat) int {
		if a.BytesIn != b.BytesIn {
			return cmp.Compare(b.BytesIn, a.BytesIn)
		}
		return cmp.Or(strings.Compare(a.Reason, b.Reason), strings.Compare(a.Cause, b.Cause))
	})
	if samples > 0 {
		snap.Latency = Latency{
			P50Ms:   quantile(&hist, samples, 0.50),
			P95Ms:   quantile(&hist, samples, 0.95),
			P99Ms:   quantile(&hist, samples, 0.99),
			Samples: samples,
		}
	}
	if c.domainsOn.Load() {
		d := s.domains.top(topDomains)
		snap.TopDomains = &d
		m := s.missed.topMissed(topMissed)
		snap.TopMissed = &m
	}
	return snap
}

// topMissed is how many hosts of the ones that lost the most bytes a snapshot
// carries.
const topMissed = 10

// MissedByReason lists the hosts that had responses left alone for a reason,
// the ones with the most bytes first, up to n. It is empty while the
// aggregation by host is off.
func (c *Collector) MissedByReason(reason string, n int) []ReasonHost {
	if !c.domainsOn.Load() {
		return []ReasonHost{}
	}
	out := c.st.Load().missed.byReason(reason, n)
	if out == nil {
		out = []ReasonHost{}
	}
	return out
}

// Clients lists the tracked clients, the most recently seen first. A client is
// active when it was seen within activeWithin.
func (c *Collector) Clients(activeWithin time.Duration) []Client {
	s := c.st.Load()
	now := c.opts.Now()
	var out []Client
	s.clients.clients.Range(func(_, v any) bool {
		out = append(out, v.(*client).view(now, activeWithin))
		return true
	})
	slices.SortFunc(out, func(a, b Client) int {
		if r := b.LastSeen.Compare(a.LastSeen); r != 0 {
			return r
		}
		return strings.Compare(a.Key, b.Key)
	})
	return out
}

// Client returns one client by key.
func (c *Collector) Client(key string) (Client, bool) {
	v, ok := c.st.Load().clients.clients.Load(key)
	if !ok {
		return Client{}, false
	}
	return v.(*client).view(c.opts.Now(), ActiveWindow), true
}

// Devices returns the totals of each device that made requests, by id. It is
// the sum of its clients, including the ones evicted since.
func (c *Collector) Devices() map[string]DeviceStat {
	cs := c.st.Load().clients
	out := cs.foldedDevices()
	now := c.opts.Now()
	cs.clients.Range(func(_, v any) bool {
		cl := v.(*client)
		if cl.info.Load().device != "" {
			v := cl.view(now, ActiveWindow)
			addDevice(out, v)
		}
		return true
	})
	return out
}

// ActiveClients counts the clients seen within the given time.
func (c *Collector) ActiveClients(within time.Duration) int {
	cut := c.opts.Now().Add(-within).UnixNano()
	n := 0
	c.st.Load().clients.clients.Range(func(_, v any) bool {
		if v.(*client).lastSeen.Load() >= cut {
			n++
		}
		return true
	})
	return n
}

// OnClient sets the function called when a client is first seen and then at
// most once every two seconds per client while it keeps making requests. It
// runs on the goroutine that recorded, with no lock held, so it must not
// block. A nil fn removes it.
func (c *Collector) OnClient(fn func(Client)) {
	if fn == nil {
		c.onClient.Store(nil)
		return
	}
	c.onClient.Store(&fn)
}

// Begin marks a request of the client as in flight until done is called, which
// is safe to call more than once.
func (c *Collector) Begin(clientKey string) (done func()) {
	if clientKey == "" {
		return func() {}
	}
	cl, created := c.st.Load().clients.get(clientKey, c.opts.Now().UnixNano())
	cl.inFlight.Add(1)
	if created {
		c.notify(cl, cl.firstSeen, true)
	}
	var once atomic.Bool
	return func() {
		if once.CompareAndSwap(false, true) {
			cl.inFlight.Add(-1)
		}
	}
}

// Reset forgets everything. Requests in flight keep counting on the old state,
// so in_flight starts again from zero.
func (c *Collector) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.st.Store(c.newState())
}
