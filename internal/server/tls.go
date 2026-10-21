package server

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const maxPendingHandshakes = 2048

// tlsListener accepts TCP connections and completes their TLS handshake in
// the background, within a deadline, before handing them to the HTTP server.
// The server's own handshake has no deadline unless read and write timeouts
// are set, which would also cut long downloads.
type tlsListener struct {
	net.Listener
	cfg     *tls.Config
	timeout time.Duration

	ch      chan net.Conn
	done    chan struct{}
	once    sync.Once
	pending chan struct{}
}

func newTLSListener(inner net.Listener, cfg *tls.Config, timeout time.Duration) *tlsListener {
	l := &tlsListener{
		Listener: inner,
		cfg:      cfg,
		timeout:  timeout,
		ch:       make(chan net.Conn),
		done:     make(chan struct{}),
		pending:  make(chan struct{}, maxPendingHandshakes),
	}
	go l.loop()
	return l
}

func (l *tlsListener) loop() {
	var delay time.Duration
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			select {
			case <-l.done:
				return
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				delay = min(max(delay*2, 5*time.Millisecond), time.Second)
				time.Sleep(delay)
				continue
			}
			return
		}
		delay = 0
		select {
		case l.pending <- struct{}{}:
			go l.handshake(c)
		default:
			_ = c.Close()
		}
	}
}

func (l *tlsListener) handshake(c net.Conn) {
	defer func() { <-l.pending }()
	tc := tls.Server(c, l.cfg)
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	if err := tc.HandshakeContext(ctx); err != nil {
		_ = c.Close()
		return
	}
	select {
	case l.ch <- tc:
	case <-l.done:
		_ = tc.Close()
	}
}

func (l *tlsListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *tlsListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}

// certReloader serves a certificate from disk and picks up a renewed one,
// either when told to or when the files change.
type certReloader struct {
	certFile, keyFile string

	mu      sync.Mutex
	cert    atomic.Pointer[tls.Certificate]
	mtime   time.Time
	checked atomic.Int64
}

const certCheckInterval = time.Minute

func newCertReloader(certFile, keyFile string) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile}
	if err := r.reload(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *certReloader) reload() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return err
	}
	r.cert.Store(&cert)
	r.mtime = r.newest()
	return nil
}

func (r *certReloader) newest() time.Time {
	var t time.Time
	for _, f := range []string{r.certFile, r.keyFile} {
		if fi, err := os.Stat(f); err == nil && fi.ModTime().After(t) {
			t = fi.ModTime()
		}
	}
	return t
}

// get returns the current certificate, checking the files at most once a
// minute. A renewal that does not load keeps the old certificate.
func (r *certReloader) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	now := time.Now().UnixNano()
	last := r.checked.Load()
	if now-last > int64(certCheckInterval) && r.checked.CompareAndSwap(last, now) {
		r.mu.Lock()
		changed := r.newest().After(r.mtime)
		r.mu.Unlock()
		if changed {
			_ = r.reload()
		}
	}
	c := r.cert.Load()
	if c == nil {
		return nil, errors.New("server: no certificate loaded")
	}
	return c, nil
}
