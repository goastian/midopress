package metrics

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

type sample struct {
	name   string
	labels map[string]string
	value  float64
}

type parsedFamily struct {
	help    string
	typ     string
	samples []sample
}

type exposition struct {
	families map[string]*parsedFamily
	order    []string
}

func (e *exposition) find(name string, kv ...string) (float64, bool) {
	for _, f := range e.families {
	next:
		for _, s := range f.samples {
			if s.name != name {
				continue
			}
			for i := 0; i < len(kv); i += 2 {
				if s.labels[kv[i]] != kv[i+1] {
					continue next
				}
			}
			if len(s.labels) != len(kv)/2 {
				continue
			}
			return s.value, true
		}
	}
	return 0, false
}

func isNameStart(c byte, colon bool) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (colon && c == ':')
}

func isNameChar(c byte, colon bool) bool {
	return isNameStart(c, colon) || (c >= '0' && c <= '9')
}

func parseName(s string, colon bool) (string, string, error) {
	if s == "" || !isNameStart(s[0], colon) {
		return "", "", fmt.Errorf("bad name start in %q", s)
	}
	i := 1
	for i < len(s) && isNameChar(s[i], colon) {
		i++
	}
	return s[:i], s[i:], nil
}

func parseValue(s string) (float64, error) {
	switch s {
	case "NaN":
		return math.NaN(), nil
	case "+Inf":
		return math.Inf(1), nil
	case "-Inf":
		return math.Inf(-1), nil
	}
	if s == "" || strings.Trim(s, "0123456789.eE+-") != "" {
		return 0, fmt.Errorf("bad value %q", s)
	}
	return strconv.ParseFloat(s, 64)
}

func parseLabels(s string) (map[string]string, string, error) {
	labels := map[string]string{}
	if s == "" || s[0] != '{' {
		return labels, s, nil
	}
	s = s[1:]
	for {
		if s == "" {
			return nil, "", fmt.Errorf("unterminated label set")
		}
		if s[0] == '}' {
			return labels, s[1:], nil
		}
		name, rest, err := parseName(s, false)
		if err != nil {
			return nil, "", err
		}
		if !strings.HasPrefix(rest, `="`) {
			return nil, "", fmt.Errorf("expected =\" after label %q", name)
		}
		rest = rest[2:]
		var val strings.Builder
		closed := false
		for i := 0; i < len(rest); i++ {
			c := rest[i]
			if c == '\n' {
				return nil, "", fmt.Errorf("raw newline in label value")
			}
			if c == '\\' {
				if i+1 >= len(rest) {
					return nil, "", fmt.Errorf("dangling escape")
				}
				i++
				switch rest[i] {
				case '\\':
					val.WriteByte('\\')
				case '"':
					val.WriteByte('"')
				case 'n':
					val.WriteByte('\n')
				default:
					return nil, "", fmt.Errorf("invalid escape \\%c", rest[i])
				}
				continue
			}
			if c == '"' {
				rest = rest[i+1:]
				closed = true
				break
			}
			val.WriteByte(c)
		}
		if !closed {
			return nil, "", fmt.Errorf("unterminated label value")
		}
		if _, dup := labels[name]; dup {
			return nil, "", fmt.Errorf("duplicate label %q", name)
		}
		labels[name] = val.String()
		switch {
		case strings.HasPrefix(rest, ","):
			s = rest[1:]
		case strings.HasPrefix(rest, "}"):
			s = rest
		default:
			return nil, "", fmt.Errorf("expected , or } after label value")
		}
	}
}

func unescapeHelp(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 >= len(s) {
			return "", fmt.Errorf("dangling escape in HELP")
		}
		i++
		switch s[i] {
		case '\\':
			b.WriteByte('\\')
		case 'n':
			b.WriteByte('\n')
		default:
			return "", fmt.Errorf("invalid HELP escape \\%c", s[i])
		}
	}
	return b.String(), nil
}

func familyOf(e *exposition, name string) (string, *parsedFamily) {
	if f, ok := e.families[name]; ok {
		return name, f
	}
	for _, suf := range []string{"_bucket", "_sum", "_count"} {
		if base, ok := strings.CutSuffix(name, suf); ok {
			if f, ok := e.families[base]; ok && (f.typ == "histogram" || f.typ == "summary") {
				return base, f
			}
		}
	}
	return "", nil
}

func seriesKey(name string, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(name)
	for _, k := range keys {
		fmt.Fprintf(&b, "|%s=%q", k, labels[k])
	}
	return b.String()
}

