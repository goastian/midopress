package textmin

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONSafeGolden(t *testing.T) {
	m := New(newConfig(Off, Off, Off, Off, Safe))
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "whitespace only, numbers byte-identical",
			in:   "{\n  \"a\" : 1.0,\n  \"b\": [ 1e2, -0, 12345678901234567890123, 0.1000 ],\n  \"c\" : \"x\\u00e9  y\\n\",\n  \"d\":{ },\n  \"e\":[ ],\n  \"f\": null, \"g\": true\n}\n",
			want: "{\"a\":1.0,\"b\":[1e2,-0,12345678901234567890123,0.1000],\"c\":\"x\\u00e9  y\\n\",\"d\":{},\"e\":[],\"f\":null,\"g\":true}",
		},
		{name: "scalar", in: "  \"str\" \n", want: "\"str\""},
		{name: "unicode and CRLF", in: "[\r\n  \"\u00e9\u4e16\" ,\r\n  \"\\ud83d\\ude00\"\r\n]\r\n", want: "[\"\u00e9\u4e16\",\"\\ud83d\\ude00\"]"},
		{name: "nested", in: "{\"a\" : {\"b\" : [ {\"c\" : 1} , 2 ]}}", want: "{\"a\":{\"b\":[{\"c\":1},2]}}"},
		{name: "duplicate keys and order are kept", in: "{\"b\":1, \"a\":2, \"b\":3}", want: "{\"b\":1,\"a\":2,\"b\":3}"},
		{name: "strings that look like structure", in: "{\"a\" : \"{ } , :  \"}", want: "{\"a\":\"{ } , :  \"}"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := minifyString(t, m, "application/json", tc.in)
			assert.Equal(t, tc.want, got)
			requireJSONEqual(t, []byte(tc.in), []byte(got))
		})
	}
}

func TestJSONAggressiveRewritesNumbers(t *testing.T) {
	m := New(newConfig(Off, Off, Off, Off, Aggressive))
	in := "{\n  \"a\" : 1.0,\n  \"b\": [ 1e2, -0, 12345678901234567890123, 0.1000 ]\n}"
	assert.Equal(t, "{\"a\":1,\"b\":[100,0,12345678901234567890123,0.1]}", minifyString(t, m, "application/json", in))
}

func TestJSONInvalidIsRejected(t *testing.T) {
	for _, mode := range []Mode{Safe, Aggressive} {
		m := New(newConfig(Off, Off, Off, Off, mode))
		for name, in := range map[string]string{
			"trailing comma": `{"a":1,}`,
			"truncated":      `[1,2`,
			"NaN":            `{"a":NaN}`,
			"trailing data":  `{"a":1} x`,
			"comment":        "// c\n{\"a\":1}",
			"BOM":            "\xef\xbb\xbf{\"a\":1}",
		} {
			var w countingWriter
			require.Error(t, m.Minify("application/json", &w, []byte(in)), "%s/%s", mode, name)
			assert.Zero(t, w.writes, "%s/%s", mode, name)
		}
	}
}

func TestJSONMediaTypes(t *testing.T) {
	m := New(newConfig(Off, Off, Off, Off, Safe))
	for _, mt := range []string{"application/json", "text/json", "application/ld+json", "application/vnd.api+json; charset=utf-8", "application/problem+json"} {
		assert.Equal(t, `{"a":1}`, minifyString(t, m, mt, "{ \"a\" : 1 }"), mt)
	}
}

func TestJSONCorpus(t *testing.T) {
	m := New(newConfig(Off, Off, Off, Off, Safe))
	for _, name := range []string{"registry.npmjs.org/left-pad", "https://api.github.com/"} {
		t.Run(name, func(t *testing.T) {
			orig := corpusBody(t, name)
			got := minifyString(t, m, "application/json", string(orig))
			requireJSONEqual(t, orig, []byte(got))
		})
	}
}
