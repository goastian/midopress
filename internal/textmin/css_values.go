package textmin

import (
	"bytes"
	"io"
	"strings"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/css"
)

func customProperties(src []byte, inline bool) ([]string, bool) {
	p := css.NewParser(parse.NewInputBytes(append([]byte(nil), src...)), inline)
	var values []string
	for {
		grammar, _, name := p.Next()
		if grammar == css.ErrorGrammar {
			if p.HasParseError() {
				return nil, false
			}
			return values, p.Err() == io.EOF
		}
		if grammar == css.CustomPropertyGrammar {
			value := p.Values()
			if len(value) != 1 {
				return nil, false
			}
			values = append(values, string(bytes.TrimSpace(name))+":"+strings.TrimSpace(string(value[0].Data)))
		}
	}
}
