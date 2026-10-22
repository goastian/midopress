package rules

import (
	"fmt"
	"testing"
)

func benchRules() []Rule {
	exprs := []string{
		`~d (bank|banco|paypal|health)\.`,
		`~u \.(mp4|webm|m3u8|ts)$ | ~t video/`,
		`~d ads\. | ~d tracker`,
		`~m ^(GET|HEAD)$ & ~c 2xx`,
		`~t ^image/ & ~c 200`,
		`~u /api/ & !~m options`,
		`~src 192.168.0.0/16 & ~d intranet`,
		`~t json & ~s`,
		`~q & ~u login`,
		`~d \.gov$`,
		`~u \.(png|jpe?g|gif|webp)$`,
		`~d cdn & ~t font/`,
		`~c 5xx`,
		`~u utm_source`,
		`~t text/html & ~c 2xx`,
		`~d example\.org & ~m post`,
		`~dst 10.0.0.0/8`,
		`~u /static/ | ~u /assets/`,
		`~d (a|b|c)\.test`,
		`~m delete`,
	}
	rules := make([]Rule, len(exprs))
	for i, e := range exprs {
		rules[i] = Rule{Match: e, Profile: fmt.Sprintf("p%d", i)}
	}
	return rules
}

func BenchmarkExprMatch(b *testing.B) {
	c := respCtx()
	for _, expr := range []string{`~d example`, `~u mp4`, `~t html`, `~c 4xx`, `~m post`, `~s`, `~q`, `~src 10.0.0.0/8`, `~h nginx`, `~bs "not found"`} {
		e := mustParse(b, expr)
		b.Run(expr, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				e.Match(c)
			}
		})
	}
}

func BenchmarkEvaluate20Rules(b *testing.B) {
	rs := mustCompile(b, benchRules())
	c := respCtx()
	b.ReportAllocs()
	for b.Loop() {
		rs.Evaluate(c)
	}
}

func BenchmarkEvaluateHost(b *testing.B) {
	rs := mustCompile(b, benchRules())
	c := respCtx()
	b.ReportAllocs()
	for b.Loop() {
		rs.EvaluateHost(c.Host, c.Src, c.Dst)
	}
}

func BenchmarkParse(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Parse(`~u \.(mp4|webm|m3u8|ts)$ | (~d a | ~d b) & ~t video/`); err != nil {
			b.Fatal(err)
		}
	}
}
