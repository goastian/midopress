package textmin

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSSafeGolden(t *testing.T) {
	m := New(newConfig(Off, Off, Safe, Off, Off))
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "directive prologue, comments and CRLF",
			in:   "'use strict';\r\n// comment\r\nvar x = 1;\r\n",
			want: "\"use strict\";var x=1",
		},
		{
			name: "license comment, template literal, regexp and unicode",
			in:   "/*! license */\nvar a = `t ${ b } \u00e9`;\nvar re = /ab+c/gi, s = 'caf\u00e9 \u4e16\u754c';\nconst o = { a, b: 2 };\n",
			want: "/*! license */var a=`t ${b} \u00e9`,re=/ab+c/gi,s=\"caf\u00e9 \u4e16\u754c\";const o={a,b:2}",
		},
		{
			name: "automatic semicolon insertion",
			in:   "var a = 1\nvar b = a\n;(function(){ return b })()\n",
			want: "var a=1,b=a;(function(){return b})()",
		},
		{
			name: "identifiers are never renamed",
			in:   "function outer(argumentOne) { var inner = function(argumentTwo) { return argumentOne + argumentTwo }; return inner }\nvar API = outer(1)(2); export default API;",
			want: "function outer(argumentOne){var inner=function(argumentTwo){return argumentOne+argumentTwo};return inner}var API=outer(1)(2);export default API",
		},
		{
			name: "function source stays inspectable by name",
			in:   "var f = function(a, b) { return a + b };\nvar src = f.toString();\n",
			want: "var f=function(a,b){return a+b},src=f.toString()",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := minifyString(t, m, "text/javascript", tc.in)
			assert.Equal(t, tc.want, got)
			requireJSPreserved(t, []byte(tc.in), []byte(got))
		})
	}
}

func TestJSAggressiveRenamesLocals(t *testing.T) {
	m := New(newConfig(Off, Off, Aggressive, Off, Off))
	in := "function outer(argumentOne) { var inner = function(argumentTwo) { return argumentOne + argumentTwo }; return inner }\nvar API = outer(1)(2); export default API;"
	want := "function outer(e){var t=function(t){return e+t};return t}var API=outer(1)(2);export default API"
	assert.Equal(t, want, minifyString(t, m, "application/javascript", in))
}

func TestJSMediaTypes(t *testing.T) {
	m := New(newConfig(Off, Off, Safe, Off, Off))
	for _, mt := range []string{"text/javascript", "application/javascript", "application/x-javascript", "application/ecmascript", "text/javascript; charset=utf-8", "APPLICATION/JavaScript"} {
		assert.Equal(t, "var a=1", minifyString(t, m, mt, "var  a  =  1;"), mt)
	}
}

func TestJSInvalidWritesNothing(t *testing.T) {
	m := New(newConfig(Off, Off, Safe, Off, Off))
	var w countingWriter
	err := m.Minify("text/javascript", &w, []byte("function ("))
	require.Error(t, err)
	assert.Zero(t, w.writes)
}

func TestJSCorpusSafe(t *testing.T) {
	m := New(newConfig(Off, Off, Safe, Off, Off))
	for _, name := range []string{"jquery/3.7.1/jquery.js", "htmx.min.js", "lodash.min.js", "react.production.min.js"} {
		t.Run(name, func(t *testing.T) {
			orig := corpusBody(t, name)
			var out bytes.Buffer
			require.NoError(t, m.Minify("application/javascript", &out, orig))
			assert.Less(t, out.Len(), len(orig)+1)
			requireJSPreserved(t, orig, out.Bytes())
		})
	}
}

// TestJSSafeLibraryRegression pins the handling of a minifier bug: with
// KeepVarNames the library emits a duplicate declaration for vue.global.prod.js.
// Output that does not parse must never be returned.
func TestJSSafeLibraryRegression(t *testing.T) {
	orig := corpusBody(t, "vue.global.prod.min.js")
	m := New(newConfig(Off, Off, Safe, Off, Off))
	var out countingWriter
	err := m.Minify("text/javascript", &out, orig)
	if err != nil {
		assert.Zero(t, out.writes, "nothing may be written on error")
		return
	}
	var buf bytes.Buffer
	require.NoError(t, m.Minify("text/javascript", &buf, orig))
	requireJSPreserved(t, orig, buf.Bytes())
}

func TestJSAggressiveCorpus(t *testing.T) {
	m := New(newConfig(Off, Off, Aggressive, Off, Off))
	for _, name := range []string{"jquery/3.7.1/jquery.js", "htmx.min.js", "vue.global.prod.min.js"} {
		t.Run(name, func(t *testing.T) {
			orig := corpusBody(t, name)
			var out bytes.Buffer
			require.NoError(t, m.Minify("application/javascript", &out, orig))
			requireJSPreserved(t, orig, out.Bytes())
		})
	}
}
