package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/goastian/midopress/internal/codec"
	"github.com/goastian/midopress/internal/httpx"
	"github.com/goastian/midopress/internal/negotiate"
)

var (
	errBusy    = errors.New("pipeline: no free transcoding slot")
	errTimeout = errors.New("pipeline: transcoding deadline exceeded")
)

func (p *Pipeline) imageEnabled(kind codec.ImageKind) bool {
	ic := &p.cfg.Image
	switch kind {
	case codec.KindJPEG:
		return ic.JPEGQuality > 0
	case codec.KindPNG:
		return ic.PNG
	case codec.KindGIF:
		return ic.GIF
	}
	return false
}

// quality returns the JPEG quality for the request. The header is read per
// request and never stored, so concurrent requests can not affect each other.
func (p *Pipeline) quality(h http.Header) int {
	v := h.Get("X-MidoPress-Quality")
	if v == "" {
		v = h.Get("X-Compy-Quality")
	}
	if q, err := strconv.Atoi(v); err == nil && q >= 1 && q <= 100 {
		return q
	}
	return p.cfg.Image.JPEGQuality
}

func (p *Pipeline) handleImage(w http.ResponseWriter, req *http.Request, resp *http.Response, mt string) Outcome {
	ic := &p.cfg.Image
	declared := codec.KindJPEG
	switch mt {
	case "image/png":
		declared = codec.KindPNG
	case "image/gif":
		declared = codec.KindGIF
	}
	if !p.imageEnabled(declared) {
		return p.passthrough(w, resp, PassDisabled)
	}
	if len(resp.Header["Content-Encoding"]) > 1 {
		return p.passthrough(w, resp, PassEncoding)
	}
	srcEnc, ok := codec.ParseEncoding(resp.Header.Get("Content-Encoding"))
	if !ok {
		return p.passthrough(w, resp, PassEncoding)
	}
	if resp.ContentLength > ic.MaxBytes {
		return p.passthrough(w, resp, PassTooLarge)
	}
	if resp.ContentLength >= 0 && resp.ContentLength < ic.MinBytes {
		return p.passthrough(w, resp, PassTooSmall)
	}

	expected := resp.ContentLength
	if expected < 0 {
		expected = ic.MaxBytes
	}
	weight := expected * 2
	if !p.mem.TryAcquire(weight) {
		return p.passthrough(w, resp, PassBusy)
	}
	defer p.mem.Release(weight)

	var raw bytes.Buffer
	if resp.ContentLength > 0 {
		raw.Grow(int(resp.ContentLength))
	}
	in := &errReader{r: resp.Body}
	if _, err := raw.ReadFrom(io.LimitReader(in, ic.MaxBytes+1)); err != nil {
		return Outcome{Reason: PassError, BytesIn: in.n, Err: err}
	}
	if int64(raw.Len()) > ic.MaxBytes {
		return p.passthroughBody(w, resp, prefixed(raw.Bytes(), resp.Body), PassTooLarge)
	}
	wire := raw.Bytes()

	plain := wire
	if srcEnc != codec.None {
		var decoded bytes.Buffer
		if err := p.text.Decode(srcEnc, wire, &decoded, ic.MaxBytes); err != nil {
			return p.writeBuffered(w, resp, wire, in.n, PassError)
		}
		plain = decoded.Bytes()
	}

	kind := codec.SniffImage(plain)
	if kind == codec.KindUnknown || !p.imageEnabled(kind) {
		return p.writeBuffered(w, resp, wire, in.n, PassType)
	}
	width, height, err := codec.ImageSize(kind, plain)
	if err != nil {
		return p.writeBuffered(w, resp, wire, in.n, PassError)
	}
	if int64(width)*int64(height) > ic.MaxPixels {
		return p.writeBuffered(w, resp, wire, in.n, PassTooLarge)
	}
	if kind == codec.KindGIF {
		if frames, err := codec.GIFFrames(plain, 2); err != nil {
			return p.writeBuffered(w, resp, wire, in.n, PassError)
		} else if frames > 1 {
			return p.writeBuffered(w, resp, wire, in.n, PassAnimated)
		}
	}

	toWebP := negotiate.AcceptsMedia(req.Header.Get("Accept"), "image/webp")
	quality := p.quality(req.Header)
	img, err := p.runCPU(req.Context(), func() (codec.Image, error) {
		switch kind {
		case codec.KindJPEG:
			return codec.RecodeJPEG(plain, quality, toWebP)
		case codec.KindPNG:
			return codec.RecodePNG(plain, toWebP)
		}
		return codec.RecodeGIF(plain, toWebP)
	})
	switch {
	case errors.Is(err, errBusy):
		return p.writeBuffered(w, resp, wire, in.n, PassBusy)
	case errors.Is(err, errTimeout):
		return p.writeBuffered(w, resp, wire, in.n, PassTimeout)
	case err != nil:
		return p.writeBuffered(w, resp, wire, in.n, PassError)
	}
	if !p.worthIt(len(img.Data), len(wire)) {
		return p.writeBuffered(w, resp, wire, in.n, PassNotSmaller)
	}

	h := w.Header()
	httpx.CopyHeader(h, resp.Header)
	httpx.WeakenETag(h)
	httpx.DropValidators(h)
	httpx.AddVary(h, "Accept")
	if srcEnc != codec.None {
		httpx.AddVary(h, "Accept-Encoding")
		h.Del("Content-Encoding")
	}
	h.Set("Content-Type", img.ContentType)
	h.Set("Content-Length", strconv.Itoa(len(img.Data)))
	w.WriteHeader(resp.StatusCode)
	n, _ := w.Write(img.Data)
	return Outcome{Reason: Transformed, BytesIn: in.n, BytesOut: int64(n), Written: true}
}

// runCPU runs fn on a CPU slot with a deadline. The codecs can not be
// interrupted, so when the deadline passes the goroutine is left to finish in
// the background; it keeps its slot until then, which bounds the CPU used.
func (p *Pipeline) runCPU(ctx context.Context, fn func() (codec.Image, error)) (codec.Image, error) {
	if !p.acquireCPU() {
		return codec.Image{}, errBusy
	}
	type result struct {
		img codec.Image
		err error
	}
	done := make(chan result, 1)
	go func() {
		defer p.releaseCPU()
		defer func() {
			if r := recover(); r != nil {
				done <- result{err: fmt.Errorf("pipeline: codec panic: %v", r)}
			}
		}()
		img, err := fn()
		done <- result{img, err}
	}()

	t := time.NewTimer(p.cfg.Image.Timeout)
	defer t.Stop()
	select {
	case r := <-done:
		return r.img, r.err
	case <-t.C:
		return codec.Image{}, errTimeout
	case <-ctx.Done():
		return codec.Image{}, ctx.Err()
	}
}
