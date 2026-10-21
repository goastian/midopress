package server_test

import (
	"io"
	"log"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// httptest servers log every connection that closes mid-handshake to the
	// default logger, which buries benchmark results.
	if os.Getenv("MIDOPRESS_TEST_LOG") == "" {
		log.SetOutput(io.Discard)
	}
	os.Exit(m.Run())
}
