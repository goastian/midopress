package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// httpServer returns the URL of a server that answers with a page that
// compresses well.
func httpServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(strings.Repeat("<p>a paragraph that repeats</p>\n", 2000)))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}
