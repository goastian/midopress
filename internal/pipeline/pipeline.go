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
	MinBytes    int64
	MaxBytes    int64
	MaxPixels   int64
	Timeout     time.Duration // longest a request waits for a recoded image
	Metric      string        // dssim, ssim or ssimulacra2
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
	inst   atomic.Pointer[instruments]

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
	p.reasons[out.Reason].Add(1)
	p.record(resp, out, time.Since(start))
	return out
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
		return p.passthrough(w, resp, reason)
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
	if httpx.HeaderHasDirective(h, "Cache-Control", "no-transform") ||
		httpx.HeaderHasDirective(req.Header, "Cache-Control", "no-transform") {
		return kindNone, "", PassNoTransform
	}
	mt := httpx.MediaType(h)
	if mt == "text/event-stream" || mt == "multipart/x-mixed-replace" {
		return kindNone, mt, PassStream
	}
	if resp.ContentLength == 0 {
		return kindNone, mt, PassTooSmall
	}
	switch k := kindOf(mt); k {
	case kindImage:
		if req.Method != http.MethodGet {
			return kindNone, mt, PassMethod
		}
		return k, mt, Transformed
	case kindText:
		return k, mt, Transformed
	}
	return kindNone, mt, PassType
}

func kindOf(mt string) kind {
	if fonts.Convertible(mt) {
		return kindText
	}
	switch mt {
	case "image/jpeg", "image/jpg", "image/png", "image/gif":
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
