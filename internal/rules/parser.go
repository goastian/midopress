package rules

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxNesting = 200

// ParseError is returned by Parse. Column is 1-based and counts characters.
type ParseError struct {
	Column int
	Msg    string
}

// Error implements error.
func (e *ParseError) Error() string {
	return fmt.Sprintf("column %d: %s", e.Column, e.Msg)
}

type parseConfig struct {
	maxBody int
}

// ParseOption customizes Parse.
type ParseOption func(*parseConfig)

// WithMaxBodyBytes sets how many body bytes ~b, ~bq and ~bs inspect.
// Non-positive values are ignored.
func WithMaxBodyBytes(n int) ParseOption {
	return func(c *parseConfig) {
		if n > 0 {
			c.maxBody = n
		}
	}
}

type parser struct {
	src      string
	pos      int
	parens   int
	nesting  int
	cfg      parseConfig
	hostOnly bool
}

// Parse compiles s into an expression. Regexes, CIDRs and status codes are
// validated at parse time and the error carries the column of the problem.
func Parse(s string, opts ...ParseOption) (*Expr, error) {
	p := &parser{src: s, cfg: parseConfig{maxBody: DefaultMaxBodyBytes}, hostOnly: true}
	for _, o := range opts {
		o(&p.cfg)
	}
	p.skipSpace()
	if p.pos >= len(s) {
		return nil, p.errAt(p.pos, "empty expression")
	}
	root, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.pos < len(s) {
		return nil, p.trailingError()
	}
	return &Expr{root: root, hostOnly: p.hostOnly}, nil
}

