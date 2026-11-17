package quota

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/goastian/midopress/internal/filelock"
)

var (
	ErrRequests    = errors.New("device request quota exhausted")
	ErrBytes       = errors.New("device byte quota exhausted")
	ErrConcurrency = errors.New("device concurrency quota exhausted")
	ErrUnavailable = errors.New("device quota storage unavailable")
)

type Limits struct {
	Requests    uint64 `json:"requests" yaml:"requests"`
	Bytes       uint64 `json:"bytes" yaml:"bytes"`
	Concurrency int    `json:"concurrency" yaml:"concurrency"`
}

func (l Limits) Valid() bool {
	return l.Concurrency >= 0 && l.Concurrency <= 10000 && l.Requests <= 1<<53 && l.Bytes <= 1<<53
}

type Usage struct {
	Requests    uint64    `json:"requests"`
	Bytes       uint64    `json:"bytes"`
	Active      int       `json:"active"`
	Tunnels     int       `json:"tunnels"`
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
	Limits      Limits    `json:"limits"`
}

type count struct {
	Requests uint64 `json:"requests"`
	Bytes    uint64 `json:"bytes"`
}
type state struct {
	Version int              `json:"version"`
	Start   int64            `json:"window_start"`
	Window  int64            `json:"window_seconds"`
	Devices map[string]count `json:"devices"`
}
type Ledger struct {
	mu       sync.Mutex
	path     string
	state    state
	active   map[string]int
	tunnels  map[string]int
	window   time.Duration
	defaults Limits
	limits   func(string) *Limits
	now      func() time.Time
	unlock   func()
	closed   bool
}

func Open(path string, window time.Duration, defaults Limits, limits func(string) *Limits) (*Ledger, error) {
	if path == "" || window < time.Second || window%time.Second != 0 || window > 365*24*time.Hour || !defaults.Valid() {
		return nil, ErrUnavailable
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("%w: directory", ErrUnavailable)
	}
	unlock, err := filelock.Acquire(path+".lock", true)
	if err != nil {
		return nil, fmt.Errorf("%w: another owner or lock failure", ErrUnavailable)
	}
	l := &Ledger{path: path, window: window.Truncate(time.Second), defaults: defaults, limits: limits, now: time.Now, active: map[string]int{}, tunnels: map[string]int{}, unlock: unlock}
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) > 16<<20 || json.Unmarshal(data, &l.state) != nil || l.state.Version != 1 || l.state.Devices == nil || l.state.Start <= 0 || l.state.Start > 1<<53 || l.state.Window <= 0 || l.state.Window > 365*24*3600 || len(l.state.Devices) > 10000 {
			unlock()
			return nil, ErrUnavailable
		}
		for _, c := range l.state.Devices {
			if c.Requests > 1<<53 || c.Bytes > 1<<53 {
				unlock()
				return nil, ErrUnavailable
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		unlock()
		return nil, ErrUnavailable
	}
	if l.state.Devices == nil {
		l.state = state{Version: 1, Start: l.now().Unix(), Window: int64(l.window / time.Second), Devices: map[string]count{}}
		if err := l.persist(); err != nil {
			unlock()
			return nil, err
		}
	}
	return l, nil
}

func (l *Ledger) persist() error {
	data, err := json.Marshal(l.state)
	if err != nil {
		return ErrUnavailable
	}
	f, err := os.CreateTemp(filepath.Dir(l.path), ".usage-*")
	if err != nil {
		return ErrUnavailable
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, l.path)
	}
	if err != nil {
		return ErrUnavailable
	}
	dir, err := os.Open(filepath.Dir(l.path))
	if err != nil {
		return ErrUnavailable
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return ErrUnavailable
	}
	return nil
}

func (l *Ledger) resetWindow() error {
	now := l.now().Unix()
	if now < l.state.Start+l.state.Window {
		return nil
	}
	prev := l.state
	l.state = state{Version: 1, Start: now, Window: int64(l.window / time.Second), Devices: map[string]count{}}
	if err := l.persist(); err != nil {
		l.state = prev
		return err
	}
	return nil
}

