package textmin

import (
	"bytes"
	"regexp"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/xml"
)

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

func lower(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// hasPrefixFold reports whether b starts with the lowercase ASCII string p,
// ignoring ASCII case.
func hasPrefixFold(b []byte, p string) bool {
	if len(b) < len(p) {
		return false
	}
	for i := 0; i < len(p); i++ {
		if lower(b[i]) != p[i] {
			return false
		}
	}
	return true
}

// tagEnds reports whether b (the bytes after a tag name prefix) ends the name.
func tagEnds(b []byte) bool {
	return len(b) == 0 || isSpace(b[0]) || b[0] == '>' || b[0] == '/'
}

// hasTemplateMarkers reports whether src contains server-side or
// client-side template syntax that the HTML minifier could corrupt. The
// ${ marker is ignored inside <script> and <style>, where it is JavaScript or
// CSS syntax.
func hasTemplateMarkers(src []byte) bool {
	raw := false
	for i := 0; i < len(src); i++ {
		switch src[i] {
		case '{':
			if i+1 < len(src) && (src[i+1] == '{' || src[i+1] == '%') {
				return true
			}
		case '$':
			if !raw && i+1 < len(src) && src[i+1] == '{' {
				return true
			}
		case '<':
			rest := src[i+1:]
			if len(rest) == 0 {
				continue
			}
			switch lower(rest[0]) {
			case '%':
				return true
			case '?':
				if len(rest) > 1 && (rest[1] == '=' || isSpace(rest[1]) || hasPrefixFold(rest[1:], "php")) {
					return true
				}
			case '/':
				if raw && (hasPrefixFold(rest[1:], "script") || hasPrefixFold(rest[1:], "style")) {
					raw = false
				}
			case 's':
				if hasPrefixFold(rest, "script") && tagEnds(rest[6:]) || hasPrefixFold(rest, "style") && tagEnds(rest[5:]) {
					raw = true
				}
			}
		}
	}
	return false
}

// htmlRisky reports constructs for which Safe HTML minification could change
// rendering.
func htmlRisky(src []byte) bool {
	for i := 0; i < len(src); i++ {
		if src[i] != '<' && src[i] != 'w' && src[i] != 'W' {
			continue
		}
		rest := src[i:]
		switch {
		case hasPrefixFold(rest, "<!doctype"):
			if !simpleDoctype(rest[len("<!doctype"):]) {
				return true
			}
		case hasPrefixFold(rest, "<xmp") && tagEnds(rest[4:]),
			hasPrefixFold(rest, "<listing") && tagEnds(rest[8:]),
			hasPrefixFold(rest, "<plaintext") && tagEnds(rest[10:]):
			return true
		case hasPrefixFold(rest, "white-space") && preservesWhitespace(rest[len("white-space"):]):
			return true
		}
	}
	return false
}

// simpleDoctype reports whether the doctype body (after "<!doctype") is the
// HTML5 doctype or the legacy-compat form, both of which select standards
// mode exactly like the <!doctype html> the minifier writes.
func simpleDoctype(b []byte) bool {
	end := bytes.IndexByte(b, '>')
	if end < 0 {
		return false
	}
	body := bytes.Join(bytes.Fields(b[:end]), []byte{' '})
	return bytes.EqualFold(body, []byte("html")) ||
		bytes.EqualFold(body, []byte(`html system "about:legacy-compat"`)) ||
		bytes.EqualFold(body, []byte(`html system 'about:legacy-compat'`))
}

func preservesWhitespace(b []byte) bool {
	i := 0
	for i < len(b) && isSpace(b[i]) {
		i++
	}
	if i >= len(b) || b[i] != ':' {
		return false
	}
	i++
	for i < len(b) && isSpace(b[i]) {
		i++
	}
	return hasPrefixFold(b[i:], "pre") || hasPrefixFold(b[i:], "break-spaces")
}

var genericFamilies = map[string]bool{
	"serif": true, "sans-serif": true, "monospace": true, "cursive": true, "fantasy": true,
	"system-ui": true, "ui-serif": true, "ui-sans-serif": true, "ui-monospace": true,
	"ui-rounded": true, "math": true, "emoji": true, "fangsong": true,
}

var caseSensitiveSelectors = []string{
	"::part(", "::highlight(", ":state(", "::view-transition", "@namespace",
}

var svgElementNames = [][]byte{
	[]byte("foreignObject"), []byte("linearGradient"), []byte("radialGradient"), []byte("clipPath"),
	[]byte("textPath"), []byte("animateMotion"), []byte("animateTransform"), []byte("feBlend"),
	[]byte("feColorMatrix"), []byte("feComponentTransfer"), []byte("feComposite"), []byte("feConvolveMatrix"),
	[]byte("feDiffuseLighting"), []byte("feDisplacementMap"), []byte("feDistantLight"), []byte("feDropShadow"),
	[]byte("feFlood"), []byte("feGaussianBlur"), []byte("feImage"), []byte("feMerge"), []byte("feMorphology"),
	[]byte("feOffset"), []byte("fePointLight"), []byte("feSpecularLighting"), []byte("feSpotLight"),
	[]byte("feTile"), []byte("feTurbulence"),
}

// cssRisky reports constructs that the CSS minifier would change in a way
// that is not equivalent: it lowercases type selectors and pseudo-class
// arguments (case-sensitive for custom highlights, parts and SVG elements)
// and unquotes quoted generic font family names, which turns a family named
// "serif" into the generic keyword.
func cssRisky(src []byte) bool {
	var word [16]byte
	for i := 0; i < len(src); i++ {
		switch c := src[i]; c {
		case '"', '\'':
			end := i + 1
			for end < len(src) && end-i <= 14 && src[end] != c {
				end++
			}
			if end < len(src) && src[end] == c && end-i > 1 {
				n := copy(word[:], src[i+1:end])
				for j := range n {
					word[j] = lower(word[j])
				}
				if genericFamilies[string(word[:n])] {
					return true
				}
			}
		case ':', '@':
			for _, p := range caseSensitiveSelectors {
				if hasPrefixFold(src[i:], p) {
					return true
				}
			}
		}
	}
	for _, n := range svgElementNames {
		if bytes.Contains(src, n) {
			return true
		}
	}
	return false
}

var xmlnsPrefix = regexp.MustCompile(`xmlns:([A-Za-z_][\w.-]*)\s*=`)

var editorNamespaces = map[string]bool{
	"inkscape": true, "sodipodi": true, "dc": true, "cc": true, "rdf": true,
	"xlink": true, "svg": true, "xml": true, "xmlns": true,
}

// svgKeepNamespaces returns the namespace prefixes to keep, in addition to
// xlink: xml and every prefix declared in src except the ones used by editor
// and RDF metadata (Inkscape, Sodipodi, Dublin Core, Creative Commons, RDF).
func svgKeepNamespaces(src []byte) []string {
	keep := []string{"xml"}
	for _, match := range xmlnsPrefix.FindAllSubmatch(src, -1) {
		p := string(match[1])
		if !editorNamespaces[p] {
			keep = append(keep, p)
		}
	}
	return keep
}

// svgRisky reports SVG documents whose whitespace-sensitive or
// script-bearing content the SVG minifier would damage: it collapses
// whitespace in <script> text (breaking // comments and string literals),
// trims text around <tspan>, removes xml:space="preserve", xml-stylesheet
// instructions and the XML declaration (which may carry a non-UTF-8
// encoding), and strips the svg: prefix from start tags only.
func svgRisky(src []byte) bool {
	for _, p := range [][]byte{
		[]byte("<script"), []byte("<tspan"), []byte("<textPath"), []byte("<tref"), []byte("<altGlyph"),
		[]byte("xml:space"), []byte("<?xml-stylesheet"), []byte("<svg:"),
	} {
		if bytes.Contains(src, p) {
			return true
		}
	}
	if nonUTF8Declaration(src) {
		return true
	}
	for i := 0; i+11 < len(src); i++ {
		if (src[i] == 'w' || src[i] == 'W') && hasPrefixFold(src[i:], "white-space") && preservesWhitespace(src[i+11:]) {
			return true
		}
	}
	return false
}

// svgWellFormed reports whether the start and end tags of src are balanced.
// SVG embedded in HTML does not have to be well-formed XML (for example an
// unclosed <path>), and the XML-based SVG minifier would then move the
// following markup inside the element.
func svgWellFormed(src []byte) bool {
	l := xml.NewLexer(parse.NewInput(bytes.NewReader(src)))
	var stack [][]byte
	for {
		tt, _ := l.Next()
		switch tt {
		case xml.ErrorToken:
			return len(stack) == 0
		case xml.StartTagToken:
			stack = append(stack, append([]byte(nil), l.Text()...))
		case xml.StartTagCloseVoidToken:
			if len(stack) == 0 {
				return false
			}
			stack = stack[:len(stack)-1]
		case xml.EndTagToken:
			if len(stack) == 0 || !bytes.Equal(stack[len(stack)-1], l.Text()) {
				return false
			}
			stack = stack[:len(stack)-1]
		}
	}
}

// nonUTF8Declaration reports whether src starts with an XML declaration that
// names an encoding other than UTF-8; the SVG minifier drops the declaration.
func nonUTF8Declaration(src []byte) bool {
	src = bytes.TrimLeft(src, "\xef\xbb\xbf \t\r\n")
	if !hasPrefixFold(src, "<?xml") {
		return false
	}
	end := bytes.Index(src, []byte("?>"))
	if end < 0 {
		return true
	}
	decl := src[:end]
	i := bytes.Index(decl, []byte("encoding"))
	if i < 0 {
		return false
	}
	rest := bytes.TrimLeft(decl[i+len("encoding"):], " \t\r\n")
	if len(rest) == 0 || rest[0] != '=' {
		return true
	}
	rest = bytes.TrimLeft(rest[1:], " \t\r\n")
	if len(rest) < 2 || rest[0] != '"' && rest[0] != '\'' {
		return true
	}
	val := rest[1:]
	if q := bytes.IndexByte(val, rest[0]); q >= 0 {
		val = val[:q]
	}
	return !bytes.EqualFold(val, []byte("utf-8"))
}
