package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/goastian/midopress/internal/codec"
	"github.com/goastian/midopress/internal/httpx"
	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/xcache"
)

var (
	errBusy    = errors.New("pipeline: no free transcoding slot")
	errTimeout = errors.New("pipeline: transcoding deadline exceeded")
)

func (p *Pipeline) imageEnabled(kind img.Format) bool {
	ic := &p.cfg.Image
	switch kind {
	case img.JPEG:
		return ic.JPEG
	case img.PNG:
		return ic.PNG
	case img.GIF:
		return ic.GIF
	case img.WebP, img.AVIF, img.JXL:
		return ic.Modern
	}
	return false
}

// expiry is when a result computed for a response with these headers should
// leave the cache.
func (p *Pipeline) expiry(h http.Header) time.Time {
	now := time.Now()
	return now.Add(ttlFor(h, now, p.live.Load().ttl))
}

// quality returns the fixed quality for the request, 0 to search for it. The
// header is read per request, so concurrent requests can not affect each other.
func (p *Pipeline) quality(h http.Header, fl *flow) int {
	if fl.opts.quality > 0 {
		return fl.opts.quality
	}
	v := h.Get("X-MidoPress-Quality")
	if v == "" {
		v = h.Get("X-Compy-Quality")
	}
	if q, err := strconv.Atoi(v); err == nil && q >= 1 && q <= 100 {
		return q
	}
	return p.cfg.Image.JPEGQuality
}

// hintsFrom reads how the client shows images: the client hints of the browser
// or, from the extension, X-MidoPress-Viewport as "WIDTHxDPR". The width is
// rounded up to 50 pixels so that similar screens share cached results.
func hintsFrom(h http.Header) img.Hints {
	var hints img.Hints
	width := func(v string) int {
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || n < 1 || n > 20000 {
			return 0
		}
		return int(n)
	}
	dpr := func(v string) float64 {
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || n < 0.5 || n > 8 {
			return 0
		}
		return n
	}
	if v := h.Get("X-MidoPress-Viewport"); v != "" {
		w, d, _ := strings.Cut(strings.NewReplacer(",", "x", ";", "x").Replace(v), "x")
		hints.ViewportWidth, hints.DPR = width(w), dpr(d)
	}
	if hints.ViewportWidth == 0 {
		hints.ViewportWidth = width(h.Get("Sec-CH-Viewport-Width"))
	}
	if hints.ViewportWidth == 0 {
		hints.ViewportWidth = width(h.Get("Viewport-Width"))
	}
	if hints.DPR == 0 {
		hints.DPR = dpr(h.Get("Sec-CH-DPR"))
	}
	if hints.DPR == 0 {
		hints.DPR = dpr(h.Get("DPR"))
	}
	if hints.ViewportWidth > 0 {
		hints.ViewportWidth = (hints.ViewportWidth + 49) / 50 * 50
		hints.DPR = float64(int(hints.DPR*4+0.5)) / 4
	}
	return hints
}

func imageKey(plain []byte, prof *img.Profile, caps img.Caps, hints img.Hints, fixed int) string {
	h := sha256.New()
	h.Write(plain)
	_, _ = fmt.Fprintf(h, "|%s|%v%v%v|%d|%.2f|%d", prof.Name, caps.WebP, caps.AVIF, caps.JXL, hints.ViewportWidth, hints.DPR, fixed)
	return hex.EncodeToString(h.Sum(nil))
}

// imageDetail is what handleImage tells its caller about the response.
type imageDetail struct {
	entry *xcache.Entry
	hit   bool
	plain []byte
}

func (p *Pipeline) handleImage(w http.ResponseWriter, req *http.Request, resp *http.Response, mt string, fl *flow) Outcome {
	var d imageDetail
	out := p.imageFlow(w, req, resp, mt, fl, &d)
	out.Profile = fl.prof.Name
	if out.Reason == Transformed && d.entry != nil {
		out.OutType = d.entry.ContentType
		out.Codec = strings.TrimPrefix(d.entry.ContentType, "image/")
		out.CacheHit = d.hit
		out.Metric, out.Measure = d.entry.Metric, d.entry.Measure
		if p.capture.Load() {
			out.Original, out.Result = d.plain, d.entry.Data
		}
	}
	return out
}

