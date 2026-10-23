// Package pipeline decides what to do with each upstream response and writes
// it to the client: pass it through untouched, or recompress it. Whatever goes
// wrong while transforming, the client still receives the original response.
package pipeline

import (
	"bytes"
	"context"
	"net/http"
	"net/netip"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"

	"github.com/goastian/midopress/internal/codec"
	"github.com/goastian/midopress/internal/fonts"
	"github.com/goastian/midopress/internal/httpx"
	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/rules"
	"github.com/goastian/midopress/internal/textmin"
	"github.com/goastian/midopress/internal/xcache"
)

// maxExpansion caps how much a compressed text body may grow when decoded,
// relative to its size on the wire. Real text rarely goes past 20 times.
const maxExpansion = 32

// Reason says why a response was left alone, or Transformed when it was not.
type Reason uint8

const (
	Transformed Reason = iota
	PassMethod
	PassStatus
	PassRange
	PassNoTransform
	PassType
	PassStream
	PassEncoding
	PassAlreadyCompressed
	PassNoCodec
	PassTooSmall
	PassTooLarge
	PassDisabled
	PassAnimated
	PassBusy
	PassTimeout
	PassNotSmaller
	PassError
	PassBudget
	PassRule
	numReasons
)

var reasonNames = [numReasons]string{
	"transformed", "method", "status", "range", "no-transform", "type", "stream",
	"encoding", "already-compressed", "no-codec", "too-small", "too-large",
	"disabled", "animated", "busy", "timeout", "not-smaller", "error", "budget", "rule",
}

func (r Reason) String() string {
	if int(r) < len(reasonNames) {
		return reasonNames[r]
	}
	return "unknown"
}

// NumReasons is the number of distinct reasons, for sizing counters.
const NumReasons = int(numReasons)

// Causes of PassBusy: what was exhausted.
const (
	CauseMemoryBody   = "memory-body"   // the memory budget could not hold the body of an image
	CauseMemoryPixels = "memory-pixels" // ... or the pixels of the decoded picture
	CauseMemoryText   = "memory-text"   // ... or the buffers of a text response
	CauseCPU          = "cpu"           // no CPU slot became free in time
)

// TextConfig tunes the compression of text like content.
type TextConfig struct {
	Minify     textmin.Config // what to minify, and how far
	Recompress bool           // turn gzip, deflate or zstd responses into brotli when the client accepts it
	BufferMax  int64          // bodies up to this size are buffered, which allows the only-if-smaller check
	MinBytes   int64          // smaller bodies are not worth compressing
	MaxDecoded int64          // limit for what a compressed body may expand to
}

// ImageConfig tunes image recoding.
type ImageConfig struct {
	JPEG        bool // recode JPEG images
	JPEGQuality int  // 1 to 100 fixes the quality, 0 finds it for each image
	PNG         bool
	GIF         bool
	// Modern recodes the WebP and AVIF pictures that sites already send.
	Modern bool
	// PageHints reads the HTML that goes by to learn how wide the page shows
	// each image, for the profiles that resize, and which files have an
	// integrity hash, which are never minified.
	PageHints bool
	// HintDPR is the pixel density assumed for those widths, 2 when 0.
	HintDPR   float64
	MinBytes  int64
	MaxBytes  int64
	MaxPixels int64
	Timeout   time.Duration // longest a request waits for a recoded image
	Metric    string        // dssim, ssim or ssimulacra2
	// Background finishes, after the response went out, the images that did
	// not fit in Timeout, so that the next request finds them in the cache.
	Background        bool
	BackgroundTimeout time.Duration
	// Warmup compiles the image codecs when the pipeline starts.
	Warmup bool
}

// Config is the pipeline configuration.
type Config struct {
	MinGain       float64       // a result must be at least this fraction smaller than the original
	MaxConcurrent int           // concurrent CPU heavy transformations, 0 means the number of CPUs
	TranscodeWait time.Duration // how long to wait for a free slot before passing the response through
	MemoryBudget  int64         // bytes the pipeline may hold in buffers at the same time
	Profile       string        // default profile
	// NoTransform is what to do with a response that says Cache-Control:
	// no-transform: respect, images or all. Empty or "profile" leaves it to the
	// profile of the request, which respects it unless it says otherwise.
	NoTransform string
	// Profiles are the profiles available, nil for the built-in ones.
	Profiles map[string]*img.Profile
	Levels   codec.Levels // text levels that replace the ones of the profile, when not zero
	Text     TextConfig
	Image    ImageConfig
	// Rules is evaluated for every response, nil for none.
	Rules *rules.RuleSet
	// TTL bounds how long results stay in the cache.
	TTL TTLConfig
	// Cache keeps recoded images and fonts and slowly compressed text. Nil
	// makes a memory cache of 256 MiB.
	Cache *xcache.Cache
	// Backend encodes the images, nil for the WebAssembly one.
	Backend img.Backend
}

