package img

import (
	"bytes"
	"context"
	"image"
	"image/draw"
	stdgif "image/gif"
	"time"

	"github.com/goastian/midopress/internal/pngopt"
)

// maxAnimationPixels bounds frames times pixels, which is the memory the
// composed animation takes (4 bytes each).
const maxAnimationPixels = 48_000_000

func (e *Engine) optimizeGIF(ctx context.Context, in Input) (*Result, error) {
	if NeedsColorConversion(in.Raw, GIF) {
		return nil, whyNotModern("GIF ICC needs managed animation frames")
	}
	if in.Profile.AnimatedToStill {
		p := *in.Profile
		p.AnimatedToStill = false
		in.Profile = &p
	}
	g, err := stdgif.DecodeAll(bytes.NewReader(in.Raw))
	if err != nil || len(g.Image) == 0 {
		return nil, ErrUnsupported
	}
	w, h := g.Config.Width, g.Config.Height
	if w <= 0 || h <= 0 {
		return nil, ErrUnsupported
	}
	if len(g.Image) > 1 && !in.Profile.AnimatedToStill {
		return e.optimizeAnimation(ctx, in, g)
	}

	// A still image: the first frame, or the only one.
	frames := composeFrames(g, 1)
	still := frames[0]
	base := &Result{Data: in.Raw, Format: GIF, Lossless: true, Note: "original gif"}
	if opt := e.stillAsPNG(still); opt != nil && len(opt.Data) < len(base.Data) {
		base = opt
	}
	if len(g.Image) > 1 {
		// Dropping the animation is a loss that the profile allowed, but the
		// measured similarity must say so honestly.
		base.Lossless = false
		base.Note += ", first frame only"
	}
	res, err := e.optimizeStill(ctx, in, still, base, false)
	if err == nil && res != nil && len(g.Image) > 1 {
		res.Lossless = false
		res.Note += ", first frame only"
	}
	return res, err
}

// stillAsPNG saves a picture as an optimized PNG, which is almost always
// smaller than the same picture as a GIF.
func (e *Engine) stillAsPNG(pic image.Image) *Result {
	data, err := pngopt.Encode(pic, pngopt.Default)
	if err != nil {
		return nil
	}
	return &Result{Data: data, Format: PNG, Lossless: true, Note: "gif as png"}
}

func (e *Engine) optimizeAnimation(ctx context.Context, in Input, g *stdgif.GIF) (*Result, error) {
	pixels := int64(g.Config.Width) * int64(g.Config.Height) * int64(len(g.Image))
	if pixels <= 0 || pixels > maxAnimationPixels {
		return nil, ErrBudget
	}
	if (!in.Caps.WebP || !e.be.Supports(WebP)) && (!in.Caps.JXL || !e.be.Supports(JXL)) {
		return nil, ErrUnsupported
	}
	return e.optimizeFrames(ctx, in, &Animation{
		Frames: composeFrames(g, len(g.Image)), DelaysMS: browserDelays(g.Delay), LoopCount: webpLoops(g.LoopCount),
	})
}