// parseExposition strictly parses text format 0.0.4 and validates it.
func parseExposition(text string) (*exposition, error) {
	e := &exposition{families: map[string]*parsedFamily{}}
	if text == "" {
		return e, nil
	}
	if !strings.HasSuffix(text, "\n") {
		return nil, fmt.Errorf("missing trailing newline")
	}
	seen := map[string]struct{}{}
	for n, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		fail := func(format string, args ...any) (*exposition, error) {
			return nil, fmt.Errorf("line %d %q: %s", n+1, line, fmt.Sprintf(format, args...))
		}
		switch {
		case line == "":
			return fail("empty line")
		case strings.HasPrefix(line, "# HELP "):
			name, rest, err := parseName(line[7:], true)
			if err != nil {
				return fail("%v", err)
			}
			if !strings.HasPrefix(rest, " ") {
				return fail("HELP needs text")
			}
			help, err := unescapeHelp(rest[1:])
			if err != nil {
				return fail("%v", err)
			}
			if _, dup := e.families[name]; dup {
				return fail("duplicate HELP")
			}
			e.families[name] = &parsedFamily{help: help}
			e.order = append(e.order, name)
		case strings.HasPrefix(line, "# TYPE "):
			name, rest, err := parseName(line[7:], true)
			if err != nil {
				return fail("%v", err)
			}
			f := e.families[name]
			if f == nil {
				return fail("TYPE without preceding HELP")
			}
			if f.typ != "" {
				return fail("duplicate TYPE")
			}
			typ := strings.TrimPrefix(rest, " ")
			switch typ {
			case "counter", "gauge", "histogram", "summary", "untyped":
			default:
				return fail("bad type %q", typ)
			}
			f.typ = typ
		case strings.HasPrefix(line, "#"):
			return fail("unexpected comment")
		default:
			name, rest, err := parseName(line, true)
			if err != nil {
				return fail("%v", err)
			}
			labels, rest, err := parseLabels(rest)
			if err != nil {
				return fail("%v", err)
			}
			if !strings.HasPrefix(rest, " ") {
				return fail("missing space before value")
			}
			v, err := parseValue(rest[1:])
			if err != nil {
				return fail("%v", err)
			}
			_, f := familyOf(e, name)
			if f == nil || f.typ == "" {
				return fail("sample without HELP/TYPE")
			}
			key := seriesKey(name, labels)
			if _, dup := seen[key]; dup {
				return fail("duplicate series")
			}
			seen[key] = struct{}{}
			if (f.typ == "counter" || strings.HasSuffix(name, "_count")) && (v < 0 || math.IsNaN(v)) {
				return fail("negative or NaN counter")
			}
			f.samples = append(f.samples, sample{name: name, labels: labels, value: v})
		}
	}
	for _, name := range e.order {
		if f := e.families[name]; f.typ == "histogram" {
			if err := checkHistogram(name, f); err != nil {
				return nil, err
			}
		}
	}
	return e, nil
}

func checkHistogram(name string, f *parsedFamily) error {
	type hist struct {
		lastLE   float64
		lastCum  float64
		started  bool
		inf      float64
		hasInf   bool
		count    float64
		hasCount bool
		hasSum   bool
	}
	groups := map[string]*hist{}
	get := func(s sample) *hist {
		l := map[string]string{}
		for k, v := range s.labels {
			if k != "le" {
				l[k] = v
			}
		}
		k := seriesKey("", l)
		if groups[k] == nil {
			groups[k] = &hist{}
		}
		return groups[k]
	}
	for _, s := range f.samples {
		h := get(s)
		switch s.name {
		case name + "_bucket":
			ls, ok := s.labels["le"]
			if !ok {
				return fmt.Errorf("%s: bucket without le", name)
			}
			le, err := parseValue(ls)
			if err != nil {
				return err
			}
			if h.hasInf {
				return fmt.Errorf("%s: bucket after +Inf", name)
			}
			if h.started && le <= h.lastLE {
				return fmt.Errorf("%s: le not increasing", name)
			}
			if s.value < h.lastCum {
				return fmt.Errorf("%s: buckets not cumulative", name)
			}
			h.started, h.lastLE, h.lastCum = true, le, s.value
			if math.IsInf(le, 1) {
				h.hasInf, h.inf = true, s.value
			}
		case name + "_sum":
			h.hasSum = true
		case name + "_count":
			h.hasCount, h.count = true, s.value
		default:
			return fmt.Errorf("%s: unexpected sample %s", name, s.name)
		}
	}
	for k, h := range groups {
		if !h.hasInf || !h.hasCount || !h.hasSum {
			return fmt.Errorf("%s{%s}: incomplete histogram", name, k)
		}
		if h.inf != h.count {
			return fmt.Errorf("%s{%s}: +Inf bucket %v != count %v", name, k, h.inf, h.count)
		}
	}
	return nil
}
