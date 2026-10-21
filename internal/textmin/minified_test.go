package textmin

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLooksMinifiedTable(t *testing.T) {
	prose := strings.Repeat("The quick brown fox jumps over the lazy dog.\n", 40)
	longLine := strings.Repeat("a", 3000)
	dense := strings.Repeat("a=f(1,2);b=a+1;", 200)
	tests := []struct {
		name string
		in   []byte
		want bool
	}{
		{"nil", nil, false},
		{"empty", []byte{}, false},
		{"one byte", []byte("a"), false},
		{"tiny minified", []byte(`{"a":1}`), false},
		{"tiny pretty", []byte("{\n  \"a\": 1\n}\n"), false},
		{"just under minimum", bytes.Repeat([]byte("a"), minifiedMinSize-1), false},
		{"minimum size single line", bytes.Repeat([]byte("a"), minifiedMinSize), true},
		{"prose", []byte(prose), false},
		{"very long first line", []byte(longLine + "\n" + prose), true},
		{"long first line but lots of whitespace", []byte(strings.Repeat("a b ", 1000) + "\n" + prose), false},
		{"dense code single line", []byte(dense), true},
		{"dense code with license header", []byte("/*!\n * header\n */\n" + dense), true},
		{"binary with NUL", append(bytes.Repeat([]byte{0xff}, 2000), 0), false},
		{"binary without newlines", bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 1000), true},
		{"CRLF prose", []byte(strings.ReplaceAll(prose, "\n", "\r\n")), false},
		{"whitespace only", []byte(strings.Repeat(" \n", 500)), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, LooksMinified(tc.in))
		})
	}
}

func TestLooksMinifiedCorpus(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"lodash.min.js", true},
		{"bootstrap.min.css", true},
		{"htmx.min.js", true},
		{"jquery.min.js", true},
		{"vue.global.prod.min.js", true},
		{"normalize.min.css", true},
		{"react.production.min.js", true},
		{"left-pad", true},
		{"jquery/3.7.1/jquery.js", false},
		{"iana.org/domains/reserved", false},
		{"https://www.iana.org/", false},
		{"kernel.org", false},
		{"ladder.svg", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, LooksMinified(corpusBody(t, tc.name)))
		})
	}
	for _, e := range loadCorpus(t) {
		if e.URL == "https://go.dev/" || strings.HasSuffix(e.URL, "wikipedia.org/") || e.URL == "https://example.com/" {
			assert.False(t, LooksMinified(e.Body), e.URL)
		}
	}
}

func TestLooksMinifiedDoesNotAllocate(t *testing.T) {
	in := []byte(strings.Repeat("var a=1;", 1000))
	assert.Zero(t, testing.AllocsPerRun(20, func() { LooksMinified(in) }))
}
