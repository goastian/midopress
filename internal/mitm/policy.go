package mitm

import (
	"errors"
	"net"
	"time"

	"github.com/goastian/midopress/internal/lru"
	"github.com/goastian/midopress/internal/rules"
)

const (
	defaultAutoTTL        = 6 * time.Hour
	defaultAutoMaxEntries = 100000
)

// PolicyOptions configures a Policy. Zero values select the defaults.
type PolicyOptions struct {
	Patterns       []string
	UseDefaults    bool
	AutoTTL        time.Duration
	AutoMaxEntries int
}

// Policy decides which hosts are tunnelled instead of intercepted. It is safe
// for concurrent use.
type Policy struct {
	static *rules.HostMatcher
	auto   *lru.Cache[string, struct{}]
}

// NewPolicy builds a Policy, returning an error listing invalid patterns.
func NewPolicy(opts PolicyOptions) (*Policy, error) {
	patterns := opts.Patterns
	if opts.UseDefaults {
		patterns = append(DefaultPassthroughPatterns(), patterns...)
	}
	m, err := rules.NewHostMatcher(patterns)
	if err != nil {
		return nil, err
	}
	if opts.AutoTTL <= 0 {
		opts.AutoTTL = defaultAutoTTL
	}
	if opts.AutoMaxEntries <= 0 {
		opts.AutoMaxEntries = defaultAutoMaxEntries
	}
	return &Policy{static: m, auto: lru.New[string, struct{}](opts.AutoMaxEntries, opts.AutoTTL)}, nil
}

// Passthrough reports whether host (optionally with a port) must be tunnelled.
func (p *Policy) Passthrough(host string) bool {
	h := rules.Normalize(host)
	if p.static.Match(h) {
		return true
	}
	_, ok := p.auto.Get(h)
	return ok
}

// MarkFailed makes host passthrough for the auto TTL, typically after a
// client rejected the forged certificate.
func (p *Policy) MarkFailed(host string) {
	h := rules.Normalize(host)
	if h == "" {
		return
	}
	p.auto.Add(h, struct{}{})
}

// AutoCount returns the number of auto-passthrough marks currently stored.
func (p *Policy) AutoCount() int { return p.auto.Len() }

// DefaultPassthroughPatterns returns a conservative list of sensitive or
// pinned hosts that are tunnelled by default: banking words, government and
// military domains, health words, PayPal, password managers and Mozilla
// services. Patterns use the rules.NewHostMatcher syntax.
func DefaultPassthroughPatterns() []string {
	return []string{
		`~d (^|[.-])(bank|banco|banca|banque|sparkasse)`,
		`~d \.gov(\.[a-z]{2})?$`,
		`~d \.mil$`,
		`~d (^|[.-])(health|salud|clinic|hospital)([.-]|$)`,
		"paypal.com",
		"*.paypal.com",
		"*.1password.com",
		"*.bitwarden.com",
		"*.lastpass.com",
		"*.mozilla.org",
		"*.mozilla.com",
		"*.mozilla.net",
		"*.firefox.com",
	}
}

var untrustedAlerts = map[string]struct{}{
	"tls: bad certificate":               {},
	"tls: unsupported certificate":       {},
	"tls: revoked certificate":           {},
	"tls: expired certificate":           {},
	"tls: unknown certificate":           {},
	"tls: unknown certificate authority": {},
}

// IsUntrustedCertAlert reports whether err is a TLS alert received from the
// peer meaning it rejected our certificate.
func IsUntrustedCertAlert(err error) bool {
	var opErr *net.OpError
	if !errors.As(err, &opErr) || opErr.Op != "remote error" || opErr.Err == nil {
		return false
	}
	_, ok := untrustedAlerts[opErr.Err.Error()]
	return ok
}
