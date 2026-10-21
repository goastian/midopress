package codec

import (
	"io"

	"github.com/klauspost/compress/flate"
	"github.com/klauspost/compress/zlib"
)

func newZlibWriter(w io.Writer) io.WriteCloser { return zlib.NewWriter(w) }

func newFlateWriter(w io.Writer) io.WriteCloser {
	fw, err := flate.NewWriter(w, flate.DefaultCompression)
	if err != nil {
		panic(err)
	}
	return fw
}
