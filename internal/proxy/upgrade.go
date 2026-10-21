package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/goastian/midopress/internal/httpx"
)

const upgradeHandshakeTimeout = 30 * time.Second

// upgrade relays a request that asks to switch protocols, typically a
// WebSocket handshake, and then the bytes of the new protocol.
func (p *Proxy) upgrade(w http.ResponseWriter, r *http.Request) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		p.writeError(w, http.StatusNotImplemented)
		return
	}
	if p.closing.Load() {
		p.writeError(w, http.StatusServiceUnavailable)
		return
	}

	host, port := r.URL.Hostname(), r.URL.Port()
	if port == "" {
		port = "80"
		if r.URL.Scheme == "https" {
			port = "443"
		}
	}
	target := net.JoinHostPort(host, port)

	var up net.Conn
	var err error
	if r.URL.Scheme == "https" {
		up, err = p.opts.Upstream.DialTLS(r.Context(), target, host)
	} else {
		up, err = p.opts.Upstream.DialContext(r.Context(), "tcp", target)
	}
	if err != nil {
		p.upstreamError(w, r, err)
		return
	}

	r.Header.Del("Proxy-Authorization")
	r.Header.Del("Proxy-Connection")
	if r.Header.Get("User-Agent") == "" {
		r.Header["User-Agent"] = []string{""}
	}
	r.RequestURI = ""

	_ = up.SetDeadline(time.Now().Add(upgradeHandshakeTimeout))
	if err := r.Write(up); err != nil {
		_ = up.Close()
		p.upstreamError(w, r, err)
		return
	}
	br := bufio.NewReader(up)
	resp, err := http.ReadResponse(br, r)
	if err != nil {
		_ = up.Close()
		p.upstreamError(w, r, err)
		return
	}
	_ = up.SetDeadline(time.Time{})

	if resp.StatusCode != http.StatusSwitchingProtocols {
		// The origin refused the upgrade, so this is an ordinary response.
		defer func() { _ = up.Close() }()
		defer func() { _ = resp.Body.Close() }()
		httpx.RemoveHopByHop(resp.Header)
		httpx.CopyHeader(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}

	client, brw, err := hj.Hijack()
	if err != nil {
		_ = up.Close()
		return
	}
	if _, err := fmt.Fprintf(client, "HTTP/1.1 101 Switching Protocols\r\n"); err != nil {
		_ = client.Close()
		_ = up.Close()
		return
	}
	if err := resp.Header.Write(client); err != nil {
		_ = client.Close()
		_ = up.Close()
		return
	}
	if _, err := io.WriteString(client, "\r\n"); err != nil {
		_ = client.Close()
		_ = up.Close()
		return
	}

	clientConn := client
	if brw.Reader.Buffered() > 0 {
		clientConn = &prefixConn{Conn: client, r: brw.Reader}
	}
	upConn := up
	if br.Buffered() > 0 {
		upConn = &prefixConn{Conn: up, r: br}
	}
	p.tunnels.tunnel(clientConn, upConn)
}