func (l *Ledger) effective(id string) Limits {
	if l.limits != nil {
		if v := l.limits(id); v != nil {
			return *v
		}
	}
	return l.defaults
}

type Lease struct {
	l      *Ledger
	id     string
	active bool
	closed bool
	tunnel bool
}

func (l *Ledger) Begin(id string) (*Lease, error) {
	return l.begin(id, false)
}
func (l *Ledger) BeginTunnel(id string) (*Lease, error) { return l.begin(id, true) }
func (l *Ledger) counts(tunnel bool) map[string]int {
	if tunnel {
		return l.tunnels
	}
	return l.active
}
func (l *Ledger) begin(id string, tunnel bool) (*Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || id == "" || len(id) > 64 {
		return nil, ErrUnavailable
	}
	if err := l.resetWindow(); err != nil {
		return nil, err
	}
	limits := l.effective(id)
	c, exists := l.state.Devices[id]
	if !exists && len(l.state.Devices) >= 10000 {
		return nil, ErrUnavailable
	}
	if limits.Requests > 0 && c.Requests >= limits.Requests {
		return nil, ErrRequests
	}
	if limits.Bytes > 0 && c.Bytes >= limits.Bytes {
		return nil, ErrBytes
	}
	if limits.Concurrency > 0 && l.counts(tunnel)[id] >= limits.Concurrency {
		return nil, ErrConcurrency
	}
	old := c
	if c.Requests >= 1<<53 {
		return nil, ErrUnavailable
	}
	c.Requests++
	l.state.Devices[id] = c
	if err := l.persist(); err != nil {
		if exists {
			l.state.Devices[id] = old
		} else {
			delete(l.state.Devices, id)
		}
		return nil, err
	}
	l.counts(tunnel)[id]++
	return &Lease{l: l, id: id, active: true, tunnel: tunnel}, nil
}

// Reserve persists an allowance before bytes leave the proxy. A crash may charge
// an unfinished allowance, but can never refund traffic that already left.
type Reservation struct {
	Size   int
	window int64
}

func (s *Lease) Reserve(n int) (Reservation, error) {
	if n <= 0 {
		return Reservation{}, nil
	}
	l := s.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if s.closed || l.closed {
		return Reservation{}, ErrUnavailable
	}
	if err := l.resetWindow(); err != nil {
		return Reservation{}, err
	}
	c := l.state.Devices[s.id]
	limits := l.effective(s.id)
	allowed := uint64(n)
	if limits.Bytes > 0 {
		if c.Bytes >= limits.Bytes {
			return Reservation{}, ErrBytes
		}
		allowed = min(allowed, limits.Bytes-c.Bytes)
	}
	if allowed > 1<<53-c.Bytes {
		return Reservation{}, ErrUnavailable
	}
	old := c
	c.Bytes += allowed
	l.state.Devices[s.id] = c
	if err := l.persist(); err != nil {
		l.state.Devices[s.id] = old
		return Reservation{}, err
	}
	return Reservation{Size: int(allowed), window: l.state.Start}, nil
}

func (s *Lease) Refund(res Reservation, used int) {
	n := res.Size - used
	if n <= 0 {
		return
	}
	l := s.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || res.window != l.state.Start {
		return
	}
	c := l.state.Devices[s.id]
	old := c
	c.Bytes -= min(c.Bytes, uint64(n))
	l.state.Devices[s.id] = c
	if l.persist() != nil {
		l.state.Devices[s.id] = old
	}
}

func (s *Lease) Close() {
	l := s.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.active {
		l.counts(s.tunnel)[s.id]--
		s.active = false
	}
}
func (l *Ledger) View(id string) Usage {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.state.Devices[id]
	return Usage{Requests: c.Requests, Bytes: c.Bytes, Active: l.active[id], Tunnels: l.tunnels[id], WindowStart: time.Unix(l.state.Start, 0).UTC(), WindowEnd: time.Unix(l.state.Start+l.state.Window, 0).UTC(), Limits: l.effective(id)}
}
func (l *Ledger) RetryAfter() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return max(time.Second, time.Unix(l.state.Start+l.state.Window, 0).Sub(l.now()))
}
func (l *Ledger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		l.closed = true
		l.unlock()
	}
}
