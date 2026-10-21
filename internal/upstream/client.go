package upstream

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

const bufferSize = 32 << 10

// Client owns the shared outbound transport, DNS cache and dialer. It is safe for concurrent use.
type Client struct {
	opts      Options
	dns       *dnsCache
	dialer    *net.Dialer
	transport *http.Transport
}

// New builds a Client; zero fields of opts are replaced by their defaults.
func New(opts Options) *Client {
	opts.applyDefaults()
	c := &Client{opts: opts, dns: newDNSCache(opts)}
	c.dialer = &net.Dialer{KeepAlive: opts.KeepAlive, Control: c.control}
	c.transport = &http.Transport{
		Proxy:                 nil,
		DialContext:           c.DialContext,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{RootCAs: opts.RootCAs, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   opts.TLSHandshakeTimeout,
		ResponseHeaderTimeout: opts.ResponseHeaderTimeout,
		IdleConnTimeout:       opts.IdleConnTimeout,
		ExpectContinueTimeout: opts.ExpectContinueTimeout,
		MaxIdleConns:          opts.MaxIdleConns,
		MaxIdleConnsPerHost:   opts.MaxIdleConnsPerHost,
		MaxConnsPerHost:       opts.MaxConnsPerHost,
		ReadBufferSize:        bufferSize,
		WriteBufferSize:       bufferSize,
		HTTP2: &http.HTTP2Config{
			SendPingTimeout: 30 * time.Second,
			PingTimeout:     15 * time.Second,
		},
	}
	return c
}

// RoundTrip implements http.RoundTripper. Redirects are never followed.
func (c *Client) RoundTrip(req *http.Request) (*http.Response, error) {
	return c.transport.RoundTrip(req)
}

// Transport returns the underlying shared transport.
func (c *Client) Transport() *http.Transport { return c.transport }

// CloseIdleConnections closes idle pooled connections.
func (c *Client) CloseIdleConnections() { c.transport.CloseIdleConnections() }

// DNSStats returns the number of DNS cache hits and misses.
func (c *Client) DNSStats() (hits, misses uint64) {
	return c.dns.hits.Load(), c.dns.misses.Load()
}
