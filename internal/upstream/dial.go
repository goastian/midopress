package upstream

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"syscall"
	"time"
)

const staggerDelay = 300 * time.Millisecond

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func isPrivate(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() || cgnat.Contains(a)
}

func (c *Client) checkAddr(ap netip.AddrPort) error {
	if c.opts.DenyPrivate && isPrivate(ap.Addr()) {
		return fmt.Errorf("%w: %s is not a public address", ErrBlockedAddress, ap.Addr())
	}
	if c.opts.Deny != nil {
		if err := c.opts.Deny(netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())); err != nil {
			return fmt.Errorf("%w: %w", ErrBlockedAddress, err)
		}
	}
	return nil
}

func (c *Client) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBlockedAddress, err)
	}
	return c.checkAddr(ap)
}

// DialContext connects to addr over TCP, resolving host names through the DNS
// cache and enforcing the configured address guards on every connection attempt.
func (c *Client) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if c.opts.Dial != nil {
		return c.opts.Dial(ctx, network, addr)
	}
	var family string
	switch network {
	case "tcp":
		family = "ip"
	case "tcp4":
		family = "ip4"
	case "tcp6":
		family = "ip6"
	default:
		return nil, fmt.Errorf("upstream: unsupported network %q", network)
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("upstream: invalid port %q", portStr)
	}
	ctx, cancel := context.WithTimeout(ctx, c.opts.DialTimeout)
	defer cancel()

	var addrs []netip.Addr
	if ip, perr := netip.ParseAddr(host); perr == nil {
		addrs = []netip.Addr{ip}
	} else if addrs, err = c.dns.lookup(ctx, family, host); err != nil {
		return nil, &net.OpError{Op: "dial", Net: network, Err: err}
	}
	return c.dialAddrs(ctx, network, interleave(addrs), uint16(port))
}

// interleave alternates address families starting with the family of the first address.
func interleave(addrs []netip.Addr) []netip.Addr {
	if len(addrs) < 3 {
		return addrs
	}
	first := addrs[0].Is6()
	var a, b []netip.Addr
	for _, x := range addrs {
		if x.Is6() == first {
			a = append(a, x)
		} else {
			b = append(b, x)
		}
	}
	if len(b) == 0 {
		return addrs
	}
	out := make([]netip.Addr, 0, len(addrs))
	for i := 0; i < len(a) || i < len(b); i++ {
		if i < len(a) {
			out = append(out, a[i])
		}
		if i < len(b) {
			out = append(out, b[i])
		}
	}
	return out
}

type dialResult struct {
	conn net.Conn
	err  error
}

func (c *Client) dialOne(ctx context.Context, network string, ip netip.Addr, port uint16) (net.Conn, error) {
	return c.dialer.DialContext(ctx, network, netip.AddrPortFrom(ip, port).String())
}

func (c *Client) dialAddrs(ctx context.Context, network string, addrs []netip.Addr, port uint16) (net.Conn, error) {
	if len(addrs) == 1 {
		return c.dialOne(ctx, network, addrs[0], port)
	}
	dctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan dialResult, len(addrs))
	start := func(ip netip.Addr) {
		go func() {
			conn, err := c.dialOne(dctx, network, ip, port)
			results <- dialResult{conn, err}
		}()
	}

	next, pending := 1, 1
	start(addrs[0])
	timer := time.NewTimer(staggerDelay)
	defer timer.Stop()
	var firstErr error
	allBlocked := true
	for {
		select {
		case r := <-results:
			pending--
			if r.err == nil {
				cancel()
				go drain(results, pending)
				return r.conn, nil
			}
			if firstErr == nil {
				firstErr = r.err
			}
			if !errors.Is(r.err, ErrBlockedAddress) {
				allBlocked = false
			}
			if next < len(addrs) {
				start(addrs[next])
				next++
				pending++
				timer.Reset(staggerDelay)
			} else if pending == 0 {
				if allBlocked {
					return nil, firstErr
				}
				return nil, r.err
			}
		case <-timer.C:
			if next < len(addrs) {
				start(addrs[next])
				next++
				pending++
				timer.Reset(staggerDelay)
			}
		case <-ctx.Done():
			cancel()
			go drain(results, pending)
			if firstErr != nil && !errors.Is(firstErr, ErrBlockedAddress) {
				return nil, fmt.Errorf("%w (last error: %w)", ctx.Err(), firstErr)
			}
			return nil, ctx.Err()
		}
	}
}

func drain(results <-chan dialResult, n int) {
	for range n {
		if r := <-results; r.conn != nil {
			_ = r.conn.Close()
		}
	}
}

// DialTLS dials addr and performs a TLS handshake with serverName. ALPN is
// restricted to http/1.1 because the connection is meant for protocol upgrades.
func (c *Client) DialTLS(ctx context.Context, addr, serverName string) (*tls.Conn, error) {
	raw, err := c.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	conn := tls.Client(raw, &tls.Config{
		ServerName: serverName,
		RootCAs:    c.opts.RootCAs,
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
	})
	hctx, cancel := context.WithTimeout(ctx, c.opts.TLSHandshakeTimeout)
	defer cancel()
	if err := conn.HandshakeContext(hctx); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return conn, nil
}
