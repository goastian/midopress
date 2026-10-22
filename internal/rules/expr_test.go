package rules

import (
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustParse(t testing.TB, s string, opts ...ParseOption) *Expr {
	t.Helper()
	e, err := Parse(s, opts...)
	require.NoError(t, err)
	return e
}

func reqCtx() *Context {
	return &Context{
		Method:    "POST",
		URL:       "https://www.Example.com/video/a.mp4?x=1",
		Host:      "www.example.com",
		ReqHeader: http.Header{"User-Agent": {"curl/8"}, "X-Token": {"abc"}},
		ReqBody:   []byte(`{"user":"alice"}`),
		Src:       netip.MustParseAddr("10.1.2.3"),
		Dst:       netip.MustParseAddr("93.184.216.34"),
	}
}

func respCtx() *Context {
	c := reqCtx()
	c.HasResponse = true
	c.Status = 404
	c.ContentType = "text/html"
	c.RespHeader = http.Header{"Server": {"nginx"}, "Set-Cookie": {"a=1", "b=2"}}
	c.RespBody = []byte("<html>not found</html>")
	return c
}

func TestAtoms(t *testing.T) {
	tests := []struct {
		expr string
		req  bool
		resp bool
	}{
		{`~d example\.com$`, true, true},
		{`~d ^example`, false, false},
		{`~d ^WWW\.`, true, true},
		{`~u mp4\?x=1$`, true, true},
		{`~u Example`, true, true},
		{`~u example`, false, false},
		{`~t html`, false, true},
		{`~t ^TEXT/HTML$`, false, true},
		{`~t json`, false, false},
		{`~c 404`, false, true},
		{`~c 4xx`, false, true},
		{`~c 2xx`, false, false},
		{`~c 403`, false, false},
		{`~m post`, true, true},
		{`~m ^POST$`, true, true},
		{`~m ^GET$`, false, false},
		{`~h "User-Agent: curl"`, true, true},
		{`~h "^Server: nginx$"`, false, true},
		{`~h "Set-Cookie: b=2"`, false, true},
		{`~h "user-agent"`, false, false},
		{`~hq "X-Token: a"`, true, true},
		{`~hq nginx`, false, false},
		{`~hs nginx`, false, true},
		{`~hs curl`, false, false},
		{`~q`, true, false},
		{`~s`, false, true},
		{`~b alice`, true, true},
		{`~b "not found"`, false, true},
		{`~bq alice`, true, true},
		{`~bq "not found"`, false, false},
		{`~bs "not found"`, false, true},
		{`~bs alice`, false, false},
		{`~src 10.0.0.0/8`, true, true},
		{`~src 10.1.2.3`, true, true},
		{`~src 11.0.0.0/8`, false, false},
		{`~dst 93.184.216.0/24`, true, true},
		{`~dst 10.0.0.0/8`, false, false},
		{`~all`, true, true},
		{`~none`, false, false},
		{`!~none`, true, true},
		{`~s & ~c 404 & ~t html`, false, true},
		{`~q | ~c 5xx`, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			e := mustParse(t, tt.expr)
			assert.Equal(t, tt.req, e.Match(reqCtx()), "request phase")
			assert.Equal(t, tt.resp, e.Match(respCtx()), "response phase")
		})
	}
}

func TestStatusClasses(t *testing.T) {
	for status, class := range map[int]string{100: "1xx", 200: "2xx", 301: "3xx", 404: "4xx", 503: "5xx"} {
		c := &Context{HasResponse: true, Status: status}
		assert.True(t, mustParse(t, "~c "+class).Match(c), class)
		for _, other := range []string{"1xx", "2xx", "3xx", "4xx", "5xx"} {
			if other != class {
				assert.False(t, mustParse(t, "~c "+other).Match(c), "%d vs %s", status, other)
			}
		}
	}
}

func TestHostNormalization(t *testing.T) {
	e := mustParse(t, `~d ^example\.com$`)
	assert.True(t, e.Match(&Context{Host: "Example.COM:443"}))
	assert.True(t, e.Match(&Context{Host: "example.com."}))
	assert.False(t, e.Match(&Context{Host: ""}))
}

func TestAddressEdgeCases(t *testing.T) {
	e := mustParse(t, `~src 1.2.3.0/24`)
	assert.False(t, e.Match(&Context{}), "invalid address")
	assert.True(t, e.Match(&Context{Src: netip.MustParseAddr("::ffff:1.2.3.4")}), "v4-mapped")
	assert.False(t, e.Match(&Context{Src: netip.MustParseAddr("2001:db8::1")}))
	e6 := mustParse(t, `~dst fe80::/10`)
	assert.True(t, e6.Match(&Context{Dst: netip.MustParseAddr("fe80::1%eth0")}), "zone ignored")
}

