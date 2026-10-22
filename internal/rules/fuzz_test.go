package rules

import (
	"net/http"
	"net/netip"
	"testing"
)

func fuzzSeeds(f *testing.F) {
	for _, s := range []string{
		`~d (bank|banco|paypal|health)\.`,
		`~u \.(mp4|webm|m3u8|ts)$ | ~t video/`,
		`(~d a | ~d b) & ~t image/`,
		`!~q & ~c 4xx`,
		`~h "X-A: b" | ~hq 'x\'y' | ~hs z`,
		`~src 10.0.0.0/8 & ~dst ::1`,
		`~b "a b" & ~bq c & ~bs d`,
		`~all | ~none`,
		`((((~q))))`,
		`~d "\\\""`,
		`~m get ~s`,
		``,
	} {
		f.Add(s)
	}
}

func FuzzParse(f *testing.F) {
	fuzzSeeds(f)
	f.Fuzz(func(t *testing.T, s string) {
		e, err := Parse(s)
		if err != nil {
			if _, ok := err.(*ParseError); !ok {
				t.Fatalf("non ParseError %T: %v", err, err)
			}
			return
		}
		canon := e.String()
		e2, err := Parse(canon)
		if err != nil {
			t.Fatalf("canonical form %q of %q does not parse: %v", canon, s, err)
		}
		if got := e2.String(); got != canon {
			t.Fatalf("round trip changed %q -> %q", canon, got)
		}
		if e.HostOnly() != e2.HostOnly() {
			t.Fatalf("HostOnly changed for %q", canon)
		}
	})
}

func FuzzMatch(f *testing.F) {
	fuzzSeeds(f)
	f.Fuzz(func(t *testing.T, s string) {
		e, err := Parse(s, WithMaxBodyBytes(32))
		if err != nil {
			return
		}
		for _, c := range []*Context{
			{},
			{
				Method: "GET", URL: s, Host: s, ContentType: s, Status: 200, HasResponse: true,
				ReqHeader:  http.Header{s: {s}},
				RespHeader: http.Header{"X": {s, s}},
				ReqBody:    []byte(s), RespBody: []byte(s + "\x00"),
				Src: netip.MustParseAddr("10.0.0.1"), Dst: netip.MustParseAddr("::1"),
			},
		} {
			r1 := e.Match(c)
			if r2 := e.Match(c); r1 != r2 {
				t.Fatalf("non deterministic match for %q", s)
			}
		}
	})
}
