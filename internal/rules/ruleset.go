package rules

import (
	"errors"
	"fmt"
	"maps"
	"net/netip"
)

// Action is the terminal action of a rule.
type Action string

const (
	// ActionNone means the rule does not stop evaluation.
	ActionNone Action = ""
	// ActionPassthrough relays traffic without modifying it.
	ActionPassthrough Action = "passthrough"
	// ActionPassthroughTLS relays the TLS stream without intercepting it. It
	// is decided at CONNECT time, so the rule must be HostOnly.
	ActionPassthroughTLS Action = "passthrough_tls"
)

// Rule is the declarative form of a rule, as written in configuration.
type Rule struct {
	Match   string         `yaml:"match"`
	Action  Action         `yaml:"action"`
	Profile string         `yaml:"profile"`
	Set     map[string]any `yaml:"set"`
}

// RuleError reports a problem with one rule. Index is 0-based, matching the
// position in the rules list.
type RuleError struct {
	Index int
	Field string
	Err   error
}

// Error implements error.
func (e *RuleError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("rules[%d]: %v", e.Index, e.Err)
	}
	return fmt.Sprintf("rules[%d].%s: %v", e.Index, e.Field, e.Err)
}

// Unwrap returns the underlying error.
func (e *RuleError) Unwrap() error { return e.Err }

type compileConfig struct {
	profiles    map[string]struct{}
	checkProf   bool
	parseOption []ParseOption
}

// CompileOption customizes Compile.
type CompileOption func(*compileConfig)

// WithProfiles makes Compile reject rules that reference a profile not in
// names.
func WithProfiles(names ...string) CompileOption {
	return func(c *compileConfig) {
		c.checkProf = true
		c.profiles = make(map[string]struct{}, len(names))
		for _, n := range names {
			c.profiles[n] = struct{}{}
		}
	}
}

// WithParseOptions passes options to the expression parser.
func WithParseOptions(opts ...ParseOption) CompileOption {
	return func(c *compileConfig) { c.parseOption = append(c.parseOption, opts...) }
}

type compiledRule struct {
	expr    *Expr
	action  Action
	profile string
	set     map[string]any
}

// RuleSet is an ordered, compiled list of rules. It is immutable and safe for
// concurrent use.
type RuleSet struct {
	rules []compiledRule
}

// Decision is the outcome of evaluating a RuleSet.
type Decision struct {
	Action  Action
	Profile string
	// Set holds the merged settings, or nil when no matching rule set any.
	Set map[string]any
	// Matched lists the indexes of the matching rules, in order.
	Matched []int
}

// Explanation describes how one rule fared against a Context.
type Explanation struct {
	Index   int
	Match   string
	Matched bool
	// Reason names the condition that decided the outcome.
	Reason  string
	Action  Action
	Profile string
}

// Compile validates and compiles rules. Every invalid rule is reported in the
// returned error, which is a join of *RuleError values.
func Compile(rules []Rule, opts ...CompileOption) (*RuleSet, error) {
	var cfg compileConfig
	for _, o := range opts {
		o(&cfg)
	}
	var errs []error
	out := make([]compiledRule, 0, len(rules))
	for i, r := range rules {
		cr, err := compileRule(r, &cfg)
		if err != nil {
			errs = append(errs, &RuleError{Index: i, Field: err.field, Err: err.err})
			cr = compiledRule{}
		}
		out = append(out, cr)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &RuleSet{rules: out}, nil
}

type fieldError struct {
	field string
	err   error
}

func compileRule(r Rule, cfg *compileConfig) (compiledRule, *fieldError) {
	expr, err := Parse(r.Match, cfg.parseOption...)
	if err != nil {
		return compiledRule{}, &fieldError{"match", err}
	}
	switch r.Action {
	case ActionNone, ActionPassthrough, ActionPassthroughTLS:
	default:
		return compiledRule{}, &fieldError{"action", fmt.Errorf("unknown action %q, expected \"passthrough\" or \"passthrough_tls\"", r.Action)}
	}
	if r.Action == ActionPassthroughTLS && !expr.HostOnly() {
		return compiledRule{}, &fieldError{"match", errors.New("passthrough_tls is decided at CONNECT time, so match may only use ~d, ~src, ~dst, ~all and ~none")}
	}
	if r.Action == ActionNone && r.Profile == "" && len(r.Set) == 0 {
		return compiledRule{}, &fieldError{"", errors.New("rule has no effect: set action, profile or set")}
	}
	if cfg.checkProf && r.Profile != "" {
		if _, ok := cfg.profiles[r.Profile]; !ok {
			return compiledRule{}, &fieldError{"profile", fmt.Errorf("unknown profile %q", r.Profile)}
		}
	}
	return compiledRule{
		expr:    expr,
		action:  r.Action,
		profile: r.Profile,
		set:     maps.Clone(r.Set),
	}, nil
}

// Len returns the number of rules.
func (rs *RuleSet) Len() int { return len(rs.rules) }

// Evaluate applies the rules in order. Profile and Set accumulate, later
// rules overriding earlier ones. The first matching rule with an action ends
// the evaluation after its own profile and set have been applied.
func (rs *RuleSet) Evaluate(c *Context) Decision {
	return rs.evaluate(c, false)
}

// EvaluateHost decides at CONNECT time, before any request exists. Rules that
// are not HostOnly are skipped.
func (rs *RuleSet) EvaluateHost(host string, src, dst netip.Addr) Decision {
	c := Context{Host: host, Src: src, Dst: dst}
	return rs.evaluate(&c, true)
}

func (rs *RuleSet) evaluate(c *Context, hostOnly bool) Decision {
	var d Decision
	for i := range rs.rules {
		r := &rs.rules[i]
		if hostOnly && !r.expr.hostOnly {
			continue
		}
		if !r.expr.Match(c) {
			continue
		}
		d.Matched = append(d.Matched, i)
		if r.profile != "" {
			d.Profile = r.profile
		}
		if len(r.set) > 0 {
			if d.Set == nil {
				d.Set = make(map[string]any, len(r.set))
			}
			maps.Copy(d.Set, r.set)
		}
		if r.action != ActionNone {
			d.Action = r.action
			break
		}
	}
	return d
}

// Explain reports, for every rule, whether it matches c and why. It ignores
// terminal actions: all rules are reported.
func (rs *RuleSet) Explain(c *Context) []Explanation {
	out := make([]Explanation, len(rs.rules))
	for i := range rs.rules {
		r := &rs.rules[i]
		ok, reason := r.expr.root.explain(c)
		out[i] = Explanation{
			Index:   i,
			Match:   r.expr.String(),
			Matched: ok,
			Reason:  reason,
			Action:  r.action,
			Profile: r.profile,
		}
	}
	return out
}
