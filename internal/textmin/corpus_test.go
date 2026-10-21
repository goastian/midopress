package textmin

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/stretchr/testify/require"
)

type corpusEntry struct {
	URL         string
	ContentType string
	Body        []byte
}

var (
	corpusOnce sync.Once
	corpusData []corpusEntry
	corpusErr  error
)

func loadCorpus(tb testing.TB) []corpusEntry {
	tb.Helper()
	corpusOnce.Do(func() { corpusData, corpusErr = readCorpus() })
	if corpusErr != nil && os.IsNotExist(corpusErr) {
		tb.Skip("corpus not available: " + corpusErr.Error())
	}
	require.NoError(tb, corpusErr)
	return corpusData
}

func readCorpus() ([]corpusEntry, error) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "corpus", "corpus.har"))
	if err != nil {
		return nil, err
	}
	var har struct {
		Log struct {
			Entries []struct {
				Request  struct{ URL string }
				Response struct {
					Headers []struct{ Name, Value string }
					Content struct {
						Text     string
						Encoding string
					}
				}
			}
		}
	}
	if err := json.Unmarshal(raw, &har); err != nil {
		return nil, err
	}
	var out []corpusEntry
	for _, e := range har.Log.Entries {
		var ctype, cenc string
		for _, h := range e.Response.Headers {
			switch strings.ToLower(h.Name) {
			case "content-type":
				ctype = h.Value
			case "content-encoding":
				cenc = strings.ToLower(h.Value)
			}
		}
		body := []byte(e.Response.Content.Text)
		if e.Response.Content.Encoding == "base64" {
			if body, err = base64.StdEncoding.DecodeString(e.Response.Content.Text); err != nil {
				return nil, err
			}
		}
		switch cenc {
		case "gzip":
			zr, err := gzip.NewReader(bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			if body, err = io.ReadAll(zr); err != nil {
				return nil, err
			}
		case "br":
			if body, err = io.ReadAll(brotli.NewReader(bytes.NewReader(body))); err != nil {
				return nil, err
			}
		}
		out = append(out, corpusEntry{URL: e.Request.URL, ContentType: ctype, Body: body})
	}
	return out, nil
}

func corpusBody(tb testing.TB, substr string) []byte {
	tb.Helper()
	for _, e := range loadCorpus(tb) {
		if strings.Contains(e.URL, substr) {
			return e.Body
		}
	}
	tb.Fatalf("corpus entry %q not found", substr)
	return nil
}
