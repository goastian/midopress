// Package rules implements fast host pattern matching.
package rules

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const regexPrefix = "~d"

// HostMatcher matches hosts against a set of patterns. It is immutable and
// safe for concurrent use.
type HostMatcher struct {
	exact     map[string]struct{}
	wildcards map[string]struct{}
	regexes   []*regexp.Regexp
}

// NewHostMatcher compiles patterns. Supported forms:
//
//	~d <regex>     unanchored RE2 regex tested against the normalized host
//	*.example.com  any subdomain of example.com, but not example.com itself
//	.example.com   example.com and any subdomain
//	example.com    exactly example.com
//
// The returned error lists every invalid pattern.
func NewHostMatcher(patterns []string) (*HostMatcher, error) {
	m := &HostMatcher{
		exact:     make(map[string]struct{}),
		wildcards: make(map[string]struct{}),
	}
	var errs []error
	for _, raw := range patterns {
		p := strings.TrimSpace(raw)
		if p == "" {
			errs = append(errs, errors.New("empty pattern"))
			continue
		}
		if err := m.add(p); err != nil {
			errs = append(errs, fmt.Errorf("pattern %q: %w", raw, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *HostMatcher) add(p string) error {
	if rest, ok := strings.CutPrefix(p, regexPrefix); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
		expr := strings.TrimSpace(rest)
		if expr == "" {
			return errors.New("empty regex")
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return err
		}
		m.regexes = append(m.regexes, re)
		return nil
	}

	lower := strings.ToLower(p)
	switch {
	case strings.HasPrefix(lower, "*."):
		base := strings.TrimSuffix(lower[2:], ".")
		if err := validateName(base); err != nil {
			return err
		}
		m.wildcards[base] = struct{}{}
	case strings.HasPrefix(lower, "."):
		base := strings.TrimSuffix(lower[1:], ".")
		if err := validateName(base); err != nil {
			return err
		}
		m.exact[base] = struct{}{}
		m.wildcards[base] = struct{}{}
	default:
		name := normalize(lower)
		if err := validateName(name); err != nil {
			return err
		}
		m.exact[name] = struct{}{}
	}
	return nil
}

func validateName(name string) error {
	if name == "" {
		return errors.New("empty host name")
	}
	if strings.ContainsAny(name, "*~ \t/") {
		return errors.New("invalid character in host name")
	}
	return nil
}

// Empty reports whether the matcher has no patterns.
func (m *HostMatcher) Empty() bool {
	return m == nil || (len(m.exact) == 0 && len(m.wildcards) == 0 && len(m.regexes) == 0)
}

// Match reports whether host matches any pattern. A nil matcher matches
// nothing.
func (m *HostMatcher) Match(host string) bool {
	if m == nil {
		return false
	}
	h := normalize(host)
	if h == "" {
		return false
	}
	if _, ok := m.exact[h]; ok {
		return true
	}
	if len(m.wildcards) > 0 && !isIPLiteral(h) {
		for rest := h; ; {
			i := strings.IndexByte(rest, '.')
			if i < 0 {
				break
			}
			rest = rest[i+1:]
			if _, ok := m.wildcards[rest]; ok {
				return true
			}
		}
	}
	for _, re := range m.regexes {
		if re.MatchString(h) {
			return true
		}
	}
	return false
}

// Normalize lowercases host, strips the port, IPv6 brackets and a trailing
// dot. It does not allocate when host is already normalized.
func Normalize(host string) string { return normalize(host) }

func normalize(host string) string {
	if len(host) > 0 && host[0] == '[' {
		if end := strings.IndexByte(host, ']'); end > 0 {
			host = host[1:end]
		}
	} else if i := strings.IndexByte(host, ':'); i >= 0 && strings.IndexByte(host[i+1:], ':') < 0 {
		host = host[:i]
	}
	host = strings.TrimSuffix(host, ".")
	for i := 0; i < len(host); i++ {
		if c := host[i]; c >= 'A' && c <= 'Z' {
			return strings.ToLower(host)
		}
	}
	return host
}

func isIPLiteral(h string) bool {
	if strings.IndexByte(h, ':') >= 0 {
		return true
	}
	last := h
	if i := strings.LastIndexByte(h, '.'); i >= 0 {
		last = h[i+1:]
	}
	if last == "" {
		return false
	}
	for i := 0; i < len(last); i++ {
		if last[i] < '0' || last[i] > '9' {
			return false
		}
	}
	return true
}
