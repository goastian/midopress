// Package upstream implements the outbound side of the proxy: a shared
// http.Transport, a DNS cache and a guarded dialer.
package upstream

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/netip"
	"time"
)

// ErrBlockedAddress is returned (wrapped) when a destination is refused by DenyPrivate or Deny.
var ErrBlockedAddress = errors.New("upstream: destination address is blocked")

// Resolver resolves host names to IP addresses. *net.Resolver implements it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Options configures a Client. Zero fields are replaced by the defaults of DefaultOptions.
type Options struct {
	// DialTimeout is the total budget to establish a TCP connection.
	DialTimeout           time.Duration
	KeepAlive             time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
	IdleConnTimeout       time.Duration
	ExpectContinueTimeout time.Duration
	MaxIdleConns          int
	MaxIdleConnsPerHost   int
	// MaxConnsPerHost of 0 means unlimited.
	MaxConnsPerHost int
	// DNSCacheTTL is the lifetime of successful lookups; a negative value disables the cache.
	DNSCacheTTL time.Duration
	// DNSNegativeTTL is the lifetime of failed lookups.
	DNSNegativeTTL time.Duration
	DNSCacheSize   int
	// DenyPrivate refuses loopback, private, link-local, CGNAT, unspecified and multicast destinations.
	DenyPrivate bool
	// Deny is an optional extra guard called with every address right before connecting.
	// A non-nil error refuses the address and is returned wrapped with ErrBlockedAddress.
	Deny func(addr netip.AddrPort) error
	// RootCAs are the upstream TLS roots; nil means the system roots.
	RootCAs *x509.CertPool
	// Resolver defaults to net.DefaultResolver.
	Resolver Resolver
	// Dial replaces the TCP connect step entirely and is a test hook. When set, the
	// address is passed through unchanged: DNS resolution, the DNS cache, DenyPrivate
	// and Deny are all bypassed.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// DefaultOptions returns Options with every default filled in.
func DefaultOptions() Options {
	return Options{
		DialTimeout:           10 * time.Second,
		KeepAlive:             30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
		MaxIdleConns:          2048,
		MaxIdleConnsPerHost:   64,
		DNSCacheTTL:           30 * time.Second,
		DNSNegativeTTL:        3 * time.Second,
		DNSCacheSize:          10000,
	}
}

func (o *Options) applyDefaults() {
	d := DefaultOptions()
	if o.DialTimeout == 0 {
		o.DialTimeout = d.DialTimeout
	}
	if o.KeepAlive == 0 {
		o.KeepAlive = d.KeepAlive
	}
	if o.TLSHandshakeTimeout == 0 {
		o.TLSHandshakeTimeout = d.TLSHandshakeTimeout
	}
	if o.ResponseHeaderTimeout == 0 {
		o.ResponseHeaderTimeout = d.ResponseHeaderTimeout
	}
	if o.IdleConnTimeout == 0 {
		o.IdleConnTimeout = d.IdleConnTimeout
	}
	if o.ExpectContinueTimeout == 0 {
		o.ExpectContinueTimeout = d.ExpectContinueTimeout
	}
	if o.MaxIdleConns == 0 {
		o.MaxIdleConns = d.MaxIdleConns
	}
	if o.MaxIdleConnsPerHost == 0 {
		o.MaxIdleConnsPerHost = d.MaxIdleConnsPerHost
	}
	if o.DNSCacheTTL == 0 {
		o.DNSCacheTTL = d.DNSCacheTTL
	}
	if o.DNSNegativeTTL == 0 {
		o.DNSNegativeTTL = d.DNSNegativeTTL
	}
	if o.DNSCacheSize == 0 {
		o.DNSCacheSize = d.DNSCacheSize
	}
	if o.Resolver == nil {
		o.Resolver = net.DefaultResolver
	}
}
