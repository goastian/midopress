package textmin

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTMLSafeGolden(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SkipMinified = false
	m := New(cfg)
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "whitespace, comments and CRLF",
			in:   "<!DOCTYPE html>\r\n<html>\r\n<head>\r\n  <!-- c -->\r\n  <title>  T  </title>\r\n</head>\r\n<body>\r\n  <p>  a   b  </p>\r\n  <b>x</b> <i>y</i>\r\n</body>\r\n</html>\r\n",
			want: "<!doctype html><html>\n<head>\n<title> T </title>\n</head>\n<body>\n<p> a b </p>\n<b>x</b> <i>y</i>\n</body>\n</html>",
		},
		{
			name: "pre and textarea are untouched",
			in:   "<body><pre>\n  a   b\n</pre><textarea>\n  x   y\n</textarea><div>  q   r  </div></body>",
			want: "<body><pre>\n  a   b\n</pre><textarea>\n  x   y\n</textarea><div> q r </div></body>",
		},
		{
			name: "conditional comments and SSI are kept, other comments dropped",
			in:   "<!--[if lt IE 9]><script src=\"h.js\"></script><![endif]--><!--#include virtual=\"/x\" --><!-- drop --><p>x</p>",
			want: "<!--[if lt IE 9]><script src=\"h.js\"></script><![endif]--><!--#include virtual=\"/x\" --><p>x</p>",
		},
		{
			name: "inline script is untouched when JS is off, style and style attributes are minified",
			in:   "<script>var a  =  1; // c\n  foo( a );</script><style>a  {  color : red ; }</style><div style=\"color : red ;\" onclick=\"foo( 1 )\">x</div>",
			want: "<script>var a  =  1; // c\n  foo( a );</script><style>a{color:red}</style><div style=\"color:red\" onclick=\"foo( 1 )\">x</div>",
		},
		{
			name: "JSON and module scripts are untouched",
			in:   "<script type=\"application/json\">{ \"a\" : 1 }</script><script type=\"application/ld+json\">{ \"a\" : [ 1 ] }</script><script type=\"module\">import  x from  './a.js' ;</script>",
			want: "<script type=\"application/json\">{ \"a\" : 1 }</script><script type=\"application/ld+json\">{ \"a\" : [ 1 ] }</script><script type=\"module\">import  x from  './a.js' ;</script>",
		},
		{
			name: "quotes, default attribute values and empty attributes",
			in:   "<a href=\"x y\" class='a   b' data-x=1 id=\"\">t</a><input type=\"text\" value=\"\"><script type=\"text/javascript\" src=a.js></script><form method=\"get\"><button type=\"submit\">s</button></form>",
			want: "<a href=\"x y\" class='a b' data-x=1>t</a><input type=\"text\"><script type=\"text/javascript\" src=a.js></script><form method=\"get\"><button type=\"submit\">s</button></form>",
		},
		{
			name: "document tags and end tags are kept",
			in:   "<html><body><ul><li>a</li><li>b</li></ul><p>p1</p><p>p2</p><table><tr><td>1</td></tr></table></body></html>",
			want: "<html><body><ul><li>a</li><li>b</li></ul><p>p1</p><p>p2</p><table><tr><td>1</td></tr></table></body></html>",
		},
		{
			name: "whitespace between inline elements is kept",
			in:   "<p><b>a</b>   <i>b</i>\n<u>c</u></p>",
			want: "<p><b>a</b> <i>b</i>\n<u>c</u></p>",
		},
		{
			name: "well-formed inline SVG is minified",
			in:   "<svg viewBox=\"0 0 1 1\"><path d=\"M 0 0 L 1.50 1\"/></svg><p>after</p>",
			want: "<svg viewBox=\"0 0 1 1\"><path d=\"M0 0 1.5 1\"/></svg><p>after</p>",
		},
		{
			name: "ill-formed inline SVG is untouched",
			in:   "<svg><path d=\"M 0 0\"></svg><p>after</p>",
			want: "<svg><path d=\"M 0 0\"></svg><p>after</p>",
		},
		{
			name: "unicode and entities",
			in:   "<p>h\u00e9llo &amp; \u4e16\u754c &nbsp; &copy;</p>",
			want: "<p>h\u00e9llo & \u4e16\u754c &nbsp; &copy;</p>",
		},
		{
			name: "escaped ampersands that look like entities stay escaped",
			in:   "<p>&amp;copy; &amp;lt; &amp;amp; a&amp;b &amp;#65; &lt;b&gt; &quot;x&quot;</p>",
			want: "<p>&amp;copy; &amp;lt; &amp;amp; a&amp;b &amp;#65; &lt;b> \"x\"</p>",
		},
		{
			name: "byte order mark is preserved",
			in:   "\ufeff<p>a</p>",
			want: "\ufeff<p>a</p>",
		},
		{
			name: "script tag with template literal is still minified around it",
			in:   "<script>let a = `${x}`;</script><p>  a   b  </p>",
			want: "<script>let a = `${x}`;</script><p> a b </p>",
		},
		{
			name: "white-space nowrap does not block minification",
			in:   "<style>.a{white-space:nowrap}</style><p>  a   b  </p>",
			want: "<style>.a{white-space:nowrap}</style><p> a b </p>",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := minifyString(t, m, "text/html", tc.in)
			assert.Equal(t, tc.want, got)
			requireSafeEqual(t, kindHTML, []byte(tc.in), []byte(got), false)
		})
	}
}