// DefaultConfig returns the defaults.
func DefaultConfig() Config {
	return Config{
		MinGain:       0.02,
		MaxConcurrent: 0,
		TranscodeWait: 250 * time.Millisecond,
		MemoryBudget:  256 << 20,
		Profile:       img.ProfileBalanced,
		TTL:           DefaultTTL(),
		Text: TextConfig{
			Minify:     textmin.DefaultConfig(),
			Recompress: true,
			BufferMax:  1 << 20,
			MinBytes:   256,
			MaxDecoded: 32 << 20,
		},
		Image: ImageConfig{
			JPEG:              true,
			PNG:               true,
			GIF:               true,
			Modern:            true,
			PageHints:         true,
			HintDPR:           2,
			MinBytes:          1024,
			MaxBytes:          8 << 20,
			MaxPixels:         16_000_000,
			Timeout:           3 * time.Second,
			Metric:            "dssim",
			Background:        true,
			BackgroundTimeout: 2 * time.Minute,
			Warmup:            true,
		},
	}
}

// Pipeline is safe for concurrent use.
type Pipeline struct {
	cfg    Config
	text   *codec.Text // only decodes, the levels do not matter
	live   atomic.Pointer[live]
	engine *img.Engine
	cache  *xcache.Cache
	flight singleflight.Group
	bg     *background
	warm   sync.WaitGroup
	sem    chan struct{}
	mem    *semaphore.Weighted
	// memUsed is what is reserved of the budget now, for the gauge.
	memUsed atomic.Int64
	inst    atomic.Pointer[instruments]
	pages   *pageHints

	capture atomic.Bool

	buffers  sync.Pool
	copyBufs sync.Pool
	reasons  [numReasons]atomic.Uint64
}

// New builds a pipeline. Zero values in cfg are replaced by the defaults.
func New(cfg Config) *Pipeline {
	def := DefaultConfig()
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = runtime.GOMAXPROCS(0)
	}
	if cfg.TranscodeWait <= 0 {
		cfg.TranscodeWait = def.TranscodeWait
	}
	if cfg.MemoryBudget <= 0 {
		cfg.MemoryBudget = def.MemoryBudget
	}
	if cfg.Text.BufferMax <= 0 {
		cfg.Text.BufferMax = def.Text.BufferMax
	}
	if cfg.Text.MaxDecoded <= 0 {
		cfg.Text.MaxDecoded = def.Text.MaxDecoded
	}
	if cfg.Image.MaxBytes <= 0 {
		cfg.Image.MaxBytes = def.Image.MaxBytes
	}
	if cfg.Image.MaxPixels <= 0 {
		cfg.Image.MaxPixels = def.Image.MaxPixels
	}
	if cfg.Image.Timeout <= 0 {
		cfg.Image.Timeout = def.Image.Timeout
	}
	if cfg.Image.BackgroundTimeout <= 0 {
		cfg.Image.BackgroundTimeout = def.Image.BackgroundTimeout
	}
	if cfg.TTL == (TTLConfig{}) {
		cfg.TTL = def.TTL
	}
	if cfg.Profile == "" {
		cfg.Profile = def.Profile
	}
	if cfg.Text.Minify == (textmin.Config{}) {
		cfg.Text.Minify = textmin.Config{HTML: textmin.Off, CSS: textmin.Off, JS: textmin.Off, SVG: textmin.Off, JSON: textmin.Off}
	}
	if cfg.Backend == nil {
		cfg.Backend = img.NewWASM()
	}
	if cfg.Cache == nil {
		cfg.Cache = xcache.New(256 << 20)
	}
	metric, err := img.ParseMetric(cfg.Image.Metric)
	if err != nil {
		metric = img.MetricDSSIM
	}
	lv, err := newLive(&cfg)
	if err != nil {
		// an unknown default falls back, so that a bad name can not stop the proxy
		cfg.Profile = def.Profile
		lv, _ = newLive(&cfg)
	}

	p := &Pipeline{
		cfg:    cfg,
		text:   codec.NewText(codec.Levels{Brotli: 6, Gzip: 6, Zstd: 3}),
		engine: img.NewEngine(cfg.Backend),
		cache:  cfg.Cache,
		sem:    make(chan struct{}, cfg.MaxConcurrent),
		mem:    semaphore.NewWeighted(cfg.MemoryBudget),
		pages:  newPageHints(),
	}
	p.engine.Metric = metric
	p.engine.OnEncode = p.observeEncode
	p.live.Store(lv)
	p.bg = newBackground(p)
	if cfg.Image.Warmup {
		p.warm.Add(1)
		go func() {
			defer p.warm.Done()
			p.engine.Warm()
		}()
	}
	p.buffers.New = func() any { return new(bytes.Buffer) }
	p.copyBufs.New = func() any {
		b := make([]byte, 32<<10)
		return &b
	}
	return p
}

