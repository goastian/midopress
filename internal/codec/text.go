// Package codec holds the compression and image transcoding primitives used by
// the pipeline. Everything here is safe for concurrent use.
package codec

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"math/bits"
	"strings"
	"sync"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/flate"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zlib"
	"github.com/klauspost/compress/zstd"
)

// Encoding is a content coding.
type Encoding uint8

const (
	None Encoding = iota
	Deflate
	Gzip
	Zstd
	Brotli
)

// Token returns the Content-Encoding value of e.
func (e Encoding) Token() string {
	switch e {
	case Deflate:
		return "deflate"
	case Gzip:
		return "gzip"
	case Zstd:
		return "zstd"
	case Brotli:
		return "br"
	}
	return ""
}

// ParseEncoding parses a Content-Encoding header value holding a single
// coding. ok is false for chains and for codings that cannot be decoded.
func ParseEncoding(v string) (e Encoding, ok bool) {
	v = strings.TrimSpace(v)
	switch {
	case v == "" || strings.EqualFold(v, "identity"):
		return None, true
	case strings.EqualFold(v, "gzip"), strings.EqualFold(v, "x-gzip"):
		return Gzip, true
	case strings.EqualFold(v, "br"):
		return Brotli, true
	case strings.EqualFold(v, "zstd"):
		return Zstd, true
	case strings.EqualFold(v, "deflate"):
		return Deflate, true
	}
	return None, false
}

// Levels are the compression levels used for each encoder.
type Levels struct {
	Brotli int // 0 to 11
	Gzip   int // 0 to 9
	Zstd   int // 1 to 22, mapped to the closest level of the Go encoder
}

// StreamWriter compresses what is written to it. Close finishes the stream and
// returns the encoder to its pool, the writer must not be used afterwards.
type StreamWriter interface {
	io.Writer
	Flush() error
	Close() error
}

type resettable interface {
	io.Writer
	Flush() error
	Close() error
	Reset(io.Writer)
}

const (
	minLGWin = 10
	maxLGWin = 22
)

// Text compresses and decompresses text like content.
type Text struct {
	levels Levels

	gzipW sync.Pool
	zstdW sync.Pool
	brW   [maxLGWin + 1]sync.Pool

	gzipR sync.Pool
	brR   sync.Pool
	zstdR sync.Pool
}

// NewText returns a Text codec using the given levels.
func NewText(l Levels) *Text {
	l.Brotli = min(max(l.Brotli, 0), 11)
	l.Gzip = min(max(l.Gzip, 0), 9)
	l.Zstd = min(max(l.Zstd, 1), 22)
	return &Text{levels: l}
}

type pooledWriter struct {
	resettable
	pool *sync.Pool
}

func (p *pooledWriter) Close() error {
	err := p.resettable.Close()
	p.Reset(io.Discard)
	p.pool.Put(p)
	return err
}

func lgWin(size int64) int {
	if size <= 0 {
		return maxLGWin
	}
	return min(max(bits.Len64(uint64(size-1)), minLGWin), maxLGWin)
}

// NewWriter returns a compressor writing to w. sizeHint is the expected
// uncompressed size, zero or negative when unknown, and is used to size the
// brotli window.
func (t *Text) NewWriter(enc Encoding, w io.Writer, sizeHint int64) (StreamWriter, error) {
	switch enc {
	case Gzip:
		if p, ok := t.gzipW.Get().(*pooledWriter); ok {
			p.Reset(w)
			return p, nil
		}
		gz, err := gzip.NewWriterLevel(w, t.levels.Gzip)
		if err != nil {
			return nil, err
		}
		return &pooledWriter{resettable: gz, pool: &t.gzipW}, nil
	case Brotli:
		lg := lgWin(sizeHint)
		if p, ok := t.brW[lg].Get().(*pooledWriter); ok {
			p.Reset(w)
			return p, nil
		}
		bw := brotli.NewWriterOptions(w, brotli.WriterOptions{Quality: t.levels.Brotli, LGWin: lg})
		return &pooledWriter{resettable: bw, pool: &t.brW[lg]}, nil
	case Zstd:
		if p, ok := t.zstdW.Get().(*pooledWriter); ok {
			p.Reset(w)
			return p, nil
		}
		zw, err := zstd.NewWriter(w,
			zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(t.levels.Zstd)),
			zstd.WithEncoderConcurrency(1),
			zstd.WithWindowSize(1<<20),
			zstd.WithLowerEncoderMem(true),
		)
		if err != nil {
			return nil, err
		}
		return &pooledWriter{resettable: zw, pool: &t.zstdW}, nil
	}
	return nil, errors.New("codec: cannot compress with " + enc.Token())
}

