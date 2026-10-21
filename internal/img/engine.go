package img

import (
	"bytes"
	"context"
	"errors"
	"image"
	stdjpeg "image/jpeg"
	"image/png"
	"time"

	"github.com/goastian/midopress/internal/imgmeta"
	"github.com/goastian/midopress/internal/pngopt"
	"github.com/goastian/midopress/internal/quality"
)

// Input is one image to optimize and what is known about who will see it.
type Input struct {
	Raw     []byte
	Format  Format // as detected from the bytes
	Caps    Caps
	Profile *Profile
	Hints   Hints
	// Fixed forces the quality of the encoder, 1 to 100, and skips the search.
	// Zero lets the engine find the lowest quality that meets the profile.
	Fixed int
	// Budget is the CPU time the job may use. Zero means no limit. A format
	// that is estimated not to fit is skipped, so that a big image falls back
	// to a faster format instead of timing out.
	Budget time.Duration
	// TryAll evaluates every allowed format and keeps the smallest, instead of
	// stopping at the first that does well. It is meant for benchmarks.
	TryAll bool
}

// Result is the best representation found. It may be the original image with
// its metadata stripped.
type Result struct {
	Data     []byte
	Format   Format
	Lossless bool    // the pixels are exactly those of the source
	SSIM     float64 // similarity to the source, measured; 1 when lossless
	Attempts int     // encodes spent
	Resized  bool
	// Limited says that a better format was skipped because the time budget
	// was too short for it. A caller with more time can try again.
	Limited bool
	Quality int    // quality of the winning encode, 0 when not applicable
	Note    string // short description, for logs and benchmarks
}

// Engine recodes images with a Backend.
type Engine struct {
	be Backend
	jr JPEGRecompressor

	// Trace, when set, receives one line for every encode the search makes.
	Trace func(format string, args ...any)
}

func (e *Engine) tracef(format string, args ...any) {
	if e.Trace != nil {
		e.Trace(format, args...)
	}
}

// NewEngine returns an engine that encodes with be.
func NewEngine(be Backend) *Engine {
	e := &Engine{be: be}
	if jr, ok := be.(JPEGRecompressor); ok {
		e.jr = jr
	}
	return e
}

// Backend returns the backend in use.
func (e *Engine) Backend() Backend { return e.be }

// Optimize finds the smallest representation of in.Raw that the client can show
// and that meets the profile. It returns ErrUnsupported for images it will not
// touch, such as animated PNG or images with an unusual colour profile that no
// lossless step can improve, and ErrBudget when nothing fits in the time given.
func (e *Engine) Optimize(ctx context.Context, in Input) (*Result, error) {
	if in.Profile == nil {
		in.Profile, _ = Lookup(ProfileBalanced)
	}
	switch in.Format {
	case JPEG:
		return e.optimizeJPEG(ctx, in)
	case PNG:
		return e.optimizePNG(ctx, in)
	case GIF:
		return e.optimizeGIF(ctx, in)
	}
	return nil, ErrUnsupported
}

// ---- cost model ----

// Rough CPU time of one encode, decode and measurement per megapixel, from the
// benchmarks in docs. WebAssembly is far slower than native code, except for
// jpegli, which is fast either way.
func (e *Engine) attemptCost(f Format, pixels int) time.Duration {
	mp := float64(pixels) / 1e6
	var perMP float64
	switch f {
	case JPEG:
		perMP = 0.9
	case WebP:
		perMP = 1.2
		if e.be.Native(f) {
			perMP = 0.2
		}
	case AVIF:
		perMP = 1.4
		if e.be.Native(f) {
			perMP = 0.5
		}
	case JXL:
		perMP = 1.3
		if e.be.Native(f) {
			perMP = 0.5
		}
	default:
		perMP = 1
	}
	return time.Duration(perMP * mp * float64(time.Second))
}

// ---- JPEG ----

