package rules

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCanonical(t *testing.T) {
	tests := []struct {
		in, want string
		hostOnly bool
	}{
		{`~d example\.com`, `~d example\.com`, true},
		{`~d (bank|banco|paypal|health)\.`, `~d "(bank|banco|paypal|health)\."`, true},
		{`~u \.(mp4|webm|m3u8|ts)$ | ~t video/`, `~u "\.(mp4|webm|m3u8|ts)$" | ~t video/`, false},
		{`(~d a | ~d b) & ~t image/`, `(~d a | ~d b) & ~t image/`, false},
		{`~d a|b`, `~d "a|b"`, true},
		{`~d a&b`, `~d "a&b"`, true},
		{`~all`, `~all`, true},
		{`  ~none  `, `~none`, true},
		{`~q`, `~q`, false},
		{`~s & ~c 404`, `~s & ~c 404`, false},
		{`~c 4XX`, `~c 4xx`, false},
		{`~m get`, `~m get`, false},
		{`~h "X-Foo: bar"`, `~h "X-Foo: bar"`, false},
		{`~hq foo ~hs`, ``, false},
		{`~src 10.0.0.0/8`, `~src 10.0.0.0/8`, true},
		{`~src 10.1.2.3/8`, `~src 10.0.0.0/8`, true},
		{`~dst 192.168.1.1`, `~dst 192.168.1.1`, true},
		{`~dst ::ffff:1.2.3.4`, `~dst 1.2.3.4`, true},
		{`~src 2001:db8::/32`, `~src 2001:db8::/32`, true},
		{`~b secret & ~bq a & ~bs b`, `~b secret & ~bq a & ~bs b`, false},
		{`!~d a`, `!~d a`, true},
		{`!!~d a`, `!!~d a`, true},
		{`!(~d a | ~d b)`, `!(~d a | ~d b)`, true},
		{`~d a & ~d b | ~d c & ~d d`, `~d a & ~d b | ~d c & ~d d`, true},
		{`~d a & (~d b | ~d c)`, `~d a & (~d b | ~d c)`, true},
		{`((~d a))`, `~d a`, true},
		{`~d a & (~d b & ~d c)`, `~d a & ~d b & ~d c`, true},
		{`(~d a | (~d b & !(~d c)))`, `~d a | ~d b & !~d c`, true},
		{`~d "a b"`, `~d "a b"`, true},
		{`~d 'a b'`, `~d "a b"`, true},
		{`~d "a \"b\" c"`, `~d "a \"b\" c"`, true},
		{`~d 'it\'s'`, `~d "it's"`, true},
		{`~d "back\\slash"`, `~d back\slash`, true},
		{`~d "end\\\\"`, `~d end\\`, true},
		{`~d "\."`, `~d \.`, true},
		{`~d ""`, `~d ""`, true},
		{`(~d a)`, `~d a`, true},
		{`(~d a) & (~d b)`, `~d a & ~d b`, true},
		{`(~d "a b")&~q`, `~d "a b" & ~q`, false},
		{`(~d a|b)`, `~d "a|b"`, true},
		{`(~d (a|b))`, `~d "(a|b)"`, true},
		{`((~d (a|b)) | ~d c)`, `~d "(a|b)" | ~d c`, true},
		{`(~d a\))`, `~d "a\)"`, true},
		{`(~d (a)(b))`, `~d "(a)(b)"`, true},
		{"~d a\t&\n~q", `~d a & ~q`, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			e, err := Parse(tt.in)
			if tt.want == "" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, e.String())
			assert.Equal(t, tt.hostOnly, e.HostOnly())
			e2, err := Parse(e.String())
			require.NoError(t, err)
			assert.Equal(t, e.String(), e2.String())
		})
	}
}

