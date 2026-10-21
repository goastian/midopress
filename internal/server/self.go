package server

import (
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"github.com/goastian/midopress/internal/proxy"
)

// selfAddrs knows the addresses and names under which the proxy can be
// reached, so it can tell requests meant for itself from requests to forward
// and never connect to itself.
type selfAddrs struct {
	port  string
	ips   map[netip.Addr]struct{}
	names map[string]struct{}
}

func newSelfAddrs(listenAddr net.Addr) *selfAddrs {
	s := &selfAddrs{
		ips:   map[netip.Addr]struct{}{},
		names: map[string]struct{}{"localhost": {}},
	}
	if ta, ok := listenAddr.(*net.TCPAddr); ok {
		s.port = strconv.Itoa(ta.Port)
		if ip, ok := netip.AddrFromSlice(ta.IP); ok && !ip.IsUnspecified() {
			s.ips[ip.Unmap()] = struct{}{}
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(ipn.IP); ok {
					s.ips[ip.Unmap()] = struct{}{}
				}
			}
		}
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		h = strings.ToLower(h)
		s.names[h] = struct{}{}
		if short, _, ok := strings.Cut(h, "."); ok {
			s.names[short] = struct{}{}
		}
	}
	return s
}

// isLocal reports whether hostport names the proxy.
func (s *selfAddrs) isLocal(hostport string) bool {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = hostport, "80"
	}
	if port != s.port {
		return false
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if _, ok := s.names[host]; ok {
		return true
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		if ip.IsLoopback() {
			return true
		}
		_, ok := s.ips[ip]
		return ok
	}
	return false
}

// deny refuses to connect to the proxy's own listener.
func (s *selfAddrs) deny(ap netip.AddrPort) error {
	if strconv.Itoa(int(ap.Port())) != s.port {
		return nil
	}
	ip := ap.Addr().Unmap()
	if ip.IsLoopback() || ip.IsUnspecified() {
		return proxy.ErrLoop
	}
	if _, ok := s.ips[ip]; ok {
		return proxy.ErrLoop
	}
	return nil
}
