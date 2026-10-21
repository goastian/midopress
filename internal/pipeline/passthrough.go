package pipeline

import (
	"bytes"
	"io"
	"net/http"

	"github.com/goastian/midopress/internal/httpx"
)

// errReader remembers the error of the source so that a failure reading from
// the origin can be told apart from a failure writing to the client.
type errReader struct {
	r   io.Reader
	n   int64
	err error
}

func (e *errReader) Read(b []byte) (int, error) {
	n, err := e.r.Read(b)
	e.n += int64(n)
	if err != nil && err != io.EOF {
		e.err = err
	}
	return n, err
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n += int64(n)
	return n, err
}

// passthrough copies the response unchanged.
func (p *Pipeline) passthrough(w http.ResponseWriter, resp *http.Response, reason Reason) Outcome {
	return p.passthroughBody(w, resp, resp.Body, reason)
}

// passthroughBody copies resp using body, which may start with bytes that were
// already read from the origin and are being replayed.
func (p *Pipeline) passthroughBody(w http.ResponseWriter, resp *http.Response, body io.Reader, reason Reason) Outcome {
	httpx.CopyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	src := &errReader{r: body}
	cw := &countWriter{w: w}
	var err error
	if resp.ContentLength < 0 || httpx.MediaType(resp.Header) == "text/event-stream" {
		err = p.copyFlushing(cw, w, src)
	} else {
		bp := p.copyBufs.Get().(*[]byte)
		_, err = io.CopyBuffer(cw, src, *bp)
		p.copyBufs.Put(bp)
	}
	out := Outcome{Reason: reason, BytesIn: src.n, BytesOut: cw.n, Written: true}
	if src.err != nil {
		out.Err = src.err
		return out
	}
	if err != nil {
		return out
	}
	for k, v := range resp.Trailer {
		w.Header()[http.TrailerPrefix+k] = v
	}
	return out
}

// copyFlushing copies src to dst and flushes w after every read, so that
// streaming responses reach the client as the origin produces them.
func (p *Pipeline) copyFlushing(dst io.Writer, w http.ResponseWriter, src io.Reader) error {
	rc := http.NewResponseController(w)
	bp := p.copyBufs.Get().(*[]byte)
	defer p.copyBufs.Put(bp)
	buf := *bp
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
			if ferr := rc.Flush(); ferr != nil {
				return ferr
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return nil
			}
			return rerr
		}
	}
}

// writeBuffered sends the original headers with a body that is already in
// memory. It is the fallback used when a transformation is abandoned.
func (p *Pipeline) writeBuffered(w http.ResponseWriter, resp *http.Response, body []byte, in int64, reason Reason) Outcome {
	httpx.CopyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	n, _ := w.Write(body)
	return Outcome{Reason: reason, BytesIn: in, BytesOut: int64(n), Written: true}
}

// prefixed returns a reader that replays prefix and then continues with rest.
func prefixed(prefix []byte, rest io.Reader) io.Reader {
	if len(prefix) == 0 {
		return rest
	}
	return io.MultiReader(bytes.NewReader(prefix), rest)
}
