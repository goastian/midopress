package proxy

import (
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// hostConn remembers the host a client asked for in its CONNECT request, so
// that the TLS layer can fall back to it when the ClientHello has no SNI.
type hostConn struct {
	net.Conn
	host string
}

func closeWrite(c net.Conn) error {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Close()
}

// peekedConn gives back a byte that was read to find out what protocol the
// client speaks.
type peekedConn struct {
	net.Conn
	first    byte
	consumed bool
}

func (c *peekedConn) Read(b []byte) (int, error) {
	if !c.consumed && len(b) > 0 {
		c.consumed = true
		b[0] = c.first
		return 1, nil
	}
	return c.Conn.Read(b)
}

// prefixConn reads from r first, which holds bytes the HTTP server had already
// buffered when the connection was hijacked.
type prefixConn struct {
	net.Conn
	r io.Reader
}

func (c *prefixConn) Read(b []byte) (int, error) { return c.r.Read(b) }
func (c *prefixConn) CloseWrite() error          { return closeWrite(c.Conn) }
func (c *peekedConn) CloseWrite() error          { return closeWrite(c.Conn) }
func (c *hostConn) CloseWrite() error            { return closeWrite(c.Conn) }

// streamConn adapts one HTTP/2 stream to a net.Conn. HTTP/2 connections can
// not be hijacked, so a CONNECT tunnel is the request body in one direction
// and the response body in the other.
type streamConn struct {
	rc     *http.ResponseController
	w      io.Writer
	body   io.ReadCloser
	remote net.Addr

	mu      sync.Mutex
	isClose atomic.Bool
	closed  chan struct{}
}

func newStreamConn(w http.ResponseWriter, body io.ReadCloser, remote string) *streamConn {
	return &streamConn{
		rc:     http.NewResponseController(w),
		w:      w,
		body:   body,
		remote: addr(remote),
		closed: make(chan struct{}),
	}
}

func (c *streamConn) Read(b []byte) (int, error) { return c.body.Read(b) }

func (c *streamConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.isClose.Load() {
		return 0, net.ErrClosed
	}
	n, err := c.w.Write(b)
	if err == nil {
		err = c.rc.Flush()
	}
	return n, err
}

// Close ends the stream. It waits for a write in progress, because the
// ResponseWriter must not be used once the handler returns.
func (c *streamConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.isClose.CompareAndSwap(false, true) {
		_ = c.body.Close()
		close(c.closed)
	}
	return nil
}

func (c *streamConn) RemoteAddr() net.Addr             { return c.remote }
func (c *streamConn) LocalAddr() net.Addr              { return addr("") }
func (c *streamConn) SetDeadline(time.Time) error      { return nil }
func (c *streamConn) SetReadDeadline(time.Time) error  { return nil }
func (c *streamConn) SetWriteDeadline(time.Time) error { return nil }

type addr string

func (a addr) String() string  { return string(a) }
func (a addr) Network() string { return "tcp" }

// connListener is a net.Listener fed by the proxy with connections it has
// already accepted and decrypted.
type connListener struct {
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

func newConnListener() *connListener {
	return &connListener{ch: make(chan net.Conn), done: make(chan struct{})}
}

func (l *connListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *connListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *connListener) Addr() net.Addr { return addr("mitm") }

// deliver hands c to the server. It reports false when the listener is closed.
func (l *connListener) deliver(c net.Conn) bool {
	select {
	case l.ch <- c:
		return true
	case <-l.done:
		return false
	}
}
