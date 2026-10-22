package rules

import (
	"bytes"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// DefaultMaxBodyBytes is the number of body bytes inspected by ~b, ~bq and
// ~bs unless overridden with WithMaxBodyBytes.
const DefaultMaxBodyBytes = 64 << 10

// Context describes the flow an expression is evaluated against. Fields that
// are not available in the current phase must be left at their zero value.
type Context struct {
	Method string
	URL    string
	// Host is the request host without port.
	Host string
	// ContentType is the response media type, lower case, without parameters.
	ContentType string
	Status      int
	ReqHeader   http.Header
	RespHeader  http.Header
	// HasResponse is false while the flow is in the request phase.
	HasResponse bool
	ReqBody     []byte
	RespBody    []byte
	// Src is the client address and Dst the server address.
	Src, Dst netip.Addr
}

type atomKind uint8

const (
	kindHost atomKind = iota
	kindURL
	kindContentType
	kindCode
	kindMethod
	kindHeader
	kindReqHeader
	kindRespHeader
	kindRequestPhase
	kindHasResponse
	kindBody
	kindReqBody
	kindRespBody
	kindSrc
	kindDst
	kindAll
	kindNone
)

type atomInfo struct {
	name     string
	hasArg   bool
	regex    bool
	fold     bool
	hostOnly bool
}

var atomInfos = [...]atomInfo{
	kindHost:         {"d", true, true, true, true},
	kindURL:          {"u", true, true, false, false},
	kindContentType:  {"t", true, true, true, false},
	kindCode:         {"c", true, false, false, false},
	kindMethod:       {"m", true, true, true, false},
	kindHeader:       {"h", true, true, false, false},
	kindReqHeader:    {"hq", true, true, false, false},
	kindRespHeader:   {"hs", true, true, false, false},
	kindRequestPhase: {"q", false, false, false, false},
	kindHasResponse:  {"s", false, false, false, false},
	kindBody:         {"b", true, true, false, false},
	kindReqBody:      {"bq", true, true, false, false},
	kindRespBody:     {"bs", true, true, false, false},
	kindSrc:          {"src", true, false, false, true},
	kindDst:          {"dst", true, false, false, true},
	kindAll:          {"all", false, false, false, true},
	kindNone:         {"none", false, false, false, true},
}

func kindByName(name string) (atomKind, bool) {
	for k := range atomInfos {
		if atomInfos[k].name == name {
			return atomKind(k), true
		}
	}
	return 0, false
}

type atom struct {
	kind    atomKind
	arg     string
	re      *regexp.Regexp
	prefix  netip.Prefix
	lo, hi  int
	maxBody int
}

type opKind uint8

const (
	opAtom opKind = iota
	opAnd
	opOr
	opNot
)

type node struct {
	op   opKind
	kids []*node
	a    atom
}

// Expr is a compiled filter expression. It is immutable and safe for
// concurrent use.
type Expr struct {
	root     *node
	hostOnly bool
}

// Match reports whether the expression holds for c.
func (e *Expr) Match(c *Context) bool { return e.root.eval(c) }

// HostOnly reports whether the expression only uses ~d, ~src, ~dst, ~all and
// ~none, so it can be decided at CONNECT time before any request exists.
func (e *Expr) HostOnly() bool { return e.hostOnly }

// String returns the canonical form of the expression. Parsing it yields an
// equivalent expression with the same canonical form.
func (e *Expr) String() string {
	var sb strings.Builder
	e.root.write(&sb)
	return sb.String()
}

func (n *node) eval(c *Context) bool {
	switch n.op {
	case opAnd:
		for _, k := range n.kids {
			if !k.eval(c) {
				return false
			}
		}
		return true
	case opOr:
		for _, k := range n.kids {
			if k.eval(c) {
				return true
			}
		}
		return false
	case opNot:
		return !n.kids[0].eval(c)
	default:
		return n.a.match(c)
	}
}

func (a *atom) match(c *Context) bool {
	switch a.kind {
	case kindAll:
		return true
	case kindNone:
		return false
	case kindHost:
		return a.re.MatchString(normalize(c.Host))
	case kindURL:
		return a.re.MatchString(c.URL)
	case kindContentType:
		return c.HasResponse && a.re.MatchString(c.ContentType)
	case kindCode:
		return c.HasResponse && c.Status >= a.lo && c.Status <= a.hi
	case kindMethod:
		return a.re.MatchString(c.Method)
	case kindHeader:
		return a.matchHeaders(c.ReqHeader) || (c.HasResponse && a.matchHeaders(c.RespHeader))
	case kindReqHeader:
		return a.matchHeaders(c.ReqHeader)
	case kindRespHeader:
		return c.HasResponse && a.matchHeaders(c.RespHeader)
	case kindRequestPhase:
		return !c.HasResponse
	case kindHasResponse:
		return c.HasResponse
	case kindBody:
		return a.matchBody(c.ReqBody) || (c.HasResponse && a.matchBody(c.RespBody))
	case kindReqBody:
		return a.matchBody(c.ReqBody)
	case kindRespBody:
		return c.HasResponse && a.matchBody(c.RespBody)
	case kindSrc:
		return a.matchAddr(c.Src)
	case kindDst:
		return a.matchAddr(c.Dst)
	}
	return false
}

func (a *atom) matchAddr(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	return a.prefix.Contains(ip.WithZone("").Unmap())
}

func (a *atom) matchHeaders(h http.Header) bool {
	for name, vals := range h {
		for _, v := range vals {
			var buf [192]byte
			line := append(buf[:0], name...)
			line = append(line, ':', ' ')
			line = append(line, v...)
			if a.re.Match(line) {
				return true
			}
		}
	}
	return false
}

func (a *atom) matchBody(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	if len(b) > a.maxBody {
		b = b[:a.maxBody]
		for k := 1; k <= utf8.UTFMax-1 && k <= len(b); k++ {
			if utf8.RuneStart(b[len(b)-k]) {
				if !utf8.FullRune(b[len(b)-k:]) {
					b = b[:len(b)-k]
				}
				break
			}
		}
	}
	if bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
		return false
	}
	return a.re.Match(b)
}

