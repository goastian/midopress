package textmin

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCSSSafeGolden(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SkipMinified = false
	m := New(cfg)
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "charset, license comment and plain comment",
			in:   "@charset \"UTF-8\";\n/*! Lib v1 */\n/* drop me */\na { color : red ; }\n",
			want: "@charset \"UTF-8\";/*!Lib v1*/a{color:red}",
		},
		{
			name: "vendor prefixes, fallbacks and hacks keep their order",
			in:   "a{display:-webkit-box;display:-ms-flexbox;display:flex;-webkit-transition:opacity .15s;transition:opacity .15s linear;*zoom:1;_height:1px;filter:alpha(opacity=50)}",
			want: "a{display:-webkit-box;display:-ms-flexbox;display:flex;-webkit-transition:opacity .15s;transition:opacity .15s linear;*zoom:1;_height:1px;filter:alpha(opacity=50)}",
		},
		{
			name: "duplicate declarations are not merged",
			in:   "a{color:red;color:var(--x);color:blue}",
			want: "a{color:red;color:var(--x);color:blue}",
		},
		{
			name: "custom properties, strings and urls",
			in:   ":root{--a: 1px ;--b:{c:d};--c:  \"x  y\"}\n.a::before{content:\"a  b\\f101\"}b{background:url( \"a b.png\" ) ,url('c.png');}",
			want: ":root{--a:1px;--b:{c:d};--c:\"x  y\"}.a::before{content:\"a  b\\f101\"}b{background:url(\"a b.png\"),url(c.png)}",
		},
		{
			name: "numbers are never rounded",
			in:   "a{width:12.3456789012px;margin:0.50em 0.0px;line-height:1.0;opacity:.123456789;transform:rotate(12.3456789deg)}",
			want: "a{width:12.3456789012px;margin:.5em 0px;line-height:1;opacity:.123456789;transform:rotate(12.3456789deg)}",
		},
		{
			name: "important",
			in:   "a{color:red ! important;margin:0 0 0 0!important}",
			want: "a{color:red!important;margin:0!important}",
		},
		{
			name: "font shorthand and family",
			in:   "a{font-family:\"Open Sans\",Arial;font-weight:bold;font:italic bold 12px/30px Georgia,serif}",
			want: "a{font-family:open sans,Arial;font-weight:700;font:italic 700 12px/30px Georgia,serif}",
		},
		{
			name: "media queries, supports, import and font-face",
			in:   "@import url(x.css);@media screen and (min-width:100px){a:hover{color:RED}}@supports (display:grid) and (not (display:inline-grid)){a{b:c}}@font-face{font-family:X;src:url(a.woff2) format(\"woff2\"),url(a.woff) format(\"woff\")}",
			want: "@import \"x.css\";@media screen and (min-width:100px){a:hover{color:RED}}@supports(display:grid) and (not (display:inline-grid)){a{b:c}}@font-face{font-family:X;src:url(a.woff2)format(\"woff2\"),url(a.woff)format(\"woff\")}",
		},
		{
			name: "selectors, attribute selectors and combinators",
			in:   "A:Hover > .Foo ~ #Bar + b , a[href^=\"http\"],a[target=_blank]{x:y}",
			want: "a:hover>.Foo~#Bar+b,a[href^=http],a[target=_blank]{x:y}",
		},
		{
			name: "calc keeps its required spaces and gradients keep their stops",
			in:   "a{width:calc( 100% - 10px );background:linear-gradient(to right,rgba(255,255,255,0) 0,#fff 100%)}",
			want: "a{width:calc( 100% - 10px );background:linear-gradient(to right,rgba(255,255,255,0) 0,#fff 100%)}",
		},
		{
			name: "CRLF and unicode",
			in:   "a::after{\r\n  content : \"\u00e9\u4e16\" ;\r\n}\r\n",
			want: "a::after{content:\"\u00e9\u4e16\"}",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := minifyString(t, m, "text/css", tc.in)
			assert.Equal(t, tc.want, got)
			requireSafeEqual(t, kindCSS, []byte(tc.in), []byte(got), false)
		})
	}
}

func TestCSSRiskyInputsAreKeptInSafeMode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SkipMinified = false
	safe := New(cfg)
	aggressive := New(newConfig(Off, Aggressive, Off, Off, Off))
	tests := []struct {
		name           string
		in             string
		aggressiveWant string
	}{
		{"quoted generic family", "a{font-family:\"serif\",Arial}", "a{font-family:serif,Arial}"},
		{"quoted generic family, single quotes", "a{font-family: 'Sans-Serif'}", "a{font-family:sans-serif}"},
		{"part pseudo-element", "x-y::part(Foo)  {  color : red }", "x-y::part(foo){color:red}"},
		{"custom highlight", "::highlight(Hi)  {  color : red }", "::highlight(hi){color:red}"},
		{"namespace", "@namespace svg url(http://www.w3.org/2000/svg);svg|Rect  {  fill : red }", "@namespace svg url(http://www.w3.org/2000/svg);svg|rect{fill:red}"},
		{"camelCase SVG element", "linearGradient  {  x : y }", "lineargradient{x:y}"},
		{"foreignObject selector", "foreignObject  {  x : y }", "foreignobject{x:y}"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.in, minifyString(t, safe, "text/css", tc.in))
			assert.Equal(t, tc.aggressiveWant, minifyString(t, aggressive, "text/css", tc.in))
		})
	}
}

func TestCSSGenericFamilyWordsInOtherPlacesDoNotMatterWhenUnquoted(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SkipMinified = false
	m := New(cfg)
	got := minifyString(t, m, "text/css", "a{font-family:Georgia,serif}  b{content:\"x\"}")
	assert.Equal(t, "a{font-family:Georgia,serif}b{content:\"x\"}", got)
}

func TestCSSSignatureDetectsDamage(t *testing.T) {
	base := cssSignature(t, []byte("a{color:red;background:url(x.png);content:\"a\"}"), false)
	assert.Equal(t, base, cssSignature(t, []byte("a { color : #f00 ; background : url( \"x.png\" ) ; content : 'a' }"), false))
	assert.NotEqual(t, base, cssSignature(t, []byte("a{background:url(y.png);color:red;content:\"a\"}"), false))
	assert.NotEqual(t, base, cssSignature(t, []byte("a{color:red;background:url(x.png)}"), false))
	assert.NotEqual(t, base, cssSignature(t, []byte("a{color:red;background:url(x.png);content:\"b\"}"), false))
	assert.NotEqual(t, base, cssSignature(t, []byte("b{color:red;background:url(x.png);content:\"a\"}"), false))
}
