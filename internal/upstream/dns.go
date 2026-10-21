package upstream

import (
	"context"
	"errors"
	"net/netip"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/goastian/midopress/internal/lru"
)

type dnsKey struct {
	network string
	host    string
}

type dnsEntry struct {
	addrs []netip.Addr
	err   error
}

type dnsCache struct {
	resolver    Resolver
	cache       *lru.Cache[dnsKey, dnsEntry]
	flight      singleflight.Group
	ttl         time.Duration
	negativeTTL time.Duration
	lookupLimit time.Duration
	hits        atomic.Uint64
	misses      atomic.Uint64
	rotation    atomic.Uint64
}

func newDNSCache(o Options) *dnsCache {
	d := &dnsCache{
		resolver:    o.Resolver,
		ttl:         o.DNSCacheTTL,
		negativeTTL: o.DNSNegativeTTL,
		lookupLimit: o.DialTimeout,
	}
	if o.DNSCacheTTL > 0 {
		d.cache = lru.New[dnsKey, dnsEntry](o.DNSCacheSize, o.DNSCacheTTL)
	}
	return d
}

// lookup returns the addresses of host rotated by one position per call.
func (d *dnsCache) lookup(ctx context.Context, network, host string) ([]netip.Addr, error) {
	key := dnsKey{network, host}
	if d.cache != nil {
		if e, ok := d.cache.Get(key); ok {
			d.hits.Add(1)
			return d.rotate(e.addrs), e.err
		}
	}
	d.misses.Add(1)
	ch := d.flight.DoChan(network+"\x00"+host, func() (any, error) {
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.lookupLimit)
		defer cancel()
		addrs, err := d.resolver.LookupNetIP(lctx, network, host)
		for i := range addrs {
			addrs[i] = addrs[i].Unmap().WithZone("")
		}
		if err == nil && len(addrs) == 0 {
			err = errors.New("upstream: no addresses found for " + host)
		}
		if d.cache != nil {
			if err != nil {
				if d.negativeTTL > 0 {
					d.cache.AddWithTTL(key, dnsEntry{err: err}, d.negativeTTL)
				}
			} else {
				d.cache.Add(key, dnsEntry{addrs: addrs})
			}
		}
		return addrs, err
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.Err != nil {
			return nil, r.Err
		}
		return d.rotate(r.Val.([]netip.Addr)), nil
	}
}

func (d *dnsCache) rotate(addrs []netip.Addr) []netip.Addr {
	if len(addrs) < 2 {
		return addrs
	}
	off := int((d.rotation.Add(1) - 1) % uint64(len(addrs)))
	out := make([]netip.Addr, 0, len(addrs))
	out = append(out, addrs[off:]...)
	return append(out, addrs[:off]...)
}
