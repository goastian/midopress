// Package negotiate parses the Accept and Accept-Encoding request headers.
// Both parsers are allocation free because they run for every response.
package negotiate

// Encodings is a set of content codings a client declared it can decode.
type Encodings uint8

const (
	Brotli Encodings = 1 << iota
	Zstd
	Gzip
	Deflate
)

// Has reports whether every coding in e2 is present in e.
func (e Encodings) Has(e2 Encodings) bool { return e&e2 == e2 }

// ParseAcceptEncoding returns the codings explicitly listed with a non-zero
// quality. A wildcard does not enable anything: advertising "*" does not prove
// that a client can handle a coding we would pick.
func ParseAcceptEncoding(header string) Encodings {
	var set Encodings
	for header != "" {
		var item string
		item, header = nextItem(header)
		name, q := splitQuality(item)
		if q == 0 {
			continue
		}
		switch {
		case equalFold(name, "br"):
			set |= Brotli
		case equalFold(name, "zstd"):
			set |= Zstd
		case equalFold(name, "gzip"), equalFold(name, "x-gzip"):
			set |= Gzip
		case equalFold(name, "deflate"):
			set |= Deflate
		}
	}
	return set
}

// AcceptsMedia reports whether header explicitly lists mediaType with a
// non-zero quality. Wildcards such as image/* or */* do not count, for the
// same reason as in ParseAcceptEncoding.
func AcceptsMedia(header, mediaType string) bool {
	for header != "" {
		var item string
		item, header = nextItem(header)
		name, q := splitQuality(item)
		if q != 0 && equalFold(name, mediaType) {
			return true
		}
	}
	return false
}

func nextItem(s string) (item, rest string) {
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			return trim(s[:i]), s[i+1:]
		}
	}
	return trim(s), ""
}

// splitQuality splits "name;q=0.5" and returns the name and the quality scaled
// to 0..1000. A missing or malformed q parameter counts as 1000.
func splitQuality(item string) (name string, q int) {
	semi := -1
	for i := 0; i < len(item); i++ {
		if item[i] == ';' {
			semi = i
			break
		}
	}
	if semi < 0 {
		return item, 1000
	}
	name = trim(item[:semi])
	params := item[semi+1:]
	for params != "" {
		var p string
		for i := 0; i < len(params); i++ {
			if params[i] == ';' {
				p, params = params[:i], params[i+1:]
				break
			}
		}
		if p == "" {
			p, params = params, ""
		}
		p = trim(p)
		if len(p) >= 2 && (p[0] == 'q' || p[0] == 'Q') {
			rest := trim(p[1:])
			if rest != "" && rest[0] == '=' {
				return name, parseQuality(trim(rest[1:]))
			}
		}
	}
	return name, 1000
}

func parseQuality(s string) int {
	if s == "" {
		return 1000
	}
	whole := 0
	i := 0
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		whole = whole*10 + int(s[i]-'0')
		if whole > 1 {
			return 1000
		}
	}
	if i == 0 {
		return 1000
	}
	frac, scale := 0, 100
	if i < len(s) && s[i] == '.' {
		for i++; i < len(s) && s[i] >= '0' && s[i] <= '9' && scale > 0; i++ {
			frac += int(s[i]-'0') * scale
			scale /= 10
		}
	}
	return whole*1000 + frac
}

func trim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if x >= 'A' && x <= 'Z' {
			x += 'a' - 'A'
		}
		if y >= 'A' && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
