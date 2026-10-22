package rules

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustCompile(t testing.TB, rules []Rule, opts ...CompileOption) *RuleSet {
	t.Helper()
	rs, err := Compile(rules, opts...)
	require.NoError(t, err)
	return rs
}

func TestEvaluateAccumulation(t *testing.T) {
	rs := mustCompile(t, []Rule{
		{Match: `~all`, Profile: "base", Set: map[string]any{"a": 1, "b": 1}},
		{Match: `~d example`, Set: map[string]any{"b": 2, "c": 2}},
		{Match: `~none`, Profile: "never"},
		{Match: `~u video`, Profile: "video", Set: map[string]any{"c": 3}},
	})
	assert.Equal(t, 4, rs.Len())

	d := rs.Evaluate(reqCtx())
	assert.Equal(t, ActionNone, d.Action)
	assert.Equal(t, "video", d.Profile)
	assert.Equal(t, map[string]any{"a": 1, "b": 2, "c": 3}, d.Set)
	assert.Equal(t, []int{0, 1, 3}, d.Matched)

	d = rs.Evaluate(&Context{Host: "other.org", URL: "http://other.org/"})
	assert.Equal(t, "base", d.Profile)
	assert.Equal(t, map[string]any{"a": 1, "b": 1}, d.Set)
	assert.Equal(t, []int{0}, d.Matched)
}

func TestEvaluateDoesNotMutateRules(t *testing.T) {
	set := map[string]any{"a": 1}
	rs := mustCompile(t, []Rule{{Match: `~all`, Set: set}})
	d := rs.Evaluate(&Context{})
	d.Set["a"] = 99
	set["a"] = 100
	assert.Equal(t, 1, rs.Evaluate(&Context{}).Set["a"])
}

func TestEvaluateTerminalAction(t *testing.T) {
	rs := mustCompile(t, []Rule{
		{Match: `~all`, Profile: "base", Set: map[string]any{"x": 1}},
		{Match: `~d example`, Action: ActionPassthrough, Profile: "own", Set: map[string]any{"y": 2}},
		{Match: `~all`, Profile: "late", Set: map[string]any{"z": 3}},
	})
	d := rs.Evaluate(reqCtx())
	assert.Equal(t, ActionPassthrough, d.Action)
	assert.Equal(t, "own", d.Profile)
	assert.Equal(t, map[string]any{"x": 1, "y": 2}, d.Set)
	assert.Equal(t, []int{0, 1}, d.Matched)

	d = rs.Evaluate(&Context{Host: "other"})
	assert.Equal(t, ActionNone, d.Action)
	assert.Equal(t, "late", d.Profile)
	assert.Equal(t, []int{0, 2}, d.Matched)
}

func TestEvaluateEmpty(t *testing.T) {
	rs := mustCompile(t, nil)
	assert.Equal(t, Decision{}, rs.Evaluate(&Context{}))
	assert.Equal(t, 0, rs.Len())
}

func TestEvaluateHost(t *testing.T) {
	rs := mustCompile(t, []Rule{
		{Match: `~u secret`, Action: ActionPassthrough},
		{Match: `~q`, Profile: "req"},
		{Match: `~d (bank|paypal)\.`, Action: ActionPassthroughTLS},
		{Match: `~src 10.0.0.0/8 & ~d corp`, Profile: "corp"},
	})
	src := netip.MustParseAddr("10.0.0.5")
	dst := netip.MustParseAddr("1.1.1.1")

	d := rs.EvaluateHost("www.bank.com:443", src, dst)
	assert.Equal(t, ActionPassthroughTLS, d.Action)
	assert.Equal(t, []int{2}, d.Matched)

	d = rs.EvaluateHost("corp.example", src, dst)
	assert.Equal(t, "corp", d.Profile)
	assert.Equal(t, []int{3}, d.Matched)

	d = rs.EvaluateHost("corp.example", netip.MustParseAddr("8.8.8.8"), dst)
	assert.Empty(t, d.Matched)
}

