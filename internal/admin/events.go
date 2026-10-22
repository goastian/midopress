package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goastian/midopress/internal/stats"
)

type sseEvent struct {
	name string
	data []byte
}

// eventHub fans out what the interface follows live. A listener that falls
// behind misses events, it never slows anything down: the pages ask for the full
// state again after a gap.
type eventHub struct {
	s    *Server
	mu   sync.Mutex
	subs map[chan sseEvent]struct{}
	stop chan struct{}
	once sync.Once
	n    atomic.Int32
}

func newEventHub(s *Server) *eventHub {
	h := &eventHub{s: s, subs: map[chan sseEvent]struct{}{}, stop: make(chan struct{})}
	if s.d.Collector != nil {
		s.d.Collector.OnClient(func(c stats.Client) { h.broadcast("client", c) })
	}
	go h.tick()
	return h
}

func (h *eventHub) close() { h.once.Do(func() { close(h.stop) }) }

func (h *eventHub) subscribe() (chan sseEvent, func()) {
	ch := make(chan sseEvent, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	h.n.Add(1)
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
		h.n.Add(-1)
	}
}

func (h *eventHub) broadcast(name string, v any) {
	if h.n.Load() == 0 {
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	ev := sseEvent{name, data}
	h.mu.Lock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
	h.mu.Unlock()
}

func (h *eventHub) tick() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if h.n.Load() > 0 {
				h.broadcast("stats", h.s.snapshot())
			}
		case <-h.stop:
			return
		}
	}
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	hub, cancelHub := s.events.subscribe()
	defer cancelHub()
	var flowCh <-chan flowEvent
	if s.d.Flows != nil {
		ch, cancel := s.d.Flows.Subscribe()
		defer cancel()
		fc := make(chan flowEvent, 128)
		done := make(chan struct{})
		defer close(done)
		go func() {
			for {
				select {
				case f, ok := <-ch:
					if !ok {
						return
					}
					select {
					case fc <- flowEvent{f}:
					default:
					}
				case <-done:
					return
				}
			}
		}()
		flowCh = fc
	}
	var logCh <-chan LogLine
	if s.d.Logs != nil {
		ch, cancel := s.d.Logs.Subscribe()
		defer cancel()
		logCh = ch
	}

	write := func(name string, data []byte) bool {
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	// the first thing a page needs, so that it does not wait for the next tick
	if data, err := json.Marshal(s.snapshot()); err == nil && !write("stats", data) {
		return
	}
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case ev := <-hub:
			if !write(ev.name, ev.data) {
				return
			}
		case f := <-flowCh:
			if data, err := json.Marshal(f.Flow); err == nil && !write("flow", data) {
				return
			}
		case l := <-logCh:
			if data, err := json.Marshal(l); err == nil && !write("log", data) {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case <-s.events.stop:
			return
		case <-r.Context().Done():
			return
		}
	}
}
