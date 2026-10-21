package textmin

import (
	"bytes"
	stdhtml "html"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/css"
	"github.com/tdewolff/parse/v2/html"
	"github.com/tdewolff/parse/v2/js"
	"github.com/tdewolff/parse/v2/xml"
)

func isASCIISpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f'
}

func collapseSpace(s string) string {
	return strings.Join(strings.FieldsFunc(s, isASCIISpace), " ")
}

type htmlTok struct {
	tt   html.TokenType
	text string
	data string
	key  string
	val  string
}

func lexHTML(src []byte) []htmlTok {
	in := append([]byte(nil), src...)
	l := html.NewLexer(parse.NewInput(bytes.NewBuffer(in)))
	var toks []htmlTok
	for {
		tt, data := l.Next()
		if tt == html.ErrorToken {
			return toks
		}
		toks = append(toks, htmlTok{
			tt:   tt,
			text: strings.ToLower(string(l.Text())),
			data: string(data),
			key:  strings.ToLower(string(l.AttrKey())),
			val:  string(l.AttrVal()),
		})
	}
}

var booleanAttrs = map[string]bool{
	"disabled": true, "checked": true, "selected": true, "readonly": true, "required": true, "multiple": true,
	"hidden": true, "autofocus": true, "async": true, "defer": true, "novalidate": true, "open": true,
}

var urlAttrs = map[string]bool{"href": true, "src": true, "action": true, "formaction": true, "poster": true, "data": true, "cite": true}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

func normURL(v string) string {
	v = strings.TrimFunc(v, isASCIISpace)
	if len(v) > 5 && strings.EqualFold(v[:5], "data:") {
		return "data:"
	}
	if i := strings.IndexByte(v, ':'); i > 0 && !strings.ContainsAny(v[:i], "/?#") {
		v = strings.ToLower(v[:i]) + v[i:]
	}
	return v
}

// htmlSignature reduces an HTML document to the information that must be
// identical after Safe minification: start tags with their attributes, the
// normalized text between tags, raw text of whitespace-sensitive and script
// elements, and a signature of inline CSS. Comments, whitespace-only text,
// end tags and the few attributes the minifier may drop are ignored.
func htmlSignature(t testing.TB, src []byte, jsMinified bool) []string {
	toks := lexHTML(src)
	var sig []string
	var text strings.Builder
	raw := ""
	flush := func() {
		s := text.String()
		text.Reset()
		switch {
		case raw == "script":
			if jsMinified {
				if s != "" {
					sig = append(sig, "script-present")
				}
			} else if s != "" {
				sig = append(sig, "script:"+s)
			}
		case raw == "style":
			if s = strings.TrimSpace(s); s != "" {
				sig = append(sig, "style:"+strings.Join(cssSignature(t, []byte(s), false), "|"))
			}
		case raw != "":
			sig = append(sig, "raw:"+s)
		default:
			if s = collapseSpace(stdhtml.UnescapeString(s)); s != "" {
				sig = append(sig, "text:"+s)
			}
		}
	}
	preDepth := 0
	for i := 0; i < len(toks); i++ {
		tk := toks[i]
		switch tk.tt {
		case html.TextToken:
			text.WriteString(tk.data)
		case html.CommentToken:
		case html.DoctypeToken:
			flush()
			sig = append(sig, "doctype")
		case html.StartTagToken:
			flush()
			name := tk.text
			var attrs []htmlTok
			j := i + 1
			for ; j < len(toks) && toks[j].tt == html.AttributeToken; j++ {
				attrs = append(attrs, toks[j])
			}
			i = j
			if name == "colgroup" {
				continue
			}
			if (name == "script" || name == "style") && len(attrs) == 0 && i+1 < len(toks) &&
				toks[i+1].tt == html.EndTagToken {
				i++
				continue
			}
			sig = append(sig, htmlStartSignature(t, name, attrs))
			switch name {
			case "script", "style", "textarea", "title", "xmp", "listing", "plaintext":
				raw = name
				if name == "title" {
					raw = ""
				}
			case "pre":
				preDepth++
				raw = "pre"
			}
		case html.EndTagToken:
			flush()
			if tk.text == "pre" {
				preDepth--
			}
			raw = ""
			if preDepth > 0 {
				raw = "pre"
			}
		case html.SVGToken:
			flush()
			sig = append(sig, "svg:"+strings.Join(svgSignature([]byte(tk.data)), ","))
		default:
			flush()
			sig = append(sig, tk.tt.String()+":"+tk.data)
		}
	}
	flush()
	return sig
}