func (e *Engine) optimizeJPEG(ctx context.Context, in Input) (*Result, error) {
	meta, err := imgmeta.ParseJPEG(in.Raw)
	if err != nil || meta.Components == 4 {
		return nil, ErrUnsupported
	}
	icc := imgmeta.ClassifyICC(meta.ICC)
	// An sRGB profile is what every decoder assumes anyway. Any other profile
	// changes the colours, so it is kept and the pixels are not re-encoded.
	keepICC := meta.ICC != nil && icc != imgmeta.ICCsRGB
	opts := imgmeta.StripOptions{KeepICC: keepICC, KeepOrientation: true, KeepJFIF: true}
	stripped, err := imgmeta.StripJPEG(in.Raw, opts)
	if err != nil {
		return nil, ErrUnsupported
	}
	base := &Result{Data: stripped, Format: JPEG, Lossless: true, SSIM: 1, Note: "metadata stripped"}

	if in.Profile.Lossless || keepICC {
		if in.Caps.JXL && e.jr != nil {
			if jxl, err := e.jr.RecompressJPEG(in.Raw); err == nil && len(jxl) < len(base.Data) {
				return &Result{Data: jxl, Format: JXL, Lossless: true, SSIM: 1, Note: "jpeg to jxl, reversible"}, nil
			}
		}
		return base, nil
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	src, err := stdjpeg.Decode(bytes.NewReader(in.Raw))
	if err != nil {
		return base, nil
	}
	res, err := e.lossy(ctx, in, src, meta.Orientation, base)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// ---- lossy search ----

// Quality that usually lands near a similarity of 0.975 on photographs. The
// search starts here and corrects, so being off costs an extra encode, not
// quality.
var balancedQuality = map[Format]int{JPEG: 88, WebP: 82, AVIF: 66, JXL: 85}

// How much the similarity moves per quality point near the target, per format.
var qualitySlope = map[Format]float64{JPEG: 0.0016, WebP: 0.0018, AVIF: 0.0020, JXL: 0.0016}

const (
	searchMargin = 0.004 // a result this close above the target is good enough
	balancedSSIM = 0.975
)

// formatsFor lists the formats to try, in order, for this client and image.
func (e *Engine) formatsFor(in Input, alpha bool) []Format {
	var out []Format
	for _, f := range in.Profile.Formats {
		if in.Caps.Has(f) && e.be.Supports(f) {
			out = append(out, f)
		}
	}
	if !alpha {
		out = append(out, JPEG)
	}
	return out
}

// lossy runs the search on a decoded picture and returns the best result,
// which is base when nothing beats it.
func (e *Engine) lossy(ctx context.Context, in Input, src image.Image, orientation int, base *Result) (*Result, error) {
	p := in.Profile
	pic, alpha := Normalize(Orient(src, orientation))
	resized := false
	if p.ResizeToViewport {
		if w, h, ok := scaledSize(pic.Bounds().Dx(), pic.Bounds().Dy(), in.Hints); ok {
			pic, resized = Resize(pic, w, h), true
		}
	}
	ref, err := quality.NewReference(pic)
	if err != nil {
		return base, nil
	}

	pixels := pic.Bounds().Dx() * pic.Bounds().Dy()
	remaining := in.Budget
	best := base
	limited := false
	for _, f := range e.formatsFor(in, alpha) {
		if ctx.Err() != nil {
			break
		}
		attempts := p.Attempts
		if in.Fixed > 0 || attempts < 1 {
			attempts = 1
		}
		per := e.attemptCost(f, pixels)
		if in.Budget > 0 && f != JPEG {
			full := attempts
			for attempts > 0 && per*time.Duration(attempts) > remaining {
				attempts--
			}
			if attempts < full {
				limited = true
			}
			if attempts == 0 {
				continue
			}
		}
		res, spent := e.search(ctx, f, pic, ref, p, in, alpha, attempts)
		remaining -= spent
		if res != nil {
			res.Resized = resized
			if best == nil || len(res.Data) < len(best.Data) {
				best = res
			}
		}
		// A format that already saves a lot is enough: the rest would only
		// spend CPU to find a few percent more.
		if !in.TryAll && best != nil && best != base && len(best.Data)*100 <= len(in.Raw)*60 {
			break
		}
	}
	if best == nil {
		return nil, ErrBudget
	}
	if limited {
		cp := *best
		cp.Limited = true
		best = &cp
	}
	return best, nil
}

// search finds the lowest quality of f that reaches the target similarity. It
// aims a little above the target and corrects with the points it has measured:
// interpolating once a pass and a fail bracket the target, extrapolating before.
func (e *Engine) search(ctx context.Context, f Format, pic image.Image, ref *quality.Reference, p *Profile, in Input, alpha bool, attempts int) (*Result, time.Duration) {
	target := p.TargetSSIM
	if target <= 0 || target > 1 {
		target = balancedSSIM
	}
	aim := target + searchMargin/2
	q := clamp(balancedQuality[f]+int((target-balancedSSIM)/qualitySlope[f]), 1, 100)
	if in.Fixed > 0 {
		q = clamp(in.Fixed, 1, 100)
	}
	pixels := pic.Bounds().Dx() * pic.Bounds().Dy()
	opts := EncodeOptions{Effort: e.effort(f, pixels), Progressive: true}

	type point struct {
		q int
		s float64
	}
	var pass, fail *point
	var last *point
	var best *Result
	start := time.Now()
	spent := 0
	for spent < attempts {
		if ctx.Err() != nil {
			break
		}
		opts.Quality = q
		data, err := e.be.Encode(f, pic, opts)
		spent++
		if err != nil {
			break
		}
		dec, err := e.be.Decode(f, data)
		if err != nil {
			break
		}
		s, err := ref.Similarity(dec)
		if err != nil {
			break
		}
		ok := s >= target || in.Fixed > 0
		e.tracef("%s q=%d: %d bytes, similarity %.4f (target %.3f), %s", f, q, len(data), s, target, time.Since(start).Round(time.Millisecond))
		if ok && (best == nil || len(data) < len(best.Data)) {
			best = &Result{Data: data, Format: f, SSIM: s, Quality: q, Note: f.String()}
		}
		if in.Fixed > 0 {
			break
		}
		pt := &point{q, s}
		if ok {
			if pass == nil || q < pass.q {
				pass = pt
			}
			if s-target < searchMargin {
				break
			}
		} else if fail == nil || q > fail.q {
			fail = pt
		}

		slope := qualitySlope[f]
		if last != nil && last.q != q {
			if m := (s - last.s) / float64(q-last.q); m > slope/4 && m < slope*4 {
				slope = m
			}
		}
		last = pt
		var next int
		switch {
		case pass != nil && fail != nil:
			if pass.q-fail.q <= 1 {
				next = q
				break
			}
			frac := (aim - fail.s) / (pass.s - fail.s)
			next = fail.q + int(frac*float64(pass.q-fail.q)+0.5)
			next = clamp(next, fail.q+1, pass.q-1)
		default:
			next = clamp(q+int((aim-s)/slope+0.5*sign(aim-s)), q-25, q+25)
			if pass != nil {
				next = min(next, pass.q-1)
			}
			if fail != nil {
				next = max(next, fail.q+1)
			}
		}
		next = clamp(next, 1, 100)
		if next == q || (pass != nil && pass.q <= 1) {
			break
		}
		q = next
	}
	if best != nil {
		best.Attempts = spent
	}
	return best, time.Since(start)
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

// effort picks the encoder effort. Slower settings squeeze a few percent more
// out of the file, which only pays when the encoder is fast.
func (e *Engine) effort(f Format, pixels int) int {
	switch f {
	case WebP:
		return 7 // libwebp method 4
	case AVIF:
		if e.be.Native(f) {
			return 3 // speed 8
		}
		return 2 // speed 9
	case JXL:
		// effort 3: the higher ones cost several times more and, on photographs
		// at lossy qualities, did not give smaller files in the tests
		return 3
	}
	return 0
}

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }

// ---- PNG ----

func (e *Engine) optimizePNG(ctx context.Context, in Input) (*Result, error) {
	info, err := imgmeta.ParsePNG(in.Raw)
	if err != nil || info.Animated {
		return nil, ErrUnsupported
	}
	level := pngopt.Default
	if in.Budget > 0 && in.Budget < 200*time.Millisecond {
		level = pngopt.Fast
	}
	opt, err := pngopt.Optimize(in.Raw, level)
	if err != nil {
		return nil, ErrUnsupported
	}
	base := &Result{Data: opt, Format: PNG, Lossless: true, SSIM: 1, Note: "png optimized"}
	if info.NeedsColorManagement() {
		return base, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	src, err := png.Decode(bytes.NewReader(in.Raw))
	if err != nil {
		return base, nil
	}
	return e.optimizeStill(ctx, in, src, base, info.BitDepth > 8)
}

// optimizeStill chooses between lossless and lossy recoding of a decoded
// picture that came from a palette or PNG source, and returns the best of them
// and base.
func (e *Engine) optimizeStill(ctx context.Context, in Input, src image.Image, base *Result, deep bool) (*Result, error) {
	p := in.Profile
	best := base

	// Pictures with few colours are graphics: they stay lossless in every
	// profile that is not asked to reduce them. Many colours is a photograph.
	_, photo := pngopt.UniqueColors(src, p.PhotoColors)
	if p.Lossless {
		photo = false
	}

	if photo {
		res, err := e.lossy(ctx, in, src, 1, best)
		if err != nil && !errors.Is(err, ErrBudget) {
			return best, nil
		}
		if res != nil && len(res.Data) < len(best.Data) {
			best = res
		}
		return best, nil
	}

	if !deep {
		if res := e.losslessModern(ctx, in, src); res != nil && len(res.Data) < len(best.Data) {
			best = res
		}
	}
	if p.QuantizePNG && !p.Lossless {
		if res := e.quantized(ctx, in, src); res != nil && len(res.Data) < len(best.Data) {
			best = res
		}
	}
	return best, nil
}

// losslessModern encodes pic with the best lossless format the client has.
func (e *Engine) losslessModern(ctx context.Context, in Input, src image.Image) *Result {
	pic, _ := Normalize(src)
	pixels := pic.Bounds().Dx() * pic.Bounds().Dy()
	for _, f := range []Format{JXL, WebP} {
		if !in.Caps.Has(f) || !e.be.Supports(f) || ctx.Err() != nil {
			continue
		}
		// lossless encoding costs roughly twice a lossy one
		if in.Budget > 0 && 2*e.attemptCost(f, pixels) > in.Budget {
			continue
		}
		data, err := e.be.Encode(f, pic, EncodeOptions{Lossless: true, Effort: e.losslessEffort(f, pixels)})
		if err != nil {
			continue
		}
		return &Result{Data: data, Format: f, Lossless: true, SSIM: 1, Attempts: 1, Note: f.String() + " lossless"}
	}
	return nil
}

func (e *Engine) losslessEffort(f Format, pixels int) int {
	if f == JXL {
		return 3
	}
	if pixels > 1_000_000 {
		return 3 // libwebp method 1
	}
	return 5
}

// quantized reduces a graphic to a 256-colour PNG when the result still
// resembles the source closely enough.
func (e *Engine) quantized(ctx context.Context, in Input, src image.Image) *Result {
	if ctx.Err() != nil {
		return nil
	}
	pal, err := pngopt.Quantize(src, pngopt.QuantizeOptions{Colors: 256, Dither: 0.4})
	if err != nil {
		return nil
	}
	ref, err := quality.NewReference(src)
	if err != nil {
		return nil
	}
	s, err := ref.Similarity(pal)
	if err != nil || s < in.Profile.TargetSSIM {
		return nil
	}
	data, err := pngopt.Encode(pal, pngopt.Default)
	if err != nil {
		return nil
	}
	return &Result{Data: data, Format: PNG, SSIM: s, Attempts: 1, Note: "png 256 colours"}
}

// Warm runs a tiny job through every codec of the backend. The first use of a
// WebAssembly codec compiles it, which takes a good fraction of a second, and
// that cost should not land on a request.
func (e *Engine) Warm() {
	m := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for i := range m.Pix {
		m.Pix[i] = 0xff
	}
	for _, f := range []Format{JPEG, WebP, AVIF, JXL} {
		if !e.be.Supports(f) {
			continue
		}
		if data, err := e.be.Encode(f, m, EncodeOptions{Quality: 80, Effort: 1}); err == nil {
			_, _ = e.be.Decode(f, data)
		}
	}
}