// Reload applies the parts of cfg that can change while running: the default
// profile, the profiles, the text levels, what is minified, the rules and the
// TTLs. Everything else needs a restart. When cfg is not valid nothing changes.
func (p *Pipeline) Reload(cfg Config) error {
	if cfg.TTL == (TTLConfig{}) {
		cfg.TTL = DefaultTTL()
	}
	if cfg.Text.Minify == (textmin.Config{}) {
		cfg.Text.Minify = textmin.Config{HTML: textmin.Off, CSS: textmin.Off, JS: textmin.Off, SVG: textmin.Off, JSON: textmin.Off}
	}
	lv, err := newLive(&cfg)
	if err != nil {
		return err
	}
	p.live.Store(lv)
	return nil
}

// HostDecision evaluates the rules that can be decided from the host alone, at
// the time a client asks for a tunnel.
func (p *Pipeline) HostDecision(host string, src netip.Addr) rules.Decision {
	rs := p.live.Load().rules
	if rs == nil || rs.Len() == 0 {
		return rules.Decision{}
	}
	return rs.EvaluateHost(host, src, netip.Addr{})
}

// SetCapture makes the pipeline hand out the bodies of the images it recodes in
// the Outcome, so that the original and the result can be compared. It costs
// nothing but a reference to what the cache holds anyway.
func (p *Pipeline) SetCapture(on bool) { p.capture.Store(on) }

// ClearCache drops everything the cache holds in memory and on disk.
func (p *Pipeline) ClearCache() { p.cache.Clear() }

// CacheStats returns the counters of the cache.
func (p *Pipeline) CacheStats() xcache.Stats { return p.cache.Stats() }

// CPU returns the slots of the limiter, the busy ones and the jobs waiting in
// the background queue.
func (p *Pipeline) CPU() (slots, busy, queued int) {
	return cap(p.sem), len(p.sem), len(p.bg.queue)
}

// Engine returns the image engine.
func (p *Pipeline) Engine() *img.Engine { return p.engine }

// Profiles returns the profiles in use, by name, and the default one.
func (p *Pipeline) Profiles() (map[string]*img.Profile, string) {
	lv := p.live.Load()
	return lv.profiles, lv.profile
}

// Decide says what the rules and the profile selection would do for a request,
// without any body. It is what "test a URL" shows.
func (p *Pipeline) Decide(req *http.Request, resp *http.Response, mt string) Decision {
	lv := p.live.Load()
	d := Decision{Profile: nil}
	var dec rules.Decision
	if lv.rules != nil && lv.rules.Len() > 0 {
		c := rulesContext(req, resp, mt)
		d.Explanations = lv.rules.Explain(c)
		dec = lv.rules.Evaluate(c)
	}
	d.Action = dec.Action
	d.Set = dec.Set
	d.Profile, d.ProfileSource = lv.pickWithSource(ClientFrom(req.Context()), req.Header, dec.Profile)
	return d
}

// WaitBackground waits until the background jobs are done or ctx ends, and
// reports whether they are done.
func (p *Pipeline) WaitBackground(ctx context.Context) bool {
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for !p.bg.idle() {
		select {
		case <-t.C:
		case <-ctx.Done():
			return false
		}
	}
	return true
}