// Compress compresses src into dst.
func (t *Text) Compress(enc Encoding, dst *bytes.Buffer, src []byte) error {
	dst.Grow(len(src)/3 + 64)
	w, err := t.NewWriter(enc, dst, int64(len(src)))
	if err != nil {
		return err
	}
	if _, err := w.Write(src); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

type pooledReader struct {
	io.Reader
	closeFn func()
}

func (p *pooledReader) Close() error {
	if p.closeFn != nil {
		p.closeFn()
		p.closeFn = nil
	}
	return nil
}

// NewReader returns a reader decoding r. Close must be called to return the
// decoder to its pool; it does not close r.
func (t *Text) NewReader(enc Encoding, r io.Reader) (io.ReadCloser, error) {
	switch enc {
	case None:
		return io.NopCloser(r), nil
	case Gzip:
		gz, _ := t.gzipR.Get().(*gzip.Reader)
		if gz == nil {
			var err error
			if gz, err = gzip.NewReader(r); err != nil {
				return nil, err
			}
		} else if err := gz.Reset(r); err != nil {
			return nil, err
		}
		return &pooledReader{Reader: gz, closeFn: func() { t.gzipR.Put(gz) }}, nil
	case Brotli:
		br, _ := t.brR.Get().(*brotli.Reader)
		if br == nil {
			br = brotli.NewReader(r)
		} else if err := br.Reset(r); err != nil {
			return nil, err
		}
		return &pooledReader{Reader: br, closeFn: func() { t.brR.Put(br) }}, nil
	case Zstd:
		zr, _ := t.zstdR.Get().(*zstd.Decoder)
		if zr == nil {
			var err error
			zr, err = zstd.NewReader(r,
				zstd.WithDecoderConcurrency(1),
				zstd.WithDecoderMaxMemory(256<<20),
				zstd.WithDecoderMaxWindow(64<<20),
			)
			if err != nil {
				return nil, err
			}
		} else if err := zr.Reset(r); err != nil {
			return nil, err
		}
		return &pooledReader{Reader: zr, closeFn: func() {
			_ = zr.Reset(nil)
			t.zstdR.Put(zr)
		}}, nil
	case Deflate:
		return newDeflateReader(r)
	}
	return nil, errors.New("codec: cannot decode " + enc.Token())
}

// newDeflateReader handles both zlib wrapped streams, which is what the HTTP
// specification asks for, and the raw deflate streams some servers send.
func newDeflateReader(r io.Reader) (io.ReadCloser, error) {
	br := bufio.NewReader(r)
	head, err := br.Peek(2)
	if err != nil {
		return nil, err
	}
	if head[0]&0x0f == 8 && (uint16(head[0])<<8|uint16(head[1]))%31 == 0 {
		return zlib.NewReader(br)
	}
	return flate.NewReader(br), nil
}

// ErrTooLarge is returned by Decode when the decoded data exceeds the limit.
var ErrTooLarge = errors.New("codec: decoded data exceeds the limit")

// Decode decodes src into dst. It fails with ErrTooLarge as soon as more than
// limit bytes come out, which stops decompression bombs.
func (t *Text) Decode(enc Encoding, src []byte, dst *bytes.Buffer, limit int64) error {
	rd, err := t.NewReader(enc, bytes.NewReader(src))
	if err != nil {
		return err
	}
	defer func() { _ = rd.Close() }()
	n, err := dst.ReadFrom(io.LimitReader(rd, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return ErrTooLarge
	}
	return nil
}
