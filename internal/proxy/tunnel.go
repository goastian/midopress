package proxy

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

var tunnelBuffers = sync.Pool{New: func() any {
	b := make([]byte, 32<<10)
	return &b
}}

// tunnelSet tracks the open tunnels so that they can be drained on shutdown.
type tunnelSet struct {
	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
	active atomic.Int64
	total  atomic.Uint64
	// bytes that went through tunnels, untouched: up is from the client
	up   atomic.Uint64
	down atomic.Uint64
}

func (s *tunnelSet) add(conns ...net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if s.conns == nil {
		s.conns = make(map[net.Conn]struct{})
	}
	for _, c := range conns {
		s.conns[c] = struct{}{}
	}
	s.wg.Add(1)
	s.active.Add(1)
	s.total.Add(1)
	return true
}

func (s *tunnelSet) remove(conns ...net.Conn) {
	s.mu.Lock()
	for _, c := range conns {
		delete(s.conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	s.active.Add(-1)
	s.wg.Done()
}

// shutdown refuses new tunnels and waits for the open ones to finish. When ctx
// ends first, the remaining connections are closed.
func (s *tunnelSet) shutdown(ctx context.Context) {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		s.mu.Lock()
		for c := range s.conns {
			_ = c.Close()
		}
		s.mu.Unlock()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
}

// tunnel copies bytes both ways between the client and the upstream until both
// directions are finished, then closes both connections. When one side stops
// sending, that is passed on as a half-close so the other side can still
// finish what it is sending.
func (s *tunnelSet) tunnel(client, upstream net.Conn) {
	if !s.add(client, upstream) {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	defer s.remove(client, upstream)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		pump(upstream, client, &s.up)
	}()
	go func() {
		defer wg.Done()
		pump(client, upstream, &s.down)
	}()
	wg.Wait()
}

// countingWriter adds what it writes to a counter.
type countingWriter struct {
	w io.Writer
	n *atomic.Uint64
}

func (c countingWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n.Add(uint64(n))
	return n, err
}

// pump copies src to dst, then passes the end of the stream on to dst.
func pump(dst, src net.Conn, count *atomic.Uint64) {
	bp := tunnelBuffers.Get().(*[]byte)
	// counted as it goes: a tunnel can stay open for hours, and the bytes of
	// one that is still open belong in the statistics
	_, err := io.CopyBuffer(countingWriter{dst, count}, src, *bp)
	tunnelBuffers.Put(bp)
	if err != nil {
		_ = dst.Close()
		_ = src.Close()
		return
	}

	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = dst.Close()
}
