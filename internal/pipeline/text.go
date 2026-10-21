package pipeline

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/goastian/midopress/internal/codec"
	"github.com/goastian/midopress/internal/httpx"
	"github.com/goastian/midopress/internal/negotiate"
)

// pickTarget returns the coding to use for the response, None when the client
// can not decode anything we are willing to produce.
func pickTarget(accept negotiate.Encodings) codec.Encoding {
	switch {
	case accept.Has(negotiate.Brotli):
		return codec.Brotli
	case accept.Has(negotiate.Zstd):
		return codec.Zstd
	case accept.Has(negotiate.Gzip):
		return codec.Gzip
	}
	return codec.None
}

func (p *Pipeline) handleText(w http.ResponseWriter, req *http.Request, resp *http.Response, mt string) Outcome {
	if len(resp.Header["Content-Encoding"]) > 1 {
		return p.passthrough(w, resp, PassEncoding)
	}
	srcEnc, ok := codec.ParseEncoding(resp.Header.Get("Content-Encoding"))
	if !ok {
		return p.passthrough(w, resp, PassEncoding)
	}
	target := pickTarget(negotiate.ParseAcceptEncoding(req.Header.Get("Accept-Encoding")))
	minify := p.cfg.Text.Minify && minifiable(mt)
	buffered := resp.ContentLength >= 0 && resp.ContentLength <= p.cfg.Text.BufferMax
	if !buffered {
		minify = false
	}

	// An encoded body is only decoded when its size is known and small, where
	// the expansion can be bounded. A stream of unknown length could be a
	// decompression bomb that burns CPU for as long as the origin likes.
	if srcEnc != codec.None && !buffered {
		return p.passthrough(w, resp, PassAlreadyCompressed)
	}

	upgrade := p.cfg.Text.Recompress && target == codec.Brotli && srcEnc != codec.Brotli
	switch {
	case srcEnc != codec.None && !minify && !upgrade:
		return p.passthrough(w, resp, PassAlreadyCompressed)
	case srcEnc == codec.None && target == codec.None && !minify:
		return p.passthrough(w, resp, PassNoCodec)
	case resp.ContentLength >= 0 && resp.ContentLength < p.cfg.Text.MinBytes && srcEnc == codec.None:
		return p.passthrough(w, resp, PassTooSmall)
	}

	if buffered {
		return p.textBuffered(w, resp, mt, srcEnc, target, minify)
	}
	return p.textStream(w, resp, target)
}

func (p *Pipeline) textBuffered(w http.ResponseWriter, resp *http.Response, mt string, srcEnc, target codec.Encoding, minify bool) Outcome {
	size := resp.ContentLength
	decodedCap := p.cfg.Text.MaxDecoded
	if srcEnc != codec.None {
		decodedCap = min(decodedCap, size*maxExpansion+64<<10)
	}
	weight := size*2 + decodedCap + 4096
	if srcEnc == codec.None {
		weight = size*3 + 4096
	}
	if !p.mem.TryAcquire(weight) {
		return p.passthrough(w, resp, PassBusy)
	}
	defer p.mem.Release(weight)

	raw := p.getBuf()
	defer p.putBuf(raw)
	raw.Grow(int(size) + 1)
	in := &errReader{r: resp.Body}
	if _, err := raw.ReadFrom(io.LimitReader(in, size+1)); err != nil || int64(raw.Len()) != size {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return Outcome{Reason: PassError, BytesIn: in.n, Err: err}
	}
	wire := raw.Bytes()

	plain := wire
	if srcEnc != codec.None {
		decoded := p.getBuf()
		defer p.putBuf(decoded)
		if err := p.text.Decode(srcEnc, wire, decoded, decodedCap); err != nil {
			reason := PassError
			if errors.Is(err, codec.ErrTooLarge) {
				reason = PassTooLarge
			}
			return p.writeBuffered(w, resp, wire, in.n, reason)
		}
		plain = decoded.Bytes()
	}

	if minify {
		min := p.getBuf()
		defer p.putBuf(min)
		if p.acquireCPU() {
			err := p.min.Minify(mt, min, bytes.NewReader(plain))
			p.releaseCPU()
			if err == nil {
				plain = min.Bytes()
			}
		}
	}

	result := plain
	if target != codec.None {
		comp := p.getBuf()
		defer p.putBuf(comp)
		if err := p.text.Compress(target, comp, plain); err != nil {
			return p.writeBuffered(w, resp, wire, in.n, PassError)
		}
		result = comp.Bytes()
	}

	if !p.worthIt(len(result), len(wire)) {
		return p.writeBuffered(w, resp, wire, in.n, PassNotSmaller)
	}

	h := w.Header()
	httpx.CopyHeader(h, resp.Header)
	markRecoded(h)
	if target == codec.None {
		h.Del("Content-Encoding")
	} else {
		h.Set("Content-Encoding", target.Token())
	}
	h.Set("Content-Length", strconv.Itoa(len(result)))
	w.WriteHeader(resp.StatusCode)
	n, _ := w.Write(result)
	return Outcome{Reason: Transformed, BytesIn: in.n, BytesOut: int64(n), Written: true}
}

// markRecoded adjusts the headers of a response whose bytes differ from the
// origin's.
func markRecoded(h http.Header) {
	httpx.WeakenETag(h)
	httpx.DropValidators(h)
	httpx.AddVary(h, "Accept-Encoding")
}

func (p *Pipeline) textStream(w http.ResponseWriter, resp *http.Response, target codec.Encoding) Outcome {
	in := &errReader{r: resp.Body}
	cw := &countWriter{w: w}
	sw, err := p.text.NewWriter(target, cw, resp.ContentLength)
	if err != nil {
		return p.passthrough(w, resp, PassError)
	}

	h := w.Header()
	httpx.CopyHeader(h, resp.Header)
	markRecoded(h)
	h.Set("Content-Encoding", target.Token())
	h.Del("Content-Length")
	w.WriteHeader(resp.StatusCode)

	// With no known length the body may be a live stream, so what is compressed
	// is pushed to the client after every read.
	flush := resp.ContentLength < 0
	var rc *http.ResponseController
	if flush {
		rc = http.NewResponseController(w)
	}
	bp := p.copyBufs.Get().(*[]byte)
	defer p.copyBufs.Put(bp)
	buf := *bp

	out := Outcome{Reason: Transformed, Written: true}
	for {
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := sw.Write(buf[:n]); werr != nil {
				break
			}
			if flush && (sw.Flush() != nil || rc.Flush() != nil) {
				break
			}
		}
		if rerr != nil {
			break
		}
	}
	_ = sw.Close()
	out.Err = in.err
	out.BytesIn, out.BytesOut = in.n, cw.n
	return out
}

// acquireCPU takes one slot of the CPU limiter, giving up after TranscodeWait.
func (p *Pipeline) acquireCPU() bool {
	select {
	case p.sem <- struct{}{}:
		return true
	default:
	}
	t := time.NewTimer(p.cfg.TranscodeWait)
	defer t.Stop()
	select {
	case p.sem <- struct{}{}:
		return true
	case <-t.C:
		return false
	}
}

func (p *Pipeline) releaseCPU() { <-p.sem }
