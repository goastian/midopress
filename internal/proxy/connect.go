package proxy

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/goastian/midopress/internal/mitm"
	"github.com/goastian/midopress/internal/pipeline"
	"github.com/goastian/midopress/internal/rules"
)

const tlsHandshakeRecord = 0x16

// splitTarget splits a CONNECT authority, defaulting to the HTTPS port.
func splitTarget(authority string) (host, port string) {
	h, p, err := net.SplitHostPort(authority)
	if err != nil {
		return authority, "443"
	}
	return h, p
}

func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	if p.closing.Load() {
		p.writeError(w, http.StatusServiceUnavailable)
		return
	}
	host, port := splitTarget(r.Host)
	if host == "" {
		p.writeError(w, http.StatusBadRequest)
		return
	}
	target := net.JoinHostPort(host, port)
	cl := pipeline.ClientFrom(r.Context())
	intercept := p.opts.Issuer != nil && !p.opts.Policy.Passthrough(host)
	if intercept {
		switch p.opts.Pipeline.HostDecision(host, cl.Addr).Action {
		case rules.ActionPassthrough, rules.ActionPassthroughTLS:
			intercept = false
		}
	}

	// A plain tunnel connects first, so that an unreachable host is reported to
	// the client as an error status. An intercepted one connects lazily, once
	// the first request shows what the client wants.
	var up net.Conn
	if !intercept {
		c, err := p.opts.Upstream.DialContext(r.Context(), "tcp", target)
		if err != nil {
			p.upstreamError(w, r, err)
			return
		}
		up = c
	}

	client, err := p.takeover(w, r)
	if err != nil {
		if up != nil {
			_ = up.Close()
		}
		return
	}
	if up != nil {
		p.tunnels.tunnel(client, up)
		return
	}
	p.intercept(client, host, target, cl.Device)
	if sc, ok := client.(*streamConn); ok {
		// An HTTP/2 stream ends when its handler returns, so wait for the
		// intercepted connection to be done with it.
		<-sc.closed
	}
}

// takeover answers the CONNECT request and returns the client side of the
// tunnel as a connection.
func (p *Proxy) takeover(w http.ResponseWriter, r *http.Request) (net.Conn, error) {
	if hj, ok := w.(http.Hijacker); ok {
		conn, brw, err := hj.Hijack()
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			_ = conn.Close()
			return nil, err
		}
		if brw.Reader.Buffered() > 0 {
			return &prefixConn{Conn: conn, r: brw.Reader}, nil
		}
		return conn, nil
	}
	w.WriteHeader(http.StatusOK)
	if err := http.NewResponseController(w).Flush(); err != nil {
		return nil, err
	}
	return newStreamConn(w, r.Body, r.RemoteAddr), nil
}

// intercept decrypts the client side of the tunnel and hands the connection to
// the HTTP server that serves the requests found inside it.
func (p *Proxy) intercept(client net.Conn, host, target, device string) {
	first, conn, err := p.sniff(client)
	if err != nil {
		_ = client.Close()
		return
	}
	if first != tlsHandshakeRecord {
		// Not TLS (plain HTTP over CONNECT, SSH, ...): relay it untouched.
		up, err := p.opts.Upstream.DialContext(context.Background(), "tcp", target)
		if err != nil {
			_ = conn.Close()
			return
		}
		p.tunnels.tunnel(conn, up)
		return
	}

	tc := tls.Server(&hostConn{Conn: conn, host: host, device: device}, p.tlsConf)
	ctx, cancel := context.WithTimeout(context.Background(), p.opts.HandshakeTimeout)
	err = tc.HandshakeContext(ctx)
	cancel()
	p.handshakes.Add(1)
	if err != nil {
		p.handshakeErr.Add(1)
		// A client that rejects our certificate is usually pinning the real one,
		// or does not trust our CA. Tunnel that host untouched from now on.
		if mitm.IsUntrustedCertAlert(err) {
			p.opts.Policy.MarkFailed(host)
			p.autoMarked.Add(1)
			if p.log.Enabled(context.Background(), slog.LevelDebug) {
				if p.opts.LogURLs {
					p.log.Debug("client rejected the certificate, host is now passed through", "host", host)
				} else {
					p.log.Debug("client rejected the certificate, host is now passed through")
				}
			}
		}
		_ = conn.Close()
		return
	}
	if !p.ml.deliver(tc) {
		_ = tc.Close()
	}
}

// sniff reads the first byte of the client stream to tell TLS from anything
// else, and returns a connection that still delivers that byte.
func (p *Proxy) sniff(c net.Conn) (byte, net.Conn, error) {
	t := time.AfterFunc(p.opts.HandshakeTimeout, func() { _ = c.Close() })
	defer t.Stop()
	var b [1]byte
	if _, err := io.ReadFull(c, b[:]); err != nil {
		return 0, nil, err
	}
	return b[0], &peekedConn{Conn: c, first: b[0]}, nil
}
