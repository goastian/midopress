// Package flows keeps the last responses that went through the proxy, in memory,
// for the live list of the administration interface. It exists only while the
// list is on: turning it off forgets everything.
package flows

import (
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goastian/midopress/internal/rules"
)

// ClientRef says who made the request.
type ClientRef struct {
	Device string `json:"device"`
	Addr   string `json:"addr"`
}

// Flow is one response and what was done with it. The JSON is the Flow of
// web/src/api/types.ts.
type Flow struct {
	ID             string              `json:"id"`
	Time           time.Time           `json:"time"`
	Method         string              `json:"method"`
	Scheme         string              `json:"scheme"`
	Host           string              `json:"host"`
	Path           string              `json:"path"`
	Status         int                 `json:"status"`
	Client         ClientRef           `json:"client"`
	Kind           string              `json:"kind"`
	ContentType    string              `json:"content_type"`
	OutContentType string              `json:"out_content_type"`
	BytesIn        int64               `json:"bytes_in"`
	BytesOut       int64               `json:"bytes_out"`
	Decision       string              `json:"decision"` // transformed or passthrough
	Reason         string              `json:"reason"`
	Codec          string              `json:"codec"`
	Profile        string              `json:"profile"`
	DurationMS     float64             `json:"duration_ms"`
	CacheHit       bool                `json:"cache_hit"`
	Metric         string              `json:"metric"`
	Measure        float64             `json:"measure"`
	HasOriginal    bool                `json:"has_original"`
	HasResult      bool                `json:"has_result"`
	RequestHeaders map[string][]string `json:"request_headers"`
	ResponseHeader map[string][]string `json:"response_headers"`
}

// Body is what a flow kept of an image.
type Body struct {
	Data        []byte
	ContentType string
}

type entry struct {
	flow     Flow
	original *Body
	result   *Body
}

// Store is the ring of flows. It is safe for concurrent use.
type Store struct {
	capacity int
	budget   int64
	enabled  atomic.Bool
	seq      atomic.Uint64
	start    string

	mu     sync.Mutex
	ring   []*entry // oldest first, at most capacity
	byID   map[string]*entry
	held   int64    // bytes of bodies
	bodied []string // ids with bodies, oldest first
	total  uint64   // flows added since the list was last emptied

	subMu sync.Mutex
	subs  map[chan Flow]struct{}
}

// NewStore returns a store of the given capacity. captureBytes bounds the memory
// used by the bodies of images, 0 keeps none.
func NewStore(capacity int, captureBytes int64) *Store {
	if capacity < 1 {
		capacity = 1
	}
	return &Store{
		capacity: capacity, budget: captureBytes, byID: map[string]*entry{},
		subs: map[chan Flow]struct{}{}, start: strconv.FormatInt(time.Now().Unix(), 36),
	}
}

// Capacity is how many flows are kept.
func (s *Store) Capacity() int { return s.capacity }

// Enabled reports whether flows are being kept.
func (s *Store) Enabled() bool { return s.enabled.Load() }

// SetEnabled turns the list on or off. Turning it off forgets everything.
func (s *Store) SetEnabled(on bool) {
	s.enabled.Store(on)
	if !on {
		s.Clear()
	}
}

// CaptureEnabled reports whether the bodies of images are kept.
func (s *Store) CaptureEnabled() bool { return s.Enabled() && s.budget > 0 }

// Add stores a flow and returns it with its id. The bodies are kept when they
// fit in the budget. It does nothing, and returns false, while the list is off.
func (s *Store) Add(f Flow, original, result *Body) (Flow, bool) {
	if !s.enabled.Load() {
		return f, false
	}
	f.ID = s.start + "-" + strconv.FormatUint(s.seq.Add(1), 36)
	if f.Time.IsZero() {
		f.Time = time.Now()
	}
	e := &entry{}
	s.mu.Lock()
	if s.budget > 0 && original != nil && result != nil {
		size := int64(len(original.Data) + len(result.Data))
		if size <= s.budget/4 {
			for s.held+size > s.budget && len(s.bodied) > 0 {
				s.dropBodyLocked(s.bodied[0])
			}
			e.original, e.result = original, result
			s.held += size
			s.bodied = append(s.bodied, f.ID)
		}
	}
	f.HasOriginal, f.HasResult = e.original != nil, e.result != nil
	e.flow = f
	if len(s.ring) == s.capacity {
		old := s.ring[0]
		s.ring = s.ring[1:]
		s.dropBodyLocked(old.flow.ID)
		delete(s.byID, old.flow.ID)
	}
	s.ring = append(s.ring, e)
	s.byID[f.ID] = e
	s.total++
	s.mu.Unlock()

	s.publish(f)
	return f, true
}

