package rules

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustMatcher(t testing.TB, patterns ...string) *HostMatcher {
	t.Helper()
	m, err := NewHostMatcher(patterns)
	require.NoError(t, err)
	return m
}

func TestExactPattern(t *testing.T) {
	m := mustMatcher(t, "example.com")
	assert.True(t, m.Match("example.com"))
	assert.False(t, m.Match("www.example.com"))
	assert.False(t, m.Match("notexample.com"))
	assert.False(t, m.Match("example.com.evil.org"))
}

func TestWildcardPattern(t *testing.T) {
	m := mustMatcher(t, "*.example.com")
	assert.True(t, m.Match("www.example.com"))
	assert.True(t, m.Match("a.b.c.example.com"))
	assert.False(t, m.Match("example.com"))
	assert.False(t, m.Match("badexample.com"))
	assert.False(t, m.Match("example.com.evil.org"))
}

func TestLeadingDotPattern(t *testing.T) {
	m := mustMatcher(t, ".example.com")
	assert.True(t, m.Match("example.com"))
	assert.True(t, m.Match("a.b.example.com"))
	assert.False(t, m.Match("badexample.com"))
}

func TestRegexPattern(t *testing.T) {
	m := mustMatcher(t, `~d \.bank$`, "~d   ^api[0-9]+\\.")
	assert.True(t, m.Match("my.bank"))
	assert.True(t, m.Match("api12.example.org"))
	assert.False(t, m.Match("bank.example.org"))
	assert.False(t, m.Match("my.bank.example"))
}

func TestRegexIsUnanchored(t *testing.T) {
	m := mustMatcher(t, "~d bank")
	assert.True(t, m.Match("mybank.example"))
	assert.False(t, m.Match("example.org"))
}

func TestNormalization(t *testing.T) {
	m := mustMatcher(t, "example.com", "*.wild.org", "  Spaced.Com  ")
	assert.True(t, m.Match("EXAMPLE.com"))
	assert.True(t, m.Match("example.com:443"))
	assert.True(t, m.Match("example.com."))
	assert.True(t, m.Match("Example.COM.:8080"))
	assert.True(t, m.Match("A.Wild.Org:443"))
	assert.True(t, m.Match("spaced.com"))
	assert.False(t, m.Match(""))
	assert.False(t, m.Match(":443"))
}

func TestIPLiterals(t *testing.T) {
	m := mustMatcher(t, "192.168.0.1", "::1", "*.0.1", "~d ^10\\.")
	assert.True(t, m.Match("192.168.0.1"))
	assert.True(t, m.Match("192.168.0.1:80"))
	assert.False(t, m.Match("192.168.0.2"))
	assert.True(t, m.Match("[::1]:443"))
	assert.True(t, m.Match("[::1]"))
	assert.True(t, m.Match("::1"))
	assert.True(t, m.Match("10.1.2.3"))
	assert.False(t, m.Match("168.0.1"))
	assert.False(t, m.Match("1.2.0.1"))
}

func TestIPv6RegexSeesNormalizedHost(t *testing.T) {
	m := mustMatcher(t, `~d ^fe80:`)
	assert.True(t, m.Match("[fe80::1]:443"))
}

func TestErrors(t *testing.T) {
	m, err := NewHostMatcher([]string{"ok.com", "", "~d (", "*.", "~d", "a b.com"})
	require.Error(t, err)
	assert.Nil(t, m)
	msg := err.Error()
	assert.Contains(t, msg, "empty pattern")
	assert.Contains(t, msg, `"~d ("`)
	assert.Contains(t, msg, `"*."`)
	assert.Contains(t, msg, `"~d"`)
	assert.Contains(t, msg, `"a b.com"`)
	assert.NotContains(t, msg, "ok.com")
}

func TestNilAndEmpty(t *testing.T) {
	var nilM *HostMatcher
	assert.False(t, nilM.Match("example.com"))
	assert.True(t, nilM.Empty())

	m := mustMatcher(t)
	assert.True(t, m.Empty())
	assert.False(t, m.Match("example.com"))

	assert.False(t, mustMatcher(t, "a.com").Empty())
	assert.False(t, mustMatcher(t, "~d x").Empty())
}

func TestMatchDoesNotAllocate(t *testing.T) {
	m := mustMatcher(t, "example.com", "*.wild.org")
	allocs := testing.AllocsPerRun(100, func() {
		m.Match("a.b.wild.org")
		m.Match("example.com:443")
		m.Match("other.net")
	})
	assert.Zero(t, allocs)
}

func BenchmarkMatch(b *testing.B) {
	var patterns []string
	for i := range 20 {
		patterns = append(patterns, fmt.Sprintf("host%d.example.com", i))
	}
	for i := range 20 {
		patterns = append(patterns, fmt.Sprintf("*.wild%d.org", i))
	}
	patterns = append(patterns,
		`~d (^|[.-])(bank|banco|banca|banque|sparkasse)`,
		`~d \.gov(\.[a-z]{2})?$`,
		`~d \.mil$`,
		`~d (^|[.-])(health|salud|clinic|hospital)([.-]|$)`,
	)
	for i := range 6 {
		patterns = append(patterns, fmt.Sprintf(".dot%d.net", i))
	}
	m := mustMatcher(b, patterns...)
	hosts := []string{
		"host3.example.com",
		"cdn.static.wild7.org:443",
		"www.unrelated-site.io",
		"images.cdn.example.net",
		"api.dot2.net",
		"mybank.example.org",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		m.Match(hosts[i%len(hosts)])
	}
}
