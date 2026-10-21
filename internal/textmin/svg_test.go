package textmin

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const inkscapeSVG = "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!-- c -->\n<svg xmlns=\"http://www.w3.org/2000/svg\" xmlns:xlink=\"http://www.w3.org/1999/xlink\" xmlns:inkscape=\"http://x\" xmlns:foo=\"http://f\" width=\"100px\" height=\"50\" viewBox=\"0 0 100.50 50\" preserveAspectRatio=\"xMinYMin slice\" version=\"1.1\" x=\"0\">\n  <metadata><rdf:RDF/></metadata>\n  <defs><linearGradient id=\"g1\"><stop offset=\"0\" stop-color=\"#ff0000\"/></linearGradient></defs>\n  <style>.a  { fill : #ff0000 }</style>\n  <g id=\"grp\" inkscape:label=\"L\" class=\"a\"><path id=\"p\" d=\"M 10.123456 20.5 L 30 40 Z\" fill=\"url(#g1)\" stroke=\"red\"/><use xlink:href=\"#p\"/><foo:bar a=\"1\"/></g>\n  <foreignObject><div xmlns=\"http://www.w3.org/1999/xhtml\">  a   b  </div></foreignObject>\n  <text x=\"1\">  Hello  </text>\n</svg>\n"

func TestSVGSafeGolden(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SkipMinified = false
	m := New(cfg)
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "editor output",
			in:   inkscapeSVG,
			want: "<svg xmlns=\"http://www.w3.org/2000/svg\" xmlns:xlink=\"http://www.w3.org/1999/xlink\" xmlns:foo=\"http://f\" width=\"100\" height=\"50\" viewBox=\"0 0 100.5 50\" preserveAspectRatio=\"xMinYMin slice\"><defs><linearGradient id=\"g1\"><stop offset=\"0\" stop-color=\"red\"/></linearGradient></defs><style>.a{fill:red}</style><g id=\"grp\" class=\"a\"><path id=\"p\" d=\"M10.123456 20.5 30 40z\" fill=\"url(#g1)\" stroke=\"red\"/><use xlink:href=\"#p\"/><foo:bar a=\"1\"/></g><foreignObject><div xmlns=\"http://www.w3.org/1999/xhtml\">  a   b  </div></foreignObject><text x=\"1\">Hello</text></svg>",
		},
		{
			name: "simple shape with CRLF",
			in:   "<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 10 10\">\r\n  <rect id=\"r\" x=\"1.0\" y=\"1\" width=\"8\" height=\"8\"/>\r\n</svg>\r\n",
			want: "<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 10 10\"><rect id=\"r\" x=\"1\" y=\"1\" width=\"8\" height=\"8\"/></svg>",
		},
		{
			name: "xml:lang is kept",
			in:   "<svg xmlns=\"http://www.w3.org/2000/svg\" xml:lang=\"fr\"><text>  \u00e9t\u00e9  </text></svg>",
			want: "<svg xmlns=\"http://www.w3.org/2000/svg\" xml:lang=\"fr\"><text>\u00e9t\u00e9</text></svg>",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := minifyString(t, m, "image/svg+xml", tc.in)
			assert.Equal(t, tc.want, got)
			requireSVGPreserved(t, []byte(tc.in), []byte(got))
		})
	}
}

func TestSVGRiskyInputsAreKeptInSafeMode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SkipMinified = false
	safe := New(cfg)
	aggressive := New(newConfig(Off, Off, Off, Aggressive, Off))
	const ns = "<svg xmlns=\"http://www.w3.org/2000/svg\">"
	tests := []struct {
		name           string
		in             string
		aggressiveWant string
	}{
		{"xml:space preserve", ns + "<text xml:space=\"preserve\">a  b</text></svg>", ns + "<text>a b</text></svg>"},
		{"script with line comment", ns + "<script>// x\nalert(1)</script><rect/></svg>", ""},
		{"mixed text content", ns + "<text>a <tspan>b</tspan></text></svg>", ns + "<text>a<tspan>b</tspan></text></svg>"},
		{"white-space pre style", ns + "<style>text{white-space:pre}</style><text>a  b</text></svg>", ""},
		{"non-UTF-8 declaration", "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?>\n" + ns + "<rect  /></svg>", ns + "<rect/></svg>"},
		{"stylesheet instruction", "<?xml-stylesheet href=\"a.css\" type=\"text/css\"?>\n" + ns + "<rect  /></svg>", ns + "<rect/></svg>"},
		{"svg prefix", "<svg:svg xmlns:svg=\"http://www.w3.org/2000/svg\"><svg:rect  /></svg:svg>", ""},
		{"unbalanced tags", ns + "<rect></svg>", ns + "<rect></svg>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.in, minifyString(t, safe, "image/svg+xml", tc.in))
			if tc.aggressiveWant != "" {
				assert.Equal(t, tc.aggressiveWant, minifyString(t, aggressive, "image/svg+xml", tc.in))
			}
		})
	}
}

func TestSVGUTF8DeclarationIsMinified(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SkipMinified = false
	m := New(cfg)
	in := "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<svg xmlns=\"http://www.w3.org/2000/svg\"><rect  /></svg>"
	assert.Equal(t, "<svg xmlns=\"http://www.w3.org/2000/svg\"><rect/></svg>", minifyString(t, m, "image/svg+xml", in))
}

func TestSVGCorpus(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SkipMinified = false
	m := New(cfg)
	for _, name := range []string{"ladder.svg", "SVG_logo.svg"} {
		t.Run(name, func(t *testing.T) {
			orig := corpusBody(t, name)
			got := minifyString(t, m, "image/svg+xml", string(orig))
			assert.Less(t, len(got), len(orig))
			requireSVGPreserved(t, orig, []byte(got))
		})
	}
}