func TestParsePrecedence(t *testing.T) {
	c := &Context{Host: "a", Method: "GET"}
	tests := []struct {
		in   string
		want bool
	}{
		{`~all | ~none & ~none`, true},
		{`(~all | ~none) & ~none`, false},
		{`!~none & ~none`, false},
		{`!(~none & ~none)`, true},
		{`!~all | ~all`, true},
		{`~none & ~none | ~all`, true},
		{`~none | ~none & ~all`, false},
		{`!!~all`, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			e, err := Parse(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, e.Match(c))
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		in      string
		col     int
		contain string
	}{
		{``, 1, "empty expression"},
		{`   `, 4, "empty expression"},
		{`~d`, 3, "needs an argument"},
		{`~d   `, 6, "needs an argument"},
		{`~d (a`, 4, "regular expression"},
		{`~d (`, 4, "missing closing )"},
		{`~d [a`, 4, "regular expression"},
		{`~x foo`, 1, "unknown filter ~x"},
		{`~ foo`, 1, "followed by a filter name"},
		{`~D foo`, 1, "followed by a filter name"},
		{`foo`, 1, "unexpected 'f'"},
		{`~d a ~q`, 6, "implicit AND"},
		{`~q ~s`, 4, "implicit AND"},
		{`~q"x"`, 3, "takes no argument"},
		{`~qx`, 1, "unknown filter ~qx"},
		{`~q x`, 4, "implicit AND"},
		{`~d "a" ~q`, 8, "implicit AND"},
		{`~d "a"b`, 7, "right after the closing quote"},
		{`~d "abc`, 4, "unterminated"},
		{`~d 'abc"`, 4, "unterminated"},
		{`(~d a`, 1, "missing ')'"},
		{`((~d a)`, 1, "missing ')'"},
		{`(~d a))`, 7, "unmatched ')'"},
		{`~d a)`, 4, "regular expression"},
		{`~q)`, 3, "unmatched ')'"},
		{`()`, 2, "unexpected ')'"},
		{`~q &`, 5, "unexpected end"},
		{`~q |`, 5, "unexpected end"},
		{`& ~q`, 1, "unexpected '&'"},
		{`~q | | ~s`, 6, "unexpected '|'"},
		{`!`, 2, "unexpected end"},
		{`~c 99`, 4, "invalid status"},
		{`~c 600`, 4, "invalid status"},
		{`~c 6xx`, 4, "invalid status"},
		{`~c abc`, 4, "invalid status"},
		{`~c 4x`, 4, "invalid status"},
		{`~src 1.2.3`, 6, "invalid address"},
		{`~dst 1.2.3.4/33`, 6, "invalid address"},
		{`~src fe80::1%eth0`, 6, "zones"},
		{`~q & ~dst nope`, 11, "invalid address"},
		{`~q & (~t (x)`, 6, "missing ')'"},
		{`~d é & ~x`, 8, "unknown filter"},
		{"~d a \xff", 6, "byte 0xff"},
		{`~d a & ~d [`, 11, "regular expression"},
		{`~d a ( ~q`, 6, "implicit AND"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			e, err := Parse(tt.in)
			require.Error(t, err)
			assert.Nil(t, e)
			var pe *ParseError
			require.True(t, errors.As(err, &pe))
			assert.Equal(t, tt.col, pe.Column, err.Error())
			assert.Contains(t, err.Error(), tt.contain)
			assert.True(t, strings.HasPrefix(err.Error(), "column "))
		})
	}
}

func TestParseCaseInsensitiveFlag(t *testing.T) {
	e, err := Parse(`~d EXAMPLE & ~t IMAGE/ & ~m get`)
	require.NoError(t, err)
	assert.True(t, e.Match(&Context{Host: "example.com", ContentType: "image/png", HasResponse: true, Method: "GET"}))

	e, err = Parse(`~u Foo`)
	require.NoError(t, err)
	assert.False(t, e.Match(&Context{URL: "http://x/foo"}))
	e, err = Parse(`~u (?i)Foo`)
	require.NoError(t, err)
	assert.True(t, e.Match(&Context{URL: "http://x/foo"}))
}

func TestParseNestingLimit(t *testing.T) {
	_, err := Parse(strings.Repeat("(", 500) + "~q" + strings.Repeat(")", 500))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too deeply")
	_, err = Parse(strings.Repeat("!", 500) + "~q")
	require.Error(t, err)
	_, err = Parse(strings.Repeat("(", 50) + "~q" + strings.Repeat(")", 50))
	require.NoError(t, err)
}

func TestHostMatcherCompat(t *testing.T) {
	for _, re := range []string{`(bank|banco)\.`, `^foo\.`, `\.example\.com$`} {
		m, err := NewHostMatcher([]string{"~d " + re})
		require.NoError(t, err)
		e, err := Parse("~d " + re)
		require.NoError(t, err)
		for _, h := range []string{"bank.com", "foo.bar", "x.example.com", "other.org"} {
			assert.Equal(t, m.Match(h), e.Match(&Context{Host: h}), "%s vs %s", re, h)
		}
	}
}
