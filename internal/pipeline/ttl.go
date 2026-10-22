package pipeline

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/goastian/midopress/internal/httpx"
)

// TTLConfig bounds how long a result stays in the cache. The cache is addressed
// by the content of the original, so a result never goes stale; what the TTL
// does is keep what the origin says changes often from taking space that
// popular, long lived files could use.
type TTLConfig struct {
	Min     time.Duration // shortest, for responses that must not be reused
	Max     time.Duration // longest
	Default time.Duration // when the origin says nothing
}

// DefaultTTL is what the configuration starts from.
func DefaultTTL() TTLConfig {
	return TTLConfig{Min: time.Minute, Max: 7 * 24 * time.Hour, Default: time.Hour}
}

func (c TTLConfig) clamp(d time.Duration) time.Duration { return min(max(d, c.Min), c.Max) }

// ttlFor derives the lifetime of a cached result from the freshness the origin
// declared: s-maxage, then max-age, then Expires, less the time the response
// already spent in other caches (Age). no-store, no-cache and private, and
// anything that already expired, get the minimum; immutable gets the maximum.
func ttlFor(h http.Header, now time.Time, c TTLConfig) time.Duration {
	cc := h.Get("Cache-Control")
	if httpx.HeaderHasDirective(h, "Cache-Control", "no-store") ||
		httpx.HeaderHasDirective(h, "Cache-Control", "no-cache") ||
		httpx.HeaderHasDirective(h, "Cache-Control", "private") {
		return c.Min
	}
	if httpx.HeaderHasDirective(h, "Cache-Control", "immutable") {
		return c.Max
	}
	age := time.Duration(0)
	if n, err := strconv.Atoi(strings.TrimSpace(h.Get("Age"))); err == nil && n > 0 {
		age = time.Duration(n) * time.Second
	}
	var maxAge, sMaxAge = time.Duration(-1), time.Duration(-1)
	for d := range strings.SplitSeq(strings.ToLower(cc), ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(d), "=")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.Trim(strings.TrimSpace(v), `"`))
		if err != nil || n < 0 {
			continue
		}
		switch k {
		case "max-age":
			maxAge = time.Duration(n) * time.Second
		case "s-maxage":
			sMaxAge = time.Duration(n) * time.Second
		}
	}
	switch {
	case sMaxAge >= 0:
		return c.clamp(sMaxAge - age)
	case maxAge >= 0:
		return c.clamp(maxAge - age)
	}
	if exp := h.Get("Expires"); exp != "" {
		t, err := http.ParseTime(exp)
		if err != nil {
			return c.Min // "0" and "-1" mean already expired
		}
		from := now
		if date, err := http.ParseTime(h.Get("Date")); err == nil {
			from = date
		}
		return c.clamp(t.Sub(from) - age)
	}
	return c.clamp(c.Default)
}