func (e *Engine) optimizeFrames(ctx context.Context, in Input, anim *Animation) (*Result, error) {
	in.VerifyQuality = true
	if len(anim.Frames) == 0 || len(anim.DelaysMS) != len(anim.Frames) || anim.LoopCount < 0 {
		return nil, ErrUnsupported
	}
	size := anim.Frames[0].Bounds().Size()
	pixels64 := int64(size.X) * int64(size.Y) * int64(len(anim.Frames))
	if pixels64 <= 0 || pixels64 > maxAnimationPixels {
		return nil, ErrBudget
	}
	for i, frame := range anim.Frames {
		if frame.Bounds().Size() != size || anim.DelaysMS[i] <= 0 {
			return nil, ErrUnsupported
		}
	}
	f := WebP
	if !in.Caps.WebP || !e.be.Supports(WebP) {
		f = JXL
		if !in.Caps.JXL || !e.be.Supports(JXL) {
			return nil, ErrUnsupported
		}
	}
	p := in.Profile
	pixels := int(pixels64)
	remaining := in.Budget
	fits := func(cost time.Duration) bool { return in.Budget <= 0 || cost <= remaining }
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var best *Result
	try := func(lossless bool, q int) {
		start := time.Now()
		data, err := e.be.EncodeAnimation(f, anim, EncodeOptions{Lossless: lossless, Quality: q, Effort: e.losslessEffort(f, pixels)})
		remaining -= time.Since(start)
		if err != nil {
			return
		}
		var measure float64
		var metric Metric
		if in.VerifyQuality {
			decoder, ok := e.be.(AnimationDecoder)
			if !ok {
				return
			}
			decoded, err := decoder.DecodeAnimation(f, data)
			if err != nil || len(decoded.Frames) != len(anim.Frames) || len(decoded.DelaysMS) != len(anim.DelaysMS) || decoded.LoopCount != anim.LoopCount {
				return
			}
			worst := 1e30
			for i, frame := range decoded.Frames {
				if ctx.Err() != nil || decoded.DelaysMS[i] != anim.DelaysMS[i] {
					return
				}
				if lossless {
					if !samePixels(frame, anim.Frames[i]) {
						return
					}
					continue
				}
				metric = in.Metric
				if metric == "" {
					metric = e.Metric
				}
				sc, err := newScorer(metric, anim.Frames[i], in.Profile, true)
				if err != nil {
					return
				}
				score, err := sc.eval(frame)
				if err != nil || score < sc.target {
					return
				}
				if score < worst {
					worst, measure = score, sc.natural(score)
				}
			}
		}
		r := &Result{Data: data, Format: f, Lossless: lossless, Attempts: 1, Quality: q, Note: "animated " + f.String()}
		r.Metric, r.Measure = metric, measure
		if lossless {
			r.Note += " lossless"
		}
		if best == nil || len(r.Data) < len(best.Data) {
			best = r
		}
	}

	switch {
	case in.Fixed > 0 && !p.Lossless:
		if fits(e.attemptCost(f, pixels)) {
			try(false, in.Fixed)
		}
	default:
		// Lossless is exact and, for the flat colours GIFs usually have, often
		// the smaller. Lossy only gets a go when lossless left a lot on the table.
		if fits(2 * e.attemptCost(f, pixels)) {
			try(true, 0)
		}
		worthLossy := best == nil || len(best.Data)*100 > len(in.Raw)*35
		if !p.Lossless && p.AnimatedQuality > 0 && worthLossy && fits(e.attemptCost(f, pixels)) && ctx.Err() == nil {
			try(false, p.AnimatedQuality)
		}
	}
	if best == nil {
		return nil, ErrBudget
	}
	return best, nil
}

// composeFrames draws the first n frames of a GIF onto a canvas, honouring the
// disposal method of each, and returns every frame as the full picture a viewer
// sees at that moment.
func composeFrames(g *stdgif.GIF, n int) []image.Image {
	bounds := image.Rect(0, 0, g.Config.Width, g.Config.Height)
	canvas := image.NewNRGBA(bounds)
	var previous *image.NRGBA
	out := make([]image.Image, 0, n)
	for i := range n {
		frame := g.Image[i]
		disposal := byte(stdgif.DisposalNone)
		if i < len(g.Disposal) {
			disposal = g.Disposal[i]
		}
		if disposal == stdgif.DisposalPrevious {
			previous = cloneNRGBA(canvas)
		}
		draw.Draw(canvas, frame.Bounds(), frame, frame.Bounds().Min, draw.Over)
		out = append(out, cloneNRGBA(canvas))
		switch disposal {
		case stdgif.DisposalBackground:
			draw.Draw(canvas, frame.Bounds(), image.Transparent, image.Point{}, draw.Src)
		case stdgif.DisposalPrevious:
			if previous != nil {
				copy(canvas.Pix, previous.Pix)
			}
		}
	}
	return out
}

func cloneNRGBA(m *image.NRGBA) *image.NRGBA {
	c := &image.NRGBA{Pix: make([]byte, len(m.Pix)), Stride: m.Stride, Rect: m.Rect}
	copy(c.Pix, m.Pix)
	return c
}

// browserDelays converts GIF delays (hundredths of a second) to milliseconds.
// Browsers show a frame with a delay of 0 or 1 for 100 ms, so a WebP made from
// it has to say so or the animation would play ten times too fast.
func browserDelays(d []int) []int {
	out := make([]int, len(d))
	for i, v := range d {
		if v <= 1 {
			v = 10
		}
		out[i] = v * 10
	}
	return out
}

// webpLoops converts the loop count of a GIF. Go uses -1 for "play once" and 0
// for "forever", and any other value n means n+1 plays; WebP counts plays.
func webpLoops(gifLoops int) int {
	switch {
	case gifLoops < 0:
		return 1
	case gifLoops == 0:
		return 0
	}
	return gifLoops + 1
}
