package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/goastian/midopress/internal/codec"
	"github.com/goastian/midopress/internal/fonts"
	"github.com/goastian/midopress/internal/httpx"
	"github.com/goastian/midopress/internal/negotiate"
	"github.com/goastian/midopress/internal/textmin"
	"github.com/goastian/midopress/internal/xcache"
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

func (p *Pipeline) handleText(w http.ResponseWriter, req *http.Request, resp *http.Response, mt string, fl *flow) Outcome {
	out := p.textFlow(w, req, resp, mt, fl)
	out.Profile = fl.prof.Name
	return out
}

func (p *Pipeline) textFlow(w http.ResponseWriter, req *http.Request, resp *http.Response, mt string, fl *flow) Outcome {
	if len(resp.Header["Content-Encoding"]) > 1 {
		return p.passthrough(w, resp, PassEncoding)
	}
	srcEnc, ok := codec.ParseEncoding(resp.Header.Get("Content-Encoding"))
	if !ok {
		return p.passthrough(w, resp, PassEncoding)
	}
	target := pickTarget(negotiate.ParseAcceptEncoding(req.Header.Get("Accept-Encoding")))
	minifier := fl.lv.minifier(fl.opts.minify)
	minify := minifier.Handles(mt)
	// a page that loads this file with an integrity hash would block it if a
	// byte changed
	if minify && p.pages.pinned(requestURL(req)) {
		minify = false
	}
	learn := p.wantsPages(fl, mt)
	asFont := fonts.Convertible(mt) && wantsWOFF2(req.Header)
	buffered := resp.ContentLength >= 0 && resp.ContentLength <= p.cfg.Text.BufferMax
	if !buffered {
		minify, asFont = false, false
	}

	// An encoded body is only decoded when its size is known and small, where
	// the expansion can be bounded. A stream of unknown length could be a
	// decompression bomb that burns CPU for as long as the origin likes.
	if srcEnc != codec.None && !buffered {
		return p.passthrough(w, resp, PassAlreadyCompressed)
	}

	upgrade := p.cfg.Text.Recompress && target == codec.Brotli && srcEnc != codec.Brotli
	// An origin that already sends brotli does not save anything by being
	// compressed at the level of the profile, but it usually does not use the
	// slowest levels, which give about a tenth less. Only profiles that keep
	// their slow level for what can be cached (saver and extreme) pay for it.
	squeeze := p.cfg.Text.Recompress && target == codec.Brotli && srcEnc == codec.Brotli && buffered &&
		fl.prof.Text.BrotliStatic >= squeezeMinLevel && cacheable(resp.Header)
	upgrade = upgrade || squeeze
	switch {
	case srcEnc != codec.None && !minify && !upgrade && !asFont:
		return p.passthrough(w, resp, PassAlreadyCompressed)
	case srcEnc == codec.None && target == codec.None && !minify && !asFont:
		return p.passthrough(w, resp, PassNoCodec)
	case resp.ContentLength >= 0 && resp.ContentLength < p.cfg.Text.MinBytes && srcEnc == codec.None:
		return p.passthrough(w, resp, PassTooSmall)
	}

	if buffered {
		return p.textBuffered(w, req, resp, mt, srcEnc, target, minify, asFont, learn, squeeze, fl, minifier)
	}
	return p.textStream(w, req, resp, target, learn, fl)
}

// wantsWOFF2 reports whether the client asked for a font in a way that shows it
// is a browser loading a font that it can read as WOFF2.
func wantsWOFF2(h http.Header) bool {
	return negotiate.AcceptsMedia(h.Get("Accept"), fonts.MediaTypeWOFF2) || strings.EqualFold(h.Get("Sec-Fetch-Dest"), "font")
}

// cacheable guesses whether a response will be asked for again as it is, which
// makes a slow compression level worth its cost.
func cacheable(h http.Header) bool {
	if h.Get("Set-Cookie") != "" || httpx.HeaderHasDirective(h, "Cache-Control", "private") ||
		httpx.HeaderHasDirective(h, "Cache-Control", "no-store") || httpx.HeaderHasDirective(h, "Cache-Control", "no-cache") {
		return false
	}
	if httpx.HeaderHasDirective(h, "Cache-Control", "immutable") {
		return true
	}
	for _, d := range strings.Split(strings.ToLower(h.Get("Cache-Control")), ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(d), "=")
		if ok && (k == "max-age" || k == "s-maxage") {
			if n, err := strconv.Atoi(strings.Trim(v, `"`)); err == nil && n >= 60 {
				return true
			}
		}
	}
	return false
}

