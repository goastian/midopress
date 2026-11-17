package server_test

import (
	"io"
	"log"
	"os"
	"testing"

	"github.com/goastian/midopress/internal/codecworker"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "codec-worker" {
		if err := codecworker.Run(os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	// httptest servers log every connection that closes mid-handshake to the
	// default logger, which buries benchmark results.
	if os.Getenv("MIDOPRESS_TEST_LOG") == "" {
		log.SetOutput(io.Discard)
	}
	os.Exit(m.Run())
}
