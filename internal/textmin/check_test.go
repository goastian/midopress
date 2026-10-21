package textmin

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"
)

func decodeNumbers(t testing.TB, b []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	require.NoError(t, dec.Decode(&v))
	return v
}

func requireJSONEqual(t testing.TB, orig, minified []byte) {
	t.Helper()
	require.True(t, json.Valid(minified), "minified JSON invalid: %.200s", minified)
	var a, b any
	require.NoError(t, json.Unmarshal(orig, &a))
	require.NoError(t, json.Unmarshal(minified, &b))
	require.True(t, reflect.DeepEqual(a, b), "plain decode differs")
	require.True(t, reflect.DeepEqual(decodeNumbers(t, orig), decodeNumbers(t, minified)), "number-preserving decode differs")
}

func requireJSPreserved(t testing.TB, orig, minified []byte) {
	t.Helper()
	_, err := js.Parse(parse.NewInput(bytes.NewBuffer(append([]byte(nil), minified...))), js.Options{})
	require.NoError(t, err, "minified JS does not parse")
	want := jsTopLevelNames(t, orig)
	got := jsTopLevelNames(t, minified)
	require.Subset(t, got, want, "top-level declarations lost")
}

func requireSignatureEqual(t testing.TB, want, got []string) {
	t.Helper()
	for i := 0; i < len(want) && i < len(got); i++ {
		if want[i] != got[i] {
			lo := max(0, i-2)
			require.Failf(t, "signatures differ", "at item %d:\n want %.300q\n  got %.300q\n context before: %.300q", i, want[i], got[i], want[lo:i])
		}
	}
	require.Equal(t, len(want), len(got), "signature length")
}

// requireSafeEqual checks the semantic invariants of Safe mode for kind k.
func requireSafeEqual(t testing.TB, k kind, orig, minified []byte, jsMinified bool) {
	t.Helper()
	switch k {
	case kindHTML:
		requireSignatureEqual(t, htmlSignature(t, orig, jsMinified), htmlSignature(t, minified, jsMinified))
	case kindCSS:
		requireSignatureEqual(t, cssSignature(t, orig, false), cssSignature(t, minified, false))
	case kindJS:
		requireJSPreserved(t, orig, minified)
	case kindSVG:
		requireSVGPreserved(t, orig, minified)
	case kindJSON:
		requireJSONEqual(t, orig, minified)
	}
}