func TestCompileErrors(t *testing.T) {
	_, err := Compile([]Rule{
		{Match: `~d ok`, Profile: "p"},
		{Match: `~d (`, Profile: "p"},
		{Match: `~d ok`},
		{Match: `~d ok`, Action: "drop"},
		{Match: `~u x`, Action: ActionPassthroughTLS},
		{Match: `~d ok`, Profile: "nope"},
		{Match: ``, Profile: "p"},
		{Match: `~d ok ~q`, Profile: "p"},
		{Match: `~d ok`, Action: ActionPassthroughTLS},
	}, WithProfiles("p", "q"))
	require.Error(t, err)

	byIndex := map[int]*RuleError{}
	for _, e := range err.(interface{ Unwrap() []error }).Unwrap() {
		var re *RuleError
		require.True(t, errors.As(e, &re))
		byIndex[re.Index] = re
	}
	assert.Len(t, byIndex, 7)
	assert.NotContains(t, byIndex, 0)
	assert.NotContains(t, byIndex, 8)

	assert.Equal(t, "match", byIndex[1].Field)
	assert.Contains(t, byIndex[1].Error(), "rules[1].match: column 4")
	assert.Contains(t, byIndex[2].Error(), "no effect")
	assert.Contains(t, byIndex[3].Error(), `unknown action "drop"`)
	assert.Contains(t, byIndex[4].Error(), "CONNECT time")
	assert.Contains(t, byIndex[5].Error(), `unknown profile "nope"`)
	assert.Contains(t, byIndex[6].Error(), "empty expression")
	assert.Contains(t, byIndex[7].Error(), "implicit AND")

	var pe *ParseError
	assert.True(t, errors.As(byIndex[1], &pe))
}

func TestCompileProfilesNotChecked(t *testing.T) {
	_, err := Compile([]Rule{{Match: `~all`, Profile: "whatever"}})
	require.NoError(t, err)
	_, err = Compile([]Rule{{Match: `~all`, Profile: "whatever"}}, WithProfiles())
	require.Error(t, err)
}

func TestCompileParseOptions(t *testing.T) {
	rs := mustCompile(t, []Rule{{Match: `~bs needle`, Profile: "p"}}, WithParseOptions(WithMaxBodyBytes(4)))
	c := &Context{HasResponse: true, RespBody: []byte("xxxxneedle")}
	assert.Empty(t, rs.Evaluate(c).Matched)
}

func TestExplain(t *testing.T) {
	rs := mustCompile(t, []Rule{
		{Match: `~d example & ~t video/`, Profile: "video"},
		{Match: `~u mp4 | ~u webm`, Action: ActionPassthrough},
		{Match: `!~q`, Profile: "resp"},
		{Match: `~all`, Profile: "x"},
		{Match: `~c 2xx & ~d example`, Profile: "y"},
		{Match: `~d nomatch & ~q`, Profile: "z"},
	})
	c := reqCtx()
	ex := rs.Explain(c)
	require.Len(t, ex, 6)

	assert.True(t, ex[1].Matched)
	assert.Equal(t, ActionPassthrough, ex[1].Action)
	assert.Contains(t, ex[1].Reason, "~u mp4 matched")
	assert.Equal(t, `~u mp4 | ~u webm`, ex[1].Match)

	assert.False(t, ex[0].Matched)
	assert.Contains(t, ex[0].Reason, "needs a response")
	assert.Equal(t, "video", ex[0].Profile)

	assert.False(t, ex[2].Matched)
	assert.Contains(t, ex[2].Reason, "negation")
	assert.True(t, ex[3].Matched)
	assert.Contains(t, ex[3].Reason, "constant")

	assert.False(t, ex[4].Matched)
	assert.Contains(t, ex[4].Reason, "needs a response")
	assert.False(t, ex[5].Matched)
	assert.Contains(t, ex[5].Reason, `~d nomatch did not match host "www.example.com"`)

	ex = rs.Explain(respCtx())
	assert.False(t, ex[0].Matched)
	assert.Contains(t, ex[0].Reason, `~t video/ did not match content type "text/html"`)
	assert.True(t, ex[2].Matched)
	assert.False(t, ex[4].Matched)
	assert.Contains(t, ex[4].Reason, "status 404")

	for i, e := range ex {
		assert.Equal(t, i, e.Index)
	}
}

func TestExplainAgreesWithEvaluate(t *testing.T) {
	rs := mustCompile(t, []Rule{
		{Match: `~d example & !(~c 5xx | ~t json)`, Profile: "a"},
		{Match: `~s | ~m get`, Profile: "b"},
		{Match: `~q & ~src 10.0.0.0/8`, Profile: "c"},
	})
	for _, c := range []*Context{reqCtx(), respCtx(), {}} {
		var want []int
		for _, e := range rs.Explain(c) {
			if e.Matched {
				want = append(want, e.Index)
			}
		}
		assert.Equal(t, want, rs.Evaluate(c).Matched)
	}
}