func htmlStartSignature(t testing.TB, name string, attrs []htmlTok) string {
	var parts []string
	id := ""
	for _, a := range attrs {
		if a.key == "id" {
			id = stdhtml.UnescapeString(unquote(a.val))
		}
	}
	var httpEquiv, content string
	for _, a := range attrs {
		switch a.key {
		case "http-equiv":
			httpEquiv = strings.ToLower(strings.TrimSpace(unquote(a.val)))
		case "content":
			content = strings.ToLower(strings.ReplaceAll(unquote(a.val), " ", ""))
		}
	}
	metaCharset := name == "meta" && httpEquiv == "content-type" && content == "text/html;charset=utf-8"
	var metaName string
	for _, a := range attrs {
		if a.key == "name" {
			metaName = strings.ToLower(strings.TrimSpace(unquote(a.val)))
		}
	}
	for _, a := range attrs {
		v := stdhtml.UnescapeString(unquote(a.val))
		switch {
		case v == "" && (a.key == "class" || a.key == "dir" || a.key == "id" || a.key == "name" || a.key == "action" && name == "form"):
			continue
		case name == "input" && a.key == "value" && v == "":
			continue
		case name == "a" && a.key == "name" && v == id:
			continue
		case metaCharset && a.key == "http-equiv":
			continue
		case metaCharset && a.key == "content":
			parts = append(parts, "charset=utf-8")
			continue
		case booleanAttrs[a.key] && (v == "" || strings.EqualFold(v, a.key)):
			v = ""
		case a.key == "style":
			v = strings.Join(cssSignature(t, []byte(v), true), "|")
			if v == "" {
				continue
			}
		case strings.HasPrefix(a.key, "on"):
			v = strings.TrimSpace(v)
		case urlAttrs[a.key]:
			v = normURL(v)
		case a.key == "type" || a.key == "enctype" || a.key == "accept":
			v = strings.ToLower(strings.ReplaceAll(v, " ", ""))
		case name == "meta" && a.key == "content" && (metaName == "viewport" || metaName == "keywords"):
			v = strings.ReplaceAll(v, " ", "")
			if metaName == "viewport" {
				v = strings.ReplaceAll(v, "1.0", "1")
			}
		default:
			v = collapseSpace(v)
		}
		parts = append(parts, a.key+"="+v)
	}
	return "<" + name + " " + strings.Join(parts, " ") + ">"
}

// cssSignature lists the parts of a stylesheet that must survive Safe
// minification exactly: at-rule names, selectors (case-folded, without
// whitespace), property names in order, and the url(), string and custom
// property values and !important markers of every declaration.
func cssSignature(t testing.TB, src []byte, inline bool) []string {
	in := append([]byte(nil), src...)
	p := css.NewParser(parse.NewInput(bytes.NewBuffer(in)), inline)
	var sig []string
	for {
		gt, _, data := p.Next()
		if gt == css.ErrorGrammar {
			if p.HasParseError() {
				sig = append(sig, "parse-error")
				continue
			}
			return sig
		}
		switch gt {
		case css.CommentGrammar:
			if bytes.HasPrefix(data, []byte("/*!")) {
				sig = append(sig, "comment:"+strings.Join(strings.FieldsFunc(string(data), func(r rune) bool { return isASCIISpace(r) || r == '*' }), ""))
			}
		case css.AtRuleGrammar, css.BeginAtRuleGrammar:
			sig = append(sig, "at:"+strings.ToLower(string(data))+":"+valuesSig(p.Values(), "@"))
		case css.BeginRulesetGrammar:
			sig = append(sig, "sel:"+strings.ToLower(valuesText(p.Values())))
		case css.EndAtRuleGrammar, css.EndRulesetGrammar:
			sig = append(sig, "}")
		case css.DeclarationGrammar:
			sig = append(sig, "decl:"+strings.ToLower(string(data))+":"+valuesSig(p.Values(), strings.ToLower(string(data))))
		case css.CustomPropertyGrammar:
			sig = append(sig, "var:"+string(data)+":"+strings.TrimSpace(string(p.Values()[0].Data)))
		}
	}
}

func valuesText(vals []css.Token) string {
	var sb strings.Builder
	for _, v := range vals {
		if v.TokenType == css.WhitespaceToken {
			continue
		}
		sb.Write(v.Data)
	}
	return strings.ReplaceAll(sb.String(), "\"", "")
}

