package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/goastian/midopress/internal/pipeline"
	"github.com/goastian/midopress/internal/quota"
)

type meteredTransportKey struct{}

func (p *Proxy) admit(w http.ResponseWriter, r *http.Request, tunnel bool) (*quota.Lease, bool) {
	if p.opts.Quotas == nil {
		return nil, true
	}
	device := pipeline.ClientFrom(r.Context()).Device
	if !p.opts.Auth.DeviceAllowed(device) {
		p.denied.Add(1)
		p.writeError(w, http.StatusForbidden)
		return nil, false
	}
	var lease *quota.Lease
	var err error
	if tunnel {
		lease, err = p.opts.Quotas.BeginTunnel(device)
	} else {
		lease, err = p.opts.Quotas.Begin(device)
	}
	if err == nil {
		return lease, true
	}
	p.denied.Add(1)
	status, code, retry := http.StatusTooManyRequests, "requests", p.opts.Quotas.RetryAfter()
	switch {
	case errors.Is(err, quota.ErrConcurrency):
		code, retry = "concurrency", time.Second
	case errors.Is(err, quota.ErrBytes):
		code = "bytes"
	case !errors.Is(err, quota.ErrRequests):
		status, code, retry = http.StatusServiceUnavailable, "unavailable", time.Second
	}
	w.Header().Set("X-MidoPress-Quota", code)
	w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retry.Seconds())+1)))
	p.writeError(w, status)
	return nil, false
}

type quotaReader struct {
	io.ReadCloser
	lease   *quota.Lease
	failure atomic.Pointer[quotaReadFailure]
}
type quotaReadFailure struct{ err error }

func requestQuotaError(r *http.Request, err error) error {
	if reader, ok := r.Body.(*quotaReader); ok {
		if failure := reader.failure.Load(); failure != nil {
			return errors.Join(err, failure.err)
		}
	}
	return err
}

func (q *quotaReader) Read(b []byte) (int, error) {
	n, err := q.ReadCloser.Read(b)
	if n > 0 {
		res, e := q.lease.Reserve(n)
		if e != nil {
			q.failure.Store(&quotaReadFailure{e})
			_ = q.ReadCloser.Close()
			return 0, e
		}
		if res.Size < n {
			q.failure.Store(&quotaReadFailure{quota.ErrBytes})
			_ = q.ReadCloser.Close()
			return res.Size, quota.ErrBytes
		}
	}
	return n, err
}

type quotaWriter struct {
	http.ResponseWriter
	lease *quota.Lease
}

func (q *quotaWriter) Unwrap() http.ResponseWriter { return q.ResponseWriter }
func (q *quotaWriter) Write(b []byte) (int, error) {
	res, err := q.lease.Reserve(len(b))
	if err != nil {
		return 0, err
	}
	n, err := q.ResponseWriter.Write(b[:res.Size])
	q.lease.Refund(res, n)
	if err == nil && n < len(b) {
		err = quota.ErrBytes
	}
	return n, err
}

type quotaConn struct {
	net.Conn
	lease   *quota.Lease
	allowed func() bool
}

func (q *quotaConn) Read(b []byte) (int, error) {
	if !q.allowed() {
		_ = q.Close()
		return 0, quota.ErrUnavailable
	}
	n, err := q.Conn.Read(b)
	if n > 0 {
		res, e := q.lease.Reserve(n)
		if e != nil {
			_ = q.Close()
			return 0, e
		}
		if res.Size < n {
			_ = q.Close()
			return res.Size, quota.ErrBytes
		}
	}
	return n, err
}
func (q *quotaConn) Write(b []byte) (int, error) {
	if !q.allowed() {
		_ = q.Close()
		return 0, quota.ErrUnavailable
	}
	res, err := q.lease.Reserve(len(b))
	if err != nil {
		_ = q.Close()
		return 0, err
	}
	n, err := q.Conn.Write(b[:res.Size])
	q.lease.Refund(res, n)
	if err == nil && n < len(b) {
		err = quota.ErrBytes
	}
	return n, err
}
func (q *quotaConn) Close() error { err := q.Conn.Close(); q.lease.Close(); return err }
func (q *quotaConn) CloseWrite() error {
	if c, ok := q.Conn.(interface{ CloseWrite() error }); ok {
		return c.CloseWrite()
	}
	return q.Close()
}

func (p *Proxy) meterConnection(conn net.Conn, lease *quota.Lease, device string) net.Conn {
	if lease == nil {
		return conn
	}
	return &quotaConn{Conn: conn, lease: lease, allowed: func() bool { return p.opts.Auth.DeviceAllowed(device) }}
}
func meteredContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, meteredTransportKey{}, true)
}
func transportMetered(ctx context.Context) bool {
	value, _ := ctx.Value(meteredTransportKey{}).(bool)
	return value
}