func TestHTMLUnchangedInputs(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SkipMinified = false
	m := New(cfg)
	tests := map[string]string{
		"go template":              "<p>  {{ .Title }}  </p>",
		"handlebars block":         "<p>  {{#each a}}  x  {{/each}}  </p>",
		"jinja statement":          "<p>  {% if a %}  x  {% endif %}  </p>",
		"asp or erb":               "<p>  <%= a %>  </p>",
		"erb in script":            "<script>var a = '<%= a %>';</script><p>  x  </p>",
		"php":                      "<p>  <?php echo 1; ?>  </p>",
		"php short echo":           "<p>  <?= $a ?>  </p>",
		"expression language":      "<p th:text=\"${user.name}\">  x   y  </p>",
		"legacy doctype":           "<!DOCTYPE HTML PUBLIC \"-//W3C//DTD HTML 4.01 Transitional//EN\">\n<html>\n<body>\n<p>  x  </p>\n</body>\n</html>",
		"xhtml doctype":            "<!DOCTYPE html PUBLIC \"-//W3C//DTD XHTML 1.0 Strict//EN\" \"http://www.w3.org/TR/xhtml1/DTD/xhtml1-strict.dtd\"><p>  x  </p>",
		"xmp":                      "<xmp>  a   b  </xmp><p>  x  </p>",
		"listing":                  "<listing>\n  a   b  </listing><p>  x  </p>",
		"inline white-space: pre":  "<div style=\"white-space: pre\">  a   b  </div>",
		"style white-space: pre":   "<style>.a{ white-space:pre-wrap }</style><p>  x  </p>",
		"style white-space: break": "<style>.a{white-space:break-spaces}</style><p>  x  </p>",
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, in, minifyString(t, m, "text/html", in))
		})
	}
}

func TestHTMLSimpleDoctypes(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SkipMinified = false
	m := New(cfg)
	for _, in := range []string{
		"<!DOCTYPE html>\n<p>  x  </p>",
		"<!doctype   HTML  >\n<p>  x  </p>",
		"<!DOCTYPE html SYSTEM \"about:legacy-compat\">\n<p>  x  </p>",
	} {
		got := minifyString(t, m, "text/html", in)
		assert.True(t, strings.HasPrefix(got, "<!doctype html>"), got)
		assert.Contains(t, got, "<p> x </p>")
	}
}

func TestHTMLJSSafeAndOff(t *testing.T) {
	in := "<script>var a  =  1; // c\n  foo( a );</script><div onclick=\"foo( 1 )\">x</div><script type=\"module\">import  x from  './a.js' ;</script><script type=\"application/json\">{ \"a\" : 1 }</script><script type=\"text/babel\">a  =  <b/>;</script>"
	want := "<script>var a=1;foo(a)</script><div onclick=\"foo(1)\">x</div><script type=\"module\">import  x from  './a.js' ;</script><script type=\"application/json\">{ \"a\" : 1 }</script><script type=\"text/babel\">a  =  <b/>;</script>"

	m := New(newConfig(Safe, Safe, Safe, Safe, Safe))
	assert.Equal(t, want, minifyString(t, m, "text/html", in))

	m = New(newConfig(Safe, Safe, Off, Safe, Safe))
	assert.Equal(t, in, minifyString(t, m, "text/html", in))
}

func TestHTMLScriptThatDoesNotParseIsKept(t *testing.T) {
	m := New(newConfig(Safe, Safe, Safe, Safe, Safe))
	in := "<script>function (</script><p>  a  </p>"
	assert.Equal(t, "<script>function (</script><p> a </p>", minifyString(t, m, "text/html", in))
}

func TestHTMLCSSOffLeavesStylesAlone(t *testing.T) {
	m := New(newConfig(Safe, Off, Off, Safe, Safe))
	in := "<style>a  {  color : red ; }</style><div style=\"color : red ;\">  x  </div>"
	assert.Equal(t, "<style>a  {  color : red ; }</style><div style=\"color : red ;\"> x </div>", minifyString(t, m, "text/html", in))
}

func TestHTMLRiskyEmbeddedCSSIsKept(t *testing.T) {
	m := New(DefaultConfig())
	in := "<style>::part(Foo)  {  color : red ; }</style><style>a  {  color : red ; }</style>"
	assert.Equal(t, "<style>::part(Foo)  {  color : red ; }</style><style>a{color:red}</style>", minifyString(t, m, "text/html", in))
}

func TestHTMLAggressive(t *testing.T) {
	cfg := aggressiveConfig()
	cfg.SkipMinified = false
	m := New(cfg)
	in := "<!DOCTYPE html><html><head><title>t</title></head><body><ul><li>a</li><li>b</li></ul><p>x</p><p class=\"a b\">y</p><input type=\"text\" disabled=\"disabled\"></body></html>"
	want := "<!doctype html><title>t</title><ul><li>a<li>b</ul><p>x<p class=\"a b\">y</p><input disabled>"
	assert.Equal(t, want, minifyString(t, m, "text/html", in))

	in = "<p>  {{ a }}  </p>"
	assert.Equal(t, in, minifyString(t, m, "text/html", in), "template markers are honored in every mode")
}

func TestHTMLSignatureDetectsDamage(t *testing.T) {
	good := htmlSignature(t, []byte("<p class=\"a\">x  <b>y</b></p>"), false)
	assert.Equal(t, good, htmlSignature(t, []byte("<p class=a>x <b>y</b> </p>"), false))
	assert.NotEqual(t, good, htmlSignature(t, []byte("<p class=\"a\">x<b>z</b></p>"), false))
	assert.NotEqual(t, good, htmlSignature(t, []byte("<p class=\"b\">x  <b>y</b></p>"), false))
	assert.NotEqual(t, good, htmlSignature(t, []byte("<p>x  <b>y</b></p>"), false))
	pre := htmlSignature(t, []byte("<pre>a  b</pre>"), false)
	require.NotEqual(t, pre, htmlSignature(t, []byte("<pre>a b</pre>"), false))
}