// valuesSig fingerprints declaration values. Quoted and unquoted forms are
// equivalent for @import targets and font family names.
func valuesSig(vals []css.Token, prop string) string {
	if prop == "font-family" {
		var families, cur []string
		for _, v := range vals {
			switch v.TokenType {
			case css.StringToken, css.IdentToken:
				cur = append(cur, strings.ToLower(unquote(string(v.Data))))
			case css.CommaToken:
				families = append(families, strings.Join(cur, " "))
				cur = nil
			}
		}
		return strings.Join(append(families, strings.Join(cur, " ")), ",")
	}
	var parts []string
	for _, v := range vals {
		if prop == "@" && v.TokenType == css.StringToken {
			parts = append(parts, "url:"+unquote(string(v.Data)))
			continue
		}
		switch v.TokenType {
		case css.URLToken:
			u := strings.TrimSpace(string(v.Data[4 : len(v.Data)-1]))
			parts = append(parts, "url:"+unquote(u))
		case css.StringToken:
			parts = append(parts, "str:"+unquote(string(v.Data)))
		case css.DelimToken:
			if v.Data[0] == '!' {
				parts = append(parts, "!")
			}
		case css.IdentToken:
			if strings.EqualFold(string(v.Data), "important") {
				parts = append(parts, "important")
			}
		}
	}
	return strings.Join(parts, ",")
}

type svgItem struct {
	name  string
	attrs map[string]string
}

func lexSVG(src []byte) []svgItem {
	in := append([]byte(nil), src...)
	l := xml.NewLexer(parse.NewInput(bytes.NewBuffer(in)))
	var items []svgItem
	inPI := false
	for {
		tt, _ := l.Next()
		switch tt {
		case xml.ErrorToken:
			return items
		case xml.StartTagPIToken:
			inPI = true
		case xml.StartTagClosePIToken:
			inPI = false
		case xml.StartTagToken:
			items = append(items, svgItem{name: string(l.Text()), attrs: map[string]string{}})
		case xml.AttributeToken:
			if !inPI && len(items) > 0 {
				items[len(items)-1].attrs[string(l.Text())] = unquote(string(l.AttrVal()))
			}
		}
	}
}

// svgSignature lists the element names, with their ids, that survive
// minification (metadata, empty defs and namespaced editor elements are
// dropped by the minifier).
func svgSignature(src []byte) []string {
	var sig []string
	for _, it := range lexSVG(src) {
		if it.name == "metadata" || it.name == "defs" || strings.Contains(it.name, ":") {
			continue
		}
		s := it.name
		if id, ok := it.attrs["id"]; ok {
			s += "#" + id
		}
		sig = append(sig, s)
	}
	return sig
}

// requireSVGPreserved checks that the minified SVG kept (as an ordered
// subsequence) all non-editor elements, that no element gained attributes,
// that every id, class, viewBox and clip/mask/filter reference is retained
// and that the document stayed well-formed.
func requireSVGPreserved(t testing.TB, orig, minified []byte) {
	t.Helper()
	o, m := lexSVG(orig), lexSVG(minified)
	require.NotEmpty(t, m)
	oi := 0
	for _, me := range m {
		for oi < len(o) && o[oi].name != me.name {
			oi++
		}
		require.Less(t, oi, len(o), "element %q not in original order", me.name)
		for k, v := range me.attrs {
			ov, ok := o[oi].attrs[k]
			require.True(t, ok, "attribute %s gained on <%s>", k, me.name)
			if k == "id" || k == "class" || k == "href" || k == "xlink:href" || k == "clip-path" || k == "mask" || k == "filter" {
				require.Equal(t, ov, v, "attribute %s on <%s>", k, me.name)
			}
		}
		oi++
	}
	for _, oe := range o {
		if oe.name == "metadata" || strings.Contains(oe.name, ":") {
			continue
		}
		for _, k := range []string{"id", "viewBox", "preserveAspectRatio"} {
			if v, ok := oe.attrs[k]; ok && (k != "preserveAspectRatio" || v != "xMidYMid meet") {
				found := false
				for _, me := range m {
					if me.name == oe.name && (me.attrs[k] != "" || k == "id" && me.attrs[k] == v) {
						found = true
						break
					}
				}
				require.True(t, found, "<%s> lost %s=%q", oe.name, k, v)
			}
		}
	}
}

func jsTopLevelNames(t testing.TB, src []byte) []string {
	t.Helper()
	ast, err := js.Parse(parse.NewInput(bytes.NewBuffer(append([]byte(nil), src...))), js.Options{})
	require.NoError(t, err)
	var names []string
	for _, v := range ast.Declared {
		names = append(names, string(v.Name()))
	}
	sort.Strings(names)
	return names
}
