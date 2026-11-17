package imageapi

import (
	"os"
	"testing"

	"github.com/goastian/midopress/internal/codecworker"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "codec-worker" {
		if codecworker.Run(os.Stdin, os.Stdout) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
