package transcoder

import (
	"compress/gzip"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"

	"github.com/goastian/midopress/internal/proxy"
)

type Zip struct {
	proxy.Transcoder
	BrotliCompressionLevel int
	GzipCompressionLevel   int
	SkipCompressed         bool
}

func (t *Zip) Transcode(w *proxy.ResponseWriter, r *proxy.ResponseReader, headers http.Header) error {
	shouldBrotli := false
	shouldGzip := false
	for v := range strings.SplitSeq(headers.Get("Accept-Encoding"), ", ") {
		switch strings.SplitN(v, ";", 2)[0] {
		case "br":
			shouldBrotli = true
		case "gzip":
			shouldGzip = true
		}
	}

	// always gunzip if the client supports Brotli
	if r.Header().Get("Content-Encoding") == "gzip" && (shouldBrotli || !t.SkipCompressed) {
		gzr, err := gzip.NewReader(r.Reader)
		if err != nil {
			return err
		}
		defer func() { _ = gzr.Close() }()
		r.Reader = gzr
		r.Header().Del("Content-Encoding")
		w.Header().Del("Content-Encoding")
	}

	if r.Header().Get("Content-Encoding") == "br" && !t.SkipCompressed {
		r.Reader = brotli.NewReader(r.Reader)
		r.Header().Del("Content-Encoding")
		w.Header().Del("Content-Encoding")
	}

	if shouldBrotli && compress(r) {
		brw := brotli.NewWriterLevel(w.Writer, t.BrotliCompressionLevel)
		defer func() { _ = brw.Close() }()
		w.Writer = brw
		w.Header().Set("Content-Encoding", "br")
	} else if shouldGzip && compress(r) {
		gzw, err := gzip.NewWriterLevel(w.Writer, t.GzipCompressionLevel)
		if err != nil {
			return err
		}
		defer func() { _ = gzw.Close() }()
		w.Writer = gzw
		w.Header().Set("Content-Encoding", "gzip")
	}
	return t.Transcoder.Transcode(w, r, headers)
}

func compress(r *proxy.ResponseReader) bool {
	return r.Header().Get("Content-Encoding") == ""
}
