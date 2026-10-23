package admin

import (
	"bytes"
	"io"
)

func bytesReader(b []byte) io.ReadSeeker { return bytes.NewReader(b) }

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
