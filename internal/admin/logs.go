package admin

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// LogLine is a line of the log, as the interface shows it.
type LogLine struct {
	Time    time.Time         `json:"time"`
	Level   string            `json:"level"`
	Message string            `json:"message"`
	Attrs   map[string]string `json:"attrs"`
}

// LogHub keeps the last lines of the log and hands new ones to whoever
// listens. It wraps a handler, which still writes where it did before. What is
// kept is what the handler would write: the level filters first, so the lines
// never have more than the process log has, and there are no URLs unless
// log.urls is on.
type LogHub struct {
	inner  slog.Handler
	max    int
	mu     sync.Mutex
	lines  []LogLine
	subs   map[chan LogLine]struct{}
	attrs  []slog.Attr
	groups []string
	parent *LogHub
}

// NewLogHub wraps inner and keeps up to max lines.
func NewLogHub(inner slog.Handler, max int) *LogHub {
	return &LogHub{inner: inner, max: max, subs: map[chan LogLine]struct{}{}}
}

func (h *LogHub) root() *LogHub {
	if h.parent != nil {
		return h.parent
	}
	return h
}

// Enabled implements slog.Handler.
func (h *LogHub) Enabled(ctx context.Context, l slog.Level) bool { return h.inner.Enabled(ctx, l) }

// Handle implements slog.Handler.
func (h *LogHub) Handle(ctx context.Context, r slog.Record) error {
	attrs := map[string]string{}
	add := func(a slog.Attr) {
		key := a.Key
		if len(h.groups) > 0 {
			key = strings.Join(h.groups, ".") + "." + key
		}
		attrs[key] = fmt.Sprint(a.Value.Resolve().Any())
	}
	for _, a := range h.attrs {
		add(a)
	}
	r.Attrs(func(a slog.Attr) bool { add(a); return true })
	line := LogLine{Time: r.Time, Level: r.Level.String(), Message: r.Message, Attrs: attrs}
	if line.Time.IsZero() {
		line.Time = time.Now()
	}
	root := h.root()
	root.mu.Lock()
	root.lines = append(root.lines, line)
	if len(root.lines) > root.max {
		root.lines = root.lines[len(root.lines)-root.max:]
	}
	for ch := range root.subs {
		select {
		case ch <- line:
		default:
		}
	}
	root.mu.Unlock()
	return h.inner.Handle(ctx, r)
}

// WithAttrs implements slog.Handler.
func (h *LogHub) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &LogHub{inner: h.inner.WithAttrs(attrs), parent: h.root(), attrs: append(append([]slog.Attr(nil), h.attrs...), attrs...), groups: h.groups}
}

// WithGroup implements slog.Handler.
func (h *LogHub) WithGroup(name string) slog.Handler {
	return &LogHub{inner: h.inner.WithGroup(name), parent: h.root(), attrs: h.attrs, groups: append(append([]string(nil), h.groups...), name)}
}

// Lines returns the kept lines, oldest first.
func (h *LogHub) Lines() []LogLine {
	root := h.root()
	root.mu.Lock()
	defer root.mu.Unlock()
	return append([]LogLine(nil), root.lines...)
}

// Subscribe returns the new lines as they come; cancel stops it.
func (h *LogHub) Subscribe() (<-chan LogLine, func()) {
	root := h.root()
	ch := make(chan LogLine, 256)
	root.mu.Lock()
	root.subs[ch] = struct{}{}
	root.mu.Unlock()
	return ch, func() {
		root.mu.Lock()
		delete(root.subs, ch)
		root.mu.Unlock()
	}
}