func (p *Pipeline) textBuffered(w http.ResponseWriter, req *http.Request, resp *http.Response, mt string, srcEnc, target codec.Encoding, minify, asFont, learn, squeeze bool, fl *flow, minifier *textmin.Minifier) Outcome {

	size := resp.ContentLength
	decodedCap := p.cfg.Text.MaxDecoded
	if srcEnc != codec.None {
		decodedCap = min(decodedCap, size*maxExpansion+64<<10)
	}
	weight := size*2 + decodedCap + 4096
	if srcEnc == codec.None {
		weight = size*3 + 4096
	}
	mem := p.newReservation()
	defer mem.release()
	if !mem.grow(req.Context(), weight) {
		return withCause(p.passthrough(w, resp, PassBusy), CauseMemoryText)
	}

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

	if learn {
		p.pages.learn(req, plain)
	}

	if asFont {
		if e := p.convertFont(plain, resp.Header); e != nil {
			if !p.worthIt(len(e.Data), len(wire)) {
				return p.writeBuffered(w, resp, wire, in.n, PassNotSmaller)
			}
			h := w.Header()
			httpx.CopyHeader(h, resp.Header)
			markRecoded(h)
			httpx.AddVary(h, "Accept")
			httpx.AddVary(h, "Sec-Fetch-Dest")
			h.Del("Content-Encoding")
			h.Set("Content-Type", e.ContentType)
			h.Set("Content-Length", strconv.Itoa(len(e.Data)))
			w.WriteHeader(resp.StatusCode)
			n, _ := w.Write(e.Data)
			return Outcome{Reason: Transformed, BytesIn: in.n, BytesOut: int64(n), Written: true, Codec: "woff2", OutType: e.ContentType}
		}
	}

	minified := false
	if minify {
		min := p.getBuf()
		defer p.putBuf(min)
		if p.acquireCPU() {
			err := minifier.Minify(mt, min, plain)
			p.releaseCPU()
			if err == nil && min.Len() > 0 {
				minified = min.Len() < len(plain)
				plain = min.Bytes()
			}
		}
	}

	static := target == codec.Brotli && cacheable(resp.Header)
	if squeeze && !minified && !p.staticCached(fl, target, plain) && len(plain) > staticInlineMax(fl.prof.Text.BrotliStatic, fl.prof.Text.StaticWaitMS) {
		// too big to make the request wait for the slow level: the next one
		// finds it done
		p.squeezeLater(fl, target, plain, resp.Header)
		return p.writeBuffered(w, resp, wire, in.n, PassAlreadyCompressed)
	}
	result := plain
	if target != codec.None {
		comp, err := p.compress(fl, target, plain, static, resp.Header)
		if err != nil {
			return withCause(p.writeBuffered(w, resp, wire, in.n, PassError), "compress: "+errCause(err))
		}
		result = comp
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
	name := target.Token()
	if target == codec.None {
		name = "identity"
	}
	return Outcome{Reason: Transformed, BytesIn: in.n, BytesOut: int64(n), Written: true, Codec: name, Minified: minified}
}

// markRecoded adjusts the headers of a response whose bytes differ from the
// origin's.
func markRecoded(h http.Header) {
	httpx.WeakenETag(h)
	httpx.DropValidators(h)
	httpx.AddVary(h, "Accept-Encoding")
}

// squeezeMinLevel is the brotli level from which recompressing what an origin
// already compresses with brotli pays: below it the gain is under one percent.
const squeezeMinLevel = 10

// staticInlineMax is the largest body that is compressed at a slow level while
// the request waits no longer than waitMS. The speed of brotli in Go, measured
// on the text of real pages: level 11 runs at 0.2 MB/s, 10 at 0.4, 9 at 3.9 and
// 6 at 7. Bigger bodies are compressed in the background and found in the cache
// by the next request.
func staticInlineMax(level, waitMS int) int {
	var mbps float64
	switch {
	case level >= 11:
		mbps = 0.2
	case level == 10:
		mbps = 0.4
	case level >= 9:
		mbps = 3.5
	case level >= 7:
		mbps = 5
	default:
		mbps = 7
	}
	return int(mbps * 1e6 * float64(waitMS) / 1000)
}

func staticKey(prof string, target codec.Encoding, plain []byte) string {
	sum := sha256.Sum256(plain)
	return "text|" + prof + "|" + target.Token() + "|" + string(sum[:])
}

func (p *Pipeline) staticCached(fl *flow, target codec.Encoding, plain []byte) bool {
	e, ok := p.cache.Get(staticKey(fl.prof.Name, target, plain))
	return ok && !e.Keep
}

// squeezeLater compresses plain at the slow level of the profile when a CPU slot
// is free, and keeps the result by content.
func (p *Pipeline) squeezeLater(fl *flow, target codec.Encoding, plain []byte, h http.Header) {
	key := staticKey(fl.prof.Name, target, plain)
	codecs := fl.lv.texts[fl.prof.Name][1]
	body := append([]byte(nil), plain...)
	expires := p.expiry(h)
	p.bg.submitFunc(key, int64(len(body)), func(ctx context.Context) {
		var comp bytes.Buffer
		if err := codecs.Compress(target, &comp, body); err != nil {
			p.recordBackground("failed")
			return
		}
		p.recordBackground("done")
		p.cache.Add(key, &xcache.Entry{Data: comp.Bytes(), Expires: expires})
	})
}

// compress compresses plain with the levels of the profile. Responses that look
// cacheable use the slow brotli level of the profile, and their result is kept
// by content so that a popular file pays for it once. A body too big for the
// slow level to be worth the wait is compressed with the fast one, and the slow
// one is made in the background.
func (p *Pipeline) compress(fl *flow, target codec.Encoding, plain []byte, static bool, h http.Header) ([]byte, error) {
	prof := fl.prof
	pair := fl.lv.texts[prof.Name]
	if static {
		key := staticKey(prof.Name, target, plain)
		if e, ok := p.cache.Get(key); ok && !e.Keep {
			return e.Data, nil
		}
		if len(plain) <= staticInlineMax(prof.Text.BrotliStatic, prof.Text.StaticWaitMS) {
			var comp bytes.Buffer
			if err := pair[1].Compress(target, &comp, plain); err != nil {
				return nil, err
			}
			p.cache.Add(key, &xcache.Entry{Data: comp.Bytes(), Expires: p.expiry(h)})
			return comp.Bytes(), nil
		}
		p.squeezeLater(fl, target, plain, h)
	}
	comp := p.getBuf()
	defer p.putBuf(comp)
	if err := pair[0].Compress(target, comp, plain); err != nil {
		return nil, err
	}
	return append([]byte(nil), comp.Bytes()...), nil
}

// convertFont turns a TTF, OTF or WOFF font into WOFF2, or returns nil when it
// can not be done safely. Results are kept by content.
func (p *Pipeline) convertFont(plain []byte, h http.Header) *xcache.Entry {
	sum := sha256.Sum256(plain)
	key := "font|" + string(sum[:])
	if e, ok := p.cache.Get(key); ok {
		if e.Keep {
			return nil
		}
		return e
	}
	if !p.acquireCPU() {
		return nil
	}
	defer p.releaseCPU()
	out, err := fonts.ToWOFF2(plain)
	if err != nil {
		p.cache.Add(key, &xcache.Entry{Keep: true, Expires: p.expiry(h)})
		return nil
	}
	e := &xcache.Entry{Data: out, ContentType: fonts.MediaTypeWOFF2, Expires: p.expiry(h)}
	p.cache.Add(key, e)
	return e
}

func (p *Pipeline) textStream(w http.ResponseWriter, req *http.Request, resp *http.Response, target codec.Encoding, learn bool, fl *flow) Outcome {
	in := &errReader{r: resp.Body}
	cw := &countWriter{w: w}
	sw, err := fl.lv.texts[fl.prof.Name][0].NewWriter(target, cw, resp.ContentLength)
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

	// A page that comes as a stream is read as it goes by, before each piece is
	// sent on, so that what it says about its images is known by the time the
	// browser asks for them.
	var seen []byte
	scanned := 0
	out := Outcome{Reason: Transformed, Written: true, Codec: target.Token()}
	for {
		n, rerr := in.Read(buf)
		if n > 0 {
			if learn && len(seen) < pageScanBytes {
				seen = append(seen, buf[:n]...)
				// at 32 KiB, 64, 128, ...: the work stays linear in the page
				if len(seen) >= max(pageScanStep, 2*scanned) {
					p.pages.learn(req, seen)
					scanned = len(seen)
				}
			}
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
	if learn && len(seen) > scanned {
		p.pages.learn(req, seen)
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