func (p *Pipeline) imageFlow(w http.ResponseWriter, req *http.Request, resp *http.Response, mt string, fl *flow, d *imageDetail) Outcome {
	ic := &p.cfg.Image
	declared := img.JPEG
	switch mt {
	case "image/png":
		declared = img.PNG
	case "image/gif":
		declared = img.GIF
	case "image/webp":
		declared = img.WebP
	case "image/avif":
		declared = img.AVIF
	case "image/jxl":
		declared = img.JXL
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

	// The memory for the body is taken as the body arrives: twice its size (the
	// bytes as they came and, when they were encoded, the decoded ones). A body
	// of unknown length is not charged for the largest one there could be.
	mem := p.newReservation()
	defer mem.release()
	ctx := req.Context()

	var raw bytes.Buffer
	in := &errReader{r: resp.Body}
	limited := io.LimitReader(in, ic.MaxBytes+1)
	if resp.ContentLength >= 0 {
		if !mem.grow(ctx, resp.ContentLength*2) {
			return withCause(p.passthrough(w, resp, PassBusy), CauseMemoryBody)
		}
		raw.Grow(int(resp.ContentLength))
		if _, err := raw.ReadFrom(limited); err != nil {
			return Outcome{Reason: PassError, BytesIn: in.n, Err: err}
		}
	} else {
		for {
			if !mem.grow(ctx, (int64(raw.Len())+bodyStep)*2) {
				return withCause(p.passthroughBody(w, resp, prefixed(raw.Bytes(), resp.Body), PassBusy), CauseMemoryBody)
			}
			n, err := io.CopyN(&raw, limited, bodyStep)
			if err == io.EOF || (err == nil && n < bodyStep) {
				break
			}
			if err != nil {
				return Outcome{Reason: PassError, BytesIn: in.n, Err: err}
			}
		}
	}
	if int64(raw.Len()) > ic.MaxBytes {
		return p.passthroughBody(w, resp, prefixed(raw.Bytes(), resp.Body), PassTooLarge)
	}
	wire := raw.Bytes()

	plain := wire
	if srcEnc != codec.None {
		var decoded bytes.Buffer
		if err := p.text.Decode(srcEnc, wire, &decoded, ic.MaxBytes); err != nil {
			return withCause(p.writeBuffered(w, resp, wire, in.n, PassError), "content-encoding: "+errCause(err))
		}
		plain = decoded.Bytes()
	}

	kind := img.Sniff(plain)
	if kind == img.Unknown {
		return withCause(p.writeBuffered(w, resp, wire, in.n, PassType), "not a known image: "+mt)
	}
	if !p.imageEnabled(kind) {
		return withCause(p.writeBuffered(w, resp, wire, in.n, PassType), "turned off: "+kind.String())
	}

	prof := fl.prof
	caps := img.CapsFromAccept(req.Header.Get("Accept"))
	var hints img.Hints
	if prof.ResizeToViewport {
		hints = hintsFrom(req.Header)
		if hints.ViewportWidth == 0 && p.cfg.Image.PageHints {
			// no screen size from the client: use how wide the page showed it
			if w := p.pages.width(requestURL(req)); w > 0 {
				hints = img.Hints{ViewportWidth: (w + 49) / 50 * 50, DPR: p.dprGuess()}
			}
		}
	}
	fixed := p.quality(req.Header, fl)
	key := imageKey(plain, prof, caps, hints, fixed)
	input := img.Input{Raw: plain, Format: kind, Caps: caps, Profile: prof, Hints: hints, Fixed: fixed, Budget: ic.Timeout}

	// What is already done needs no memory for the picture: the cache is asked
	// before the pixels are counted.
	entry, ok := p.cache.Get(key)
	d.hit, d.plain = ok, plain
	if !ok {
		width, height, err := codec.ImageSize(codecKind(kind), plain)
		if err != nil {
			return withCause(p.writeBuffered(w, resp, wire, in.n, PassError), "size: "+errCause(err))
		}
		if int64(width)*int64(height) > ic.MaxPixels {
			return p.writeBuffered(w, resp, wire, in.n, PassTooLarge)
		}
		// decoding, resizing, measuring and encoding keep the picture and many
		// copies of it
		weight := pixelWeight(int64(width)*int64(height), caps, prof)
		if !mem.grow(ctx, mem.n+weight) {
			// not lost, only late: the next request finds it done
			p.bg.submit(key, withoutBudget(input), p.expiry(resp.Header), weight)
			return withCause(p.writeBuffered(w, resp, wire, in.n, PassBusy), CauseMemoryPixels)
		}

		res, err := p.recode(ctx, key, input)
		switch {
		case errors.Is(err, errBusy):
			// no CPU slot in time: the original goes now, and the next request
			// finds the result
			p.bg.submit(key, withoutBudget(input), p.expiry(resp.Header), weight)
			return withCause(p.writeBuffered(w, resp, wire, in.n, PassBusy), CauseCPU)
		case errors.Is(err, img.ErrBudget):
			p.bg.submit(key, withoutBudget(input), p.expiry(resp.Header), weight)
			return p.writeBuffered(w, resp, wire, in.n, PassBudget)
		case errors.Is(err, errTimeout), errors.Is(err, context.DeadlineExceeded):
			p.bg.submit(key, withoutBudget(input), p.expiry(resp.Header), weight)
			return p.writeBuffered(w, resp, wire, in.n, PassTimeout)
		case errors.Is(err, img.ErrUnsupported):
			p.cache.Add(key, &xcache.Entry{Keep: true, Expires: p.expiry(resp.Header)})
			return withCause(p.writeBuffered(w, resp, wire, in.n, PassType), leftAlone(kind, err))
		case err != nil:
			return withCause(p.writeBuffered(w, resp, wire, in.n, PassError), "recode: "+errCause(err))
		}
		entry = entryFor(res, len(plain), p.cfg.MinGain, p.expiry(resp.Header))
		if !res.Limited || !entry.Keep {
			// "the original is better" is not worth remembering when the time ran out
			p.cache.Add(key, entry)
		}
		if res.Limited {
			p.bg.submit(key, withoutBudget(input), p.expiry(resp.Header), weight)
		}
	}
	if entry.Keep || !p.worthIt(len(entry.Data), len(wire)) {
		return p.writeBuffered(w, resp, wire, in.n, PassNotSmaller)
	}
	d.entry = entry
	return p.writeRecoded(w, resp, srcEnc, entry, prof, hints, in.n)
}

// bodyStep is how much more of a body of unknown length is accounted for each
// time the memory for it runs out.
const bodyStep = 256 << 10

// pixelWeight is the memory that recoding a picture of px pixels holds at its
// peak, in bytes. It was measured, with the peak resident memory of the process
// around one recode of a photograph: 79 bytes per pixel when the answer is a
// JPEG, 156 when WebP is tried and 174 when AVIF is. The weight is that of the
// dearest format that may be tried, with a tenth over.
func pixelWeight(px int64, caps img.Caps, prof *img.Profile) int64 {
	per := int64(88)
	for _, f := range prof.Formats {
		switch {
		case f == img.AVIF && caps.AVIF:
			per = max(per, 192)
		case caps.Has(f):
			per = max(per, 172)
		}
	}
	return px * per
}

// leftAlone is the cause of an image that the engine would not recode: the
// format and, when the engine said, why (animated, a colour profile, ...).
func leftAlone(kind img.Format, err error) string {
	why := strings.TrimPrefix(err.Error(), img.ErrUnsupported.Error())
	return "left alone: " + kind.String() + why
}

// errCause is an error as a cause: short, and without what changes from one
// image to the next.
func errCause(err error) string {
	m := err.Error()
	if i := strings.IndexAny(m, "\n"); i >= 0 {
		m = m[:i]
	}
	if len(m) > 80 {
		m = m[:80]
	}
	return m
}

func withoutBudget(in img.Input) img.Input {
	in.Budget = 0
	return in
}

func codecKind(f img.Format) codec.ImageKind {
	switch f {
	case img.JPEG:
		return codec.KindJPEG
	case img.PNG:
		return codec.KindPNG
	case img.GIF:
		return codec.KindGIF
	case img.WebP:
		return codec.KindWebP
	case img.AVIF:
		return codec.KindAVIF
	case img.JXL:
		return codec.KindJXL
	}
	return codec.KindUnknown
}

func (p *Pipeline) writeRecoded(w http.ResponseWriter, resp *http.Response, srcEnc codec.Encoding, e *xcache.Entry, prof *img.Profile, hints img.Hints, bytesIn int64) Outcome {
	h := w.Header()
	httpx.CopyHeader(h, resp.Header)
	httpx.WeakenETag(h)
	httpx.DropValidators(h)
	httpx.AddVary(h, "Accept")
	httpx.AddVary(h, "Save-Data")
	if prof.ResizeToViewport {
		httpx.AddVary(h, "Sec-CH-Viewport-Width")
		httpx.AddVary(h, "Sec-CH-DPR")
	}
	if srcEnc != codec.None {
		httpx.AddVary(h, "Accept-Encoding")
		h.Del("Content-Encoding")
	}
	h.Set("Content-Type", e.ContentType)
	h.Set("Content-Length", strconv.Itoa(len(e.Data)))
	w.WriteHeader(resp.StatusCode)
	n, _ := w.Write(e.Data)
	return Outcome{Reason: Transformed, BytesIn: bytesIn, BytesOut: int64(n), Written: true}
}

// recode runs the engine for one image. Requests for the same image that
// arrive together share one run.
func (p *Pipeline) recode(ctx context.Context, key string, in img.Input) (*img.Result, error) {
	ch := p.flight.DoChan(key, func() (any, error) {
		return p.runCPU(in)
	})
	select {
	case r := <-ch:
		if r.Err != nil {
			return nil, r.Err
		}
		return r.Val.(*img.Result), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// runCPU runs the engine on a CPU slot. The engine checks its deadline between
// encodes, and an encode itself can not be interrupted, so the slot is held
// until it returns, which bounds the CPU used.
func (p *Pipeline) runCPU(in img.Input) (res *img.Result, err error) {
	if !p.acquireCPU() {
		return nil, errBusy
	}
	defer p.releaseCPU()
	defer func() {
		if r := recover(); r != nil {
			res, err = nil, fmt.Errorf("pipeline: codec panic: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.Image.Timeout)
	defer cancel()
	res, err = p.engine.Optimize(ctx, in)
	if err == nil && ctx.Err() != nil {
		if res == nil {
			return nil, errTimeout
		}
		// the time ran out in the middle of the search: what was found is the
		// best of what fitted, and a caller with more time can do better
		cp := *res
		cp.Limited = true
		res = &cp
	}
	return res, err
}
