package metrics

import (
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// ContentType is the media type of the text exposition format 0.0.4.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

const maxPooledBuf = 4 << 20

var (
	bufPool = sync.Pool{New: func() any { b := make([]byte, 0, 32<<10); return &b }}
	gzPool  = sync.Pool{New: func() any { return gzip.NewWriter(io.Discard) }}
)

// Handler returns an http.Handler serving the registry in text format 0.0.4.
// Only GET and HEAD are allowed. The body is gzip-compressed only when the
// client advertises gzip in Accept-Encoding.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		bp := bufPool.Get().(*[]byte)
		body := r.Gather((*bp)[:0])
		defer func() {
			if cap(body) <= maxPooledBuf {
				*bp = body[:0]
				bufPool.Put(bp)
			}
		}()

		h := w.Header()
		h.Set("Content-Type", ContentType)
		h.Add("Vary", "Accept-Encoding")
		if !acceptsGzip(req.Header.Values("Accept-Encoding")) {
			h.Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			if req.Method != http.MethodHead {
				_, _ = w.Write(body)
			}
			return
		}
		h.Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		if req.Method == http.MethodHead {
			return
		}
		zw := gzPool.Get().(*gzip.Writer)
		zw.Reset(w)
		_, _ = zw.Write(body)
		_ = zw.Close()
		zw.Reset(io.Discard)
		gzPool.Put(zw)
	})
}

func acceptsGzip(values []string) bool {
	for _, v := range values {
		for part := range strings.SplitSeq(v, ",") {
			name, params, _ := strings.Cut(part, ";")
			if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
				continue
			}
			for p := range strings.SplitSeq(params, ";") {
				k, val, ok := strings.Cut(strings.TrimSpace(p), "=")
				if ok && strings.EqualFold(k, "q") {
					if q, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil && q <= 0 {
						return false
					}
				}
			}
			return true
		}
	}
	return false
}