func TestBodyRules(t *testing.T) {
	c := &Context{HasResponse: true}
	c.RespBody = []byte("hello secret")
	assert.True(t, mustParse(t, `~bs secret`).Match(c))

	c.RespBody = []byte("hello\x00secret")
	assert.False(t, mustParse(t, `~bs secret`).Match(c), "NUL means binary")

	c.RespBody = []byte("secret \xff\xfe")
	assert.False(t, mustParse(t, `~bs secret`).Match(c), "invalid UTF-8")

	c.RespBody = nil
	assert.False(t, mustParse(t, `~bs .*`).Match(c), "empty body")
	assert.False(t, mustParse(t, `~b ^$`).Match(c), "empty body never matches")

	c.RespBody = []byte(strings.Repeat("a", 100) + "needle")
	assert.False(t, mustParse(t, `~bs needle`, WithMaxBodyBytes(100)).Match(c), "beyond limit")
	assert.True(t, mustParse(t, `~bs needle`, WithMaxBodyBytes(106)).Match(c))
	assert.True(t, mustParse(t, `~bs needle`).Match(c), "default limit is 64 KiB")
	assert.False(t, mustParse(t, `~bs needle`, WithMaxBodyBytes(-5), WithMaxBodyBytes(0)).Match(&Context{HasResponse: true, RespBody: append([]byte(strings.Repeat("a", DefaultMaxBodyBytes)), "needle"...)}))

	c.RespBody = []byte(strings.Repeat("a", 10) + "\u00e9\u00e9needle")
	assert.False(t, mustParse(t, `~bs ^a+$`, WithMaxBodyBytes(12)).Match(c))
	assert.True(t, mustParse(t, `~bs ^a+$`, WithMaxBodyBytes(11)).Match(c), "cut inside a rune is trimmed, not rejected")
	assert.True(t, mustParse(t, `~bs a`, WithMaxBodyBytes(11)).Match(c), "cut inside a rune is trimmed, not rejected")

	req := &Context{ReqBody: []byte("payload")}
	assert.True(t, mustParse(t, `~bq payload`).Match(req))
	assert.False(t, mustParse(t, `~bs payload`).Match(req), "~bs is false in request phase")
	req.RespBody = []byte("payload")
	assert.False(t, mustParse(t, `~bs payload`).Match(req), "response body ignored without response")
	assert.False(t, mustParse(t, `~b payload`).Match(&Context{RespBody: []byte("payload")}))
}

func TestTwoPhaseSemantics(t *testing.T) {
	req := reqCtx()
	for _, expr := range []string{`~t html`, `~c 404`, `~hs nginx`, `~bs "not found"`, `~s`} {
		assert.False(t, mustParse(t, expr).Match(req), expr)
	}
	assert.True(t, mustParse(t, `~q`).Match(req))
	assert.True(t, mustParse(t, `!~t html`).Match(req), "negation of a response atom is true in request phase")

	resp := respCtx()
	assert.False(t, mustParse(t, `~q`).Match(resp))
	assert.True(t, mustParse(t, `~s`).Match(resp))

	stale := reqCtx()
	stale.Status = 404
	stale.ContentType = "text/html"
	stale.RespHeader = http.Header{"Server": {"nginx"}}
	assert.False(t, mustParse(t, `~c 404`).Match(stale))
	assert.False(t, mustParse(t, `~t html`).Match(stale))
	assert.False(t, mustParse(t, `~h nginx`).Match(stale))
}

func TestHostOnly(t *testing.T) {
	assert.True(t, mustParse(t, `~d a & (~src 10.0.0.0/8 | !~dst 1.1.1.1) | ~all | ~none`).HostOnly())
	for _, a := range []string{`~u x`, `~t x`, `~c 2xx`, `~m x`, `~h x`, `~hq x`, `~hs x`, `~q`, `~s`, `~b x`, `~bq x`, `~bs x`} {
		assert.False(t, mustParse(t, `~d a & `+a).HostOnly(), a)
		assert.False(t, mustParse(t, `~d a | !`+a).HostOnly(), a)
	}
}

func TestExprConcurrent(t *testing.T) {
	e := mustParse(t, `~u mp4 & ~h curl & ~b alice`)
	done := make(chan bool)
	for range 8 {
		go func() {
			ok := true
			for range 200 {
				ok = ok && e.Match(reqCtx())
			}
			done <- ok
		}()
	}
	for range 8 {
		assert.True(t, <-done)
	}
}

func TestMatchAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("sync.Pool drops items randomly under the race detector")
	}
	c := respCtx()
	for _, expr := range []string{`~d example`, `~u mp4`, `~t html`, `~c 4xx`, `~m post`, `~s`, `~q`, `~src 10.0.0.0/8`, `(~d example & ~u mp4) | ~t video/`} {
		e := mustParse(t, expr)
		allocs := testing.AllocsPerRun(100, func() { e.Match(c) })
		assert.Zero(t, allocs, expr)
	}
}