func (p *parser) errAt(off int, format string, args ...any) error {
	if off > len(p.src) {
		off = len(p.src)
	}
	return &ParseError{
		Column: utf8.RuneCountInString(p.src[:off]) + 1,
		Msg:    fmt.Sprintf(format, args...),
	}
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

func (p *parser) skipSpace() {
	for p.pos < len(p.src) && isSpace(p.src[p.pos]) {
		p.pos++
	}
}

func (p *parser) describeAt(off int) string {
	r, _ := utf8.DecodeRuneInString(p.src[off:])
	if r == utf8.RuneError {
		return fmt.Sprintf("byte 0x%02x", p.src[off])
	}
	return strconv.QuoteRune(r)
}

// trailingError explains why parsing stopped before the end of the input.
func (p *parser) trailingError() error {
	c := p.src[p.pos]
	switch {
	case c == ')' && p.parens == 0:
		return p.errAt(p.pos, "unmatched ')'")
	case c == ')':
		return p.errAt(p.pos, "unexpected ')'")
	default:
		return p.errAt(p.pos, "unexpected %s after a complete expression; implicit AND is not supported, use '&' or '|'", p.describeAt(p.pos))
	}
}

func (p *parser) enter() error {
	p.nesting++
	if p.nesting > maxNesting {
		return p.errAt(p.pos, "expression nested too deeply (limit %d)", maxNesting)
	}
	return nil
}

func (p *parser) parseOr() (*node, error) {
	first, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	kids := []*node{first}
	for {
		p.skipSpace()
		if p.pos >= len(p.src) || p.src[p.pos] != '|' {
			break
		}
		p.pos++
		n, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		kids = append(kids, n)
	}
	if len(kids) == 1 {
		return first, nil
	}
	return &node{op: opOr, kids: kids}, nil
}

func (p *parser) parseAnd() (*node, error) {
	first, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	kids := []*node{first}
	for {
		p.skipSpace()
		if p.pos >= len(p.src) || p.src[p.pos] != '&' {
			break
		}
		p.pos++
		n, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		kids = append(kids, n)
	}
	if len(kids) == 1 {
		return first, nil
	}
	return &node{op: opAnd, kids: kids}, nil
}

func (p *parser) parseUnary() (*node, error) {
	p.skipSpace()
	if p.pos >= len(p.src) {
		return nil, p.errAt(p.pos, "unexpected end of expression, expected a filter, '!' or '('")
	}
	switch c := p.src[p.pos]; c {
	case '!':
		if err := p.enter(); err != nil {
			return nil, err
		}
		p.pos++
		k, err := p.parseUnary()
		p.nesting--
		if err != nil {
			return nil, err
		}
		return &node{op: opNot, kids: []*node{k}}, nil
	case '(':
		if err := p.enter(); err != nil {
			return nil, err
		}
		open := p.pos
		p.pos++
		p.parens++
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if p.pos >= len(p.src) {
			return nil, p.errAt(open, "missing ')' for this '('")
		}
		if p.src[p.pos] != ')' {
			return nil, p.trailingError()
		}
		p.pos++
		p.parens--
		p.nesting--
		return inner, nil
	case '~':
		return p.parseAtom()
	case ')', '&', '|':
		return nil, p.errAt(p.pos, "unexpected '%c', expected a filter, '!' or '('", c)
	default:
		return nil, p.errAt(p.pos, "unexpected %s, expected a filter such as ~d, '!' or '('", p.describeAt(p.pos))
	}
}

func (p *parser) parseAtom() (*node, error) {
	start := p.pos
	p.pos++
	nameStart := p.pos
	for p.pos < len(p.src) && p.src[p.pos] >= 'a' && p.src[p.pos] <= 'z' {
		p.pos++
	}
	name := p.src[nameStart:p.pos]
	kind, ok := kindByName(name)
	if !ok {
		if name == "" {
			return nil, p.errAt(start, "'~' must be followed by a filter name")
		}
		return nil, p.errAt(start, "unknown filter ~%s", name)
	}
	info := atomInfos[kind]
	if !info.hostOnly {
		p.hostOnly = false
	}
	a := atom{kind: kind, maxBody: p.cfg.maxBody}
	if !info.hasArg {
		if p.pos < len(p.src) && !isSpace(p.src[p.pos]) && !strings.ContainsRune("()&|", rune(p.src[p.pos])) {
			return nil, p.errAt(p.pos, "~%s takes no argument but is followed by %s", name, p.describeAt(p.pos))
		}
		return &node{op: opAtom, a: a}, nil
	}
	p.skipSpace()
	argOff := p.pos
	arg, err := p.readArg(name)
	if err != nil {
		return nil, err
	}
	a.arg = arg
	if err := p.compileArg(&a, info, argOff); err != nil {
		return nil, err
	}
	return &node{op: opAtom, a: a}, nil
}

func (p *parser) readArg(name string) (string, error) {
	if p.pos >= len(p.src) {
		return "", p.errAt(p.pos, "~%s needs an argument", name)
	}
	if q := p.src[p.pos]; q == '"' || q == '\'' {
		return p.readQuoted(q)
	}
	end := p.pos
	for end < len(p.src) && !isSpace(p.src[end]) {
		end++
	}
	word := p.src[p.pos:end]
	word = word[:len(word)-p.trailingClosers(word)]
	if word == "" {
		return "", p.errAt(p.pos, "~%s needs an argument", name)
	}
	p.pos += len(word)
	return word, nil
}

// trailingClosers returns how many trailing unbalanced ')' of word are
// expression syntax instead of belonging to the argument. They are only
// recognized inside a group; surplus ones are reported by the caller.
func (p *parser) trailingClosers(word string) int {
	if p.parens == 0 {
		return 0
	}
	var unmatched []int
	open := 0
	for i := 0; i < len(word); i++ {
		switch word[i] {
		case '\\':
			i++
		case '(':
			open++
		case ')':
			if open > 0 {
				open--
			} else {
				unmatched = append(unmatched, i)
			}
		}
	}
	n := 0
	end := len(word) - 1
	for j := len(unmatched) - 1; j >= 0 && unmatched[j] == end; j-- {
		n++
		end--
	}
	return n
}

func (p *parser) readQuoted(q byte) (string, error) {
	open := p.pos
	var sb strings.Builder
	i := p.pos + 1
	for i < len(p.src) {
		c := p.src[i]
		switch {
		case c == '\\' && i+1 < len(p.src) && (p.src[i+1] == '"' || p.src[i+1] == '\'' || p.src[i+1] == '\\'):
			sb.WriteByte(p.src[i+1])
			i += 2
		case c == q:
			p.pos = i + 1
			if p.pos < len(p.src) {
				if n := p.src[p.pos]; !isSpace(n) && n != ')' && n != '&' && n != '|' {
					return "", p.errAt(p.pos, "unexpected %s right after the closing quote; separate it with a space", p.describeAt(p.pos))
				}
			}
			return sb.String(), nil
		default:
			sb.WriteByte(c)
			i++
		}
	}
	return "", p.errAt(open, "unterminated quoted string, missing closing %c", q)
}

func (p *parser) compileArg(a *atom, info atomInfo, off int) error {
	switch {
	case info.regex:
		pat := a.arg
		if _, err := regexp.Compile(pat); err != nil {
			return p.errAt(off, "invalid regular expression for ~%s: %s", info.name, regexMessage(err))
		}
		if info.fold {
			pat = "(?i)" + pat
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return p.errAt(off, "invalid regular expression for ~%s: %s", info.name, regexMessage(err))
		}
		a.re = re
	case a.kind == kindCode:
		lo, hi, err := parseCode(a.arg)
		if err != nil {
			return p.errAt(off, "%v", err)
		}
		a.lo, a.hi = lo, hi
		if lo == hi {
			a.arg = strconv.Itoa(lo)
		} else {
			a.arg = strconv.Itoa(lo/100) + "xx"
		}
	default:
		pf, err := parseAddrOrPrefix(a.arg)
		if err != nil {
			return p.errAt(off, "invalid address for ~%s: %v", info.name, err)
		}
		a.prefix = pf
		a.arg = pf.String()
		if pf.IsSingleIP() {
			a.arg = pf.Addr().String()
		}
	}
	return nil
}

func regexMessage(err error) string {
	var se *syntax.Error
	if errors.As(err, &se) {
		return fmt.Sprintf("%s: `%s`", se.Code, se.Expr)
	}
	return err.Error()
}

func parseCode(s string) (lo, hi int, err error) {
	if len(s) == 3 && (s[1] == 'x' || s[1] == 'X') && (s[2] == 'x' || s[2] == 'X') && s[0] >= '1' && s[0] <= '5' {
		c := int(s[0]-'0') * 100
		return c, c + 99, nil
	}
	n, convErr := strconv.Atoi(s)
	if convErr != nil || len(s) != 3 || n < 100 || n > 599 {
		return 0, 0, fmt.Errorf("invalid status %q for ~c: use a code from 100 to 599 such as 404 or a class such as 4xx", s)
	}
	return n, n, nil
}

func parseAddrOrPrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		pf, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, err
		}
		pf = pf.Masked()
		if a := pf.Addr(); a.Is4In6() && pf.Bits() >= 96 {
			pf = netip.PrefixFrom(a.Unmap(), pf.Bits()-96)
		}
		return pf, nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	if a.Zone() != "" {
		return netip.Prefix{}, errors.New("zones are not supported")
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}