// Close stops the background work.
func (p *Pipeline) Close() {
	p.bg.close()
	// the warm-up can not be interrupted, but it must not hold a shutdown
	done := make(chan struct{})
	go func() { p.warm.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

// Outcome describes what happened to one response.
type Outcome struct {
	Reason   Reason
	BytesIn  int64 // body bytes read from the origin
	BytesOut int64 // body bytes written to the client
	// Err is set when the upstream body could not be read. If Written is false
	// nothing reached the client yet and the caller should answer with an error
	// status; if Written is true the client response is incomplete and must be
	// aborted.
	Err     error
	Written bool

	// What follows describes the response for the people who watch the proxy:
	// the administration interface and the statistics.
	Kind        string // image, text, font, media or other
	ContentType string // of the response of the origin
	// IgnoredNoTransform is set when the response said no-transform and was
	// transformed anyway, because the profile or the configuration allows it.
	IgnoredNoTransform bool
	// Cause refines Reason when that is not enough to act on: why the response
	// was busy (see the Cause constants) or, for a type that is not handled,
	// the media type.
	Cause    string
	OutType  string // of what was sent, when it is not the same
	Codec    string // avif, webp, jxl, jpeg, png, gif, br, zstd, gzip, deflate, woff2 or identity
	Profile  string
	CacheHit bool
	Metric   string  // the measure that was used for an image, and what it reached
	Measure  float64 //
	Minified bool
	// Original and Result are the bodies of an image, kept only while capture
	// is on. They are shared with the cache: do not change them.
	Original []byte
	Result   []byte
}

// Stats returns how many responses ended with each reason.
func (p *Pipeline) Stats() [NumReasons]uint64 {
	var s [NumReasons]uint64
	for i := range s {
		s[i] = p.reasons[i].Load()
	}
	return s
}

// Handle writes resp to w, recompressing it when that is safe and worthwhile.
// The hop-by-hop headers must already be gone from resp.Header.
func (p *Pipeline) Handle(w http.ResponseWriter, req *http.Request, resp *http.Response) Outcome {
	start := time.Now()
	out := p.handle(w, req, resp)
	out.ContentType = httpx.MediaType(resp.Header)
	out.Kind = kindName(out.ContentType)
	p.reasons[out.Reason].Add(1)
	p.record(resp, out, time.Since(start))
	return out
}

// Decision is the answer of Decide.
type Decision struct {
	Explanations  []rules.Explanation
	Action        rules.Action
	Set           map[string]any
	Profile       *img.Profile
	ProfileSource string
}

func kindName(mt string) string {
	switch {
	case fonts.Convertible(mt) || strings.HasPrefix(mt, "font/") || mt == "application/vnd.ms-fontobject":
		return "font"
	case kindOf(mt) == kindImage:
		return "image"
	case kindOf(mt) == kindText:
		return "text"
	case isMedia(mt):
		return "media"
	}
	return "other"
}

// isMedia reports whether a media type is video or audio, or the container a
// streaming service wraps them in: HLS playlists and the UMP stream of YouTube.
// A DASH manifest is XML, so it counts as text. These are the bytes that dominate mobile traffic and that no
// recompression here touches.
func isMedia(mt string) bool {
	if strings.HasPrefix(mt, "video/") || strings.HasPrefix(mt, "audio/") {
		return true
	}
	switch mt {
	case "application/vnd.yt-ump", "application/vnd.apple.mpegurl", "application/x-mpegurl",
		"application/mp4", "application/ogg":
		return true
	}
	return false
}

type kind uint8

const (
	kindNone kind = iota
	kindText
	kindImage
)

func (p *Pipeline) handle(w http.ResponseWriter, req *http.Request, resp *http.Response) Outcome {
	k, mt, reason := p.classify(req, resp)
	if k == kindNone {
		out := p.passthrough(w, resp, reason)
		out.Cause = classifyCause(req, resp, reason, mt)
		return out
	}
	lv := p.live.Load()
	var dec rules.Decision
	if lv.rules != nil && lv.rules.Len() > 0 {
		dec = lv.rules.Evaluate(rulesContext(req, resp, mt))
		if dec.Action != rules.ActionNone {
			return p.passthrough(w, resp, PassRule)
		}
	}
	fl, err := lv.newFlow(req, dec)
	if err != nil {
		return p.passthrough(w, resp, PassError)
	}
	overrode := false
	if reason == PassNoTransform {
		if !fl.mayIgnoreNoTransform(k) {
			out := p.passthrough(w, resp, PassNoTransform)
			out.Cause = classifyCause(req, resp, PassNoTransform, mt)
			return out
		}
		overrode = true
	}
	out := p.transform(w, req, resp, mt, k, fl, reason)
	out.IgnoredNoTransform = overrode && out.Reason == Transformed
	return out
}

// transform runs the pipeline of a kind of content.
func (p *Pipeline) transform(w http.ResponseWriter, req *http.Request, resp *http.Response, mt string, k kind, fl *flow, reason Reason) Outcome {
	switch k {
	case kindText:
		if !fl.opts.text {
			return p.passthrough(w, resp, PassDisabled)
		}
		return p.handleText(w, req, resp, mt, fl)
	case kindImage:
		if !fl.opts.images {
			return p.passthrough(w, resp, PassDisabled)
		}
		return p.handleImage(w, req, resp, mt, fl)
	}
	return p.passthrough(w, resp, reason)
}

// classify applies the cheap checks that rule out a transformation.
func (p *Pipeline) classify(req *http.Request, resp *http.Response) (kind, string, Reason) {
	if req.Method != http.MethodGet && req.Method != http.MethodPost {
		return kindNone, "", PassMethod
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNonAuthoritativeInfo {
		return kindNone, "", PassStatus
	}
	h := resp.Header
	if req.Header.Get("Range") != "" || h.Get("Content-Range") != "" {
		return kindNone, "", PassRange
	}
	// A client that says no-transform means it. A response that says it can be
	// overridden by what the person chose (see flow.mayIgnoreNoTransform), which
	// is only known once the profile is, so it is carried to handle.
	if httpx.HeaderHasDirective(req.Header, "Cache-Control", "no-transform") {
		return kindNone, "", PassNoTransform
	}
	forbidden := httpx.HeaderHasDirective(h, "Cache-Control", "no-transform")
	mt := httpx.MediaType(h)
	if mt == "text/event-stream" || mt == "multipart/x-mixed-replace" {
		return kindNone, mt, PassStream
	}
	if resp.ContentLength == 0 {
		return kindNone, mt, PassTooSmall
	}
	reason := Transformed
	if forbidden {
		reason = PassNoTransform
	}
	switch k := kindOf(mt); k {
	case kindImage:
		if req.Method != http.MethodGet {
			return kindNone, mt, PassMethod
		}
		return k, mt, reason
	case kindText:
		return k, mt, reason
	}
	if forbidden {
		return kindNone, mt, PassNoTransform
	}
	return kindNone, mt, PassType
}

// classifyCause says more about why classify left a response alone: the media
// type that is not handled, the status, the method, or which side forbade the
// transformation.
func classifyCause(req *http.Request, resp *http.Response, reason Reason, mt string) string {
	switch reason {
	case PassType:
		if mt == "" {
			return "none"
		}
		if len(mt) > 64 {
			mt = mt[:64]
		}
		return mt
	case PassStatus:
		return strconv.Itoa(resp.StatusCode)
	case PassMethod:
		return req.Method
	case PassNoTransform:
		if httpx.HeaderHasDirective(req.Header, "Cache-Control", "no-transform") {
			return "request"
		}
		return "response"
	}
	return ""
}

func kindOf(mt string) kind {
	if fonts.Convertible(mt) {
		return kindText
	}
	switch mt {
	case "image/jpeg", "image/jpg", "image/png", "image/gif", "image/webp", "image/avif", "image/jxl":
		return kindImage
	case "application/json", "application/javascript", "application/x-javascript",
		"application/ecmascript", "application/xml", "application/wasm",
		"application/x-ndjson", "application/vnd.ms-fontobject",
		"application/x-font-ttf", "application/x-font-opentype", "application/font-sfnt",
		"font/ttf", "font/otf", "image/x-icon", "image/vnd.microsoft.icon", "image/bmp":
		return kindText
	}
	if strings.HasPrefix(mt, "text/") ||
		strings.HasSuffix(mt, "+json") || strings.HasSuffix(mt, "+xml") {
		return kindText
	}
	return kindNone
}

func (p *Pipeline) getBuf() *bytes.Buffer {
	b := p.buffers.Get().(*bytes.Buffer)
	b.Reset()
	return b
}

func (p *Pipeline) putBuf(b *bytes.Buffer) {
	if b.Cap() <= 4<<20 {
		p.buffers.Put(b)
	}
}

// worthIt reports whether a result of size got is small enough compared with
// the original of size want to be sent instead of it.
func (p *Pipeline) worthIt(got, want int) bool {
	return float64(got) <= float64(want)*(1-p.cfg.MinGain)
}