func (s *Store) dropBodyLocked(id string) {
	e, ok := s.byID[id]
	if !ok || e.original == nil {
		return
	}
	s.held -= int64(len(e.original.Data) + len(e.result.Data))
	e.original, e.result = nil, nil
	e.flow.HasOriginal, e.flow.HasResult = false, false
	for i, b := range s.bodied {
		if b == id {
			s.bodied = append(s.bodied[:i], s.bodied[i+1:]...)
			break
		}
	}
}

// Clear forgets every flow and body.
func (s *Store) Clear() {
	s.mu.Lock()
	s.ring, s.byID, s.bodied, s.held, s.total = nil, map[string]*entry{}, nil, 0, 0
	s.mu.Unlock()
}

// ListOptions narrows a listing.
type ListOptions struct {
	Filter *rules.Expr // nil for every flow
	Kind   string      // empty for every kind
	Limit  int         // 0 means 100, at most 500
}

// List returns the newest flows that match, newest first, and the number of
// flows held.
func (s *Store) List(o ListOptions) ([]Flow, int) {
	limit := o.Limit
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 500)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Flow, 0, min(limit, len(s.ring)))
	for i := len(s.ring) - 1; i >= 0 && len(out) < limit; i-- {
		f := s.ring[i].flow
		if o.Kind != "" && f.Kind != o.Kind {
			continue
		}
		if o.Filter != nil && !o.Filter.Match(f.RulesContext()) {
			continue
		}
		out = append(out, f)
	}
	return out, len(s.ring)
}

// Get returns one flow.
func (s *Store) Get(id string) (Flow, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byID[id]
	if !ok {
		return Flow{}, false
	}
	return e.flow, true
}

// Bodies returns what was kept of a flow's image: the original and the result.
func (s *Store) Bodies(id string) (original, result *Body, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, found := s.byID[id]
	if !found || e.original == nil {
		return nil, nil, false
	}
	return e.original, e.result, true
}

// All returns every flow, oldest first.
func (s *Store) All() []Flow {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Flow, len(s.ring))
	for i, e := range s.ring {
		out[i] = e.flow
	}
	return out
}

// HeldBytes is the memory used by the bodies.
func (s *Store) HeldBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.held
}

// Subscribe returns a channel of the new flows. A subscriber that can not keep
// up misses flows instead of slowing the proxy down. Call cancel when done.
func (s *Store) Subscribe() (flows <-chan Flow, cancel func()) {
	ch := make(chan Flow, 128)
	s.subMu.Lock()
	s.subs[ch] = struct{}{}
	s.subMu.Unlock()
	return ch, func() {
		s.subMu.Lock()
		delete(s.subs, ch)
		s.subMu.Unlock()
	}
}

func (s *Store) publish(f Flow) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- f:
		default:
		}
	}
}

// RulesContext lets the filter language look at a flow.
func (f Flow) RulesContext() *rules.Context {
	c := &rules.Context{
		Method: f.Method, URL: f.Scheme + "://" + f.Host + f.Path, Host: f.Host,
		ContentType: f.ContentType, Status: f.Status, HasResponse: true,
		ReqHeader: http.Header(f.RequestHeaders), RespHeader: http.Header(f.ResponseHeader),
	}
	if ap, err := netip.ParseAddrPort(f.Client.Addr); err == nil {
		c.Src = ap.Addr()
	} else if a, err := netip.ParseAddr(f.Client.Addr); err == nil {
		c.Src = a
	}
	return c
}

// SafeHeaders copies headers for display, hiding the values that are secrets.
func SafeHeaders(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, v := range h {
		switch http.CanonicalHeaderKey(k) {
		case "Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "Proxy-Authenticate", "X-Api-Key":
			hidden := make([]string, len(v))
			for i := range v {
				hidden[i] = "<hidden>"
			}
			out[k] = hidden
		default:
			out[k] = append([]string(nil), v...)
		}
	}
	return out
}