func (n *node) write(sb *strings.Builder) {
	switch n.op {
	case opAtom:
		n.a.write(sb)
	case opNot:
		sb.WriteByte('!')
		k := n.kids[0]
		if k.op == opAnd || k.op == opOr {
			sb.WriteByte('(')
			k.write(sb)
			sb.WriteByte(')')
		} else {
			k.write(sb)
		}
	case opAnd, opOr:
		sep := " & "
		if n.op == opOr {
			sep = " | "
		}
		for i, k := range n.kids {
			if i > 0 {
				sb.WriteString(sep)
			}
			if k.op == opOr && n.op == opAnd {
				sb.WriteByte('(')
				k.write(sb)
				sb.WriteByte(')')
			} else {
				k.write(sb)
			}
		}
	}
}

func (a *atom) write(sb *strings.Builder) {
	info := atomInfos[a.kind]
	sb.WriteByte('~')
	sb.WriteString(info.name)
	if !info.hasArg {
		return
	}
	sb.WriteByte(' ')
	writeArg(sb, a.arg)
}

func writeArg(sb *strings.Builder, s string) {
	if s != "" && !strings.ContainsAny(s, " \t\n\r\v\f()|&!\"'") {
		sb.WriteString(s)
		return
	}
	sb.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			if i+1 == len(s) || s[i+1] == '\\' || s[i+1] == '"' || s[i+1] == '\'' {
				sb.WriteString(`\\`)
			} else {
				sb.WriteByte('\\')
			}
		default:
			sb.WriteByte(c)
		}
	}
	sb.WriteByte('"')
}

func (n *node) explain(c *Context) (bool, string) {
	switch n.op {
	case opNot:
		ok, r := n.kids[0].explain(c)
		return !ok, "negation of (" + r + ")"
	case opAnd:
		var parts []string
		for _, k := range n.kids {
			ok, r := k.explain(c)
			if !ok {
				return false, r
			}
			parts = append(parts, r)
		}
		return true, strings.Join(parts, "; ")
	case opOr:
		var parts []string
		for _, k := range n.kids {
			ok, r := k.explain(c)
			if ok {
				return true, r
			}
			parts = append(parts, r)
		}
		return false, strings.Join(parts, "; ")
	default:
		ok := n.a.match(c)
		return ok, n.a.reason(c, ok)
	}
}

func (a *atom) reason(c *Context, ok bool) string {
	var sb strings.Builder
	a.write(&sb)
	label := sb.String()
	switch a.kind {
	case kindAll, kindNone:
		return label + " is constant"
	case kindRequestPhase:
		if ok {
			return "flow has no response yet"
		}
		return "flow already has a response (~q)"
	case kindHasResponse:
		if ok {
			return "flow has a response"
		}
		return "flow has no response yet (~s)"
	case kindContentType, kindCode, kindRespHeader, kindRespBody:
		if !c.HasResponse {
			return label + " needs a response and there is none yet"
		}
	}
	verb := "did not match"
	if ok {
		verb = "matched"
	}
	var subject string
	switch a.kind {
	case kindHost:
		subject = "host " + strconv.Quote(c.Host)
	case kindURL:
		subject = "URL " + strconv.Quote(c.URL)
	case kindContentType:
		subject = "content type " + strconv.Quote(c.ContentType)
	case kindCode:
		subject = "status " + strconv.Itoa(c.Status)
	case kindMethod:
		subject = "method " + strconv.Quote(c.Method)
	case kindHeader, kindReqHeader, kindRespHeader:
		subject = "headers"
	case kindBody, kindReqBody, kindRespBody:
		subject = "text body"
	case kindSrc:
		subject = "source " + c.Src.String()
	case kindDst:
		subject = "destination " + c.Dst.String()
	}
	return fmt.Sprintf("%s %s %s", label, verb, subject)
}
