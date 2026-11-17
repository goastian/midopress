package img

import (
	"context"
	"fmt"

	"github.com/goastian/midopress/internal/imgmeta"
)

// isModern reports whether f is one of the formats this package produces, as
// opposed to the ones that come from cameras and editors.
func isModern(f Format) bool { return f == WebP || f == AVIF || f == JXL }

// whyNotModern says why a file is left alone, as an error that is
// ErrUnsupported: the reason is one of a short list, so that it can be counted.
func whyNotModern(why string) error { return fmt.Errorf("%w: %s", ErrUnsupported, why) }

// checkModern identifies the conservative plain-sRGB fast path. The managed
// recoding path uses DecodeDisplay for color and presentation metadata.
func checkModern(f Format, raw []byte) error {
	switch f {
	case WebP:
		w, err := imgmeta.ParseWebP(raw)
		switch {
		case err != nil:
			return whyNotModern("unreadable header")
		case w.Animated:
			return whyNotModern("animated")
		case w.Orientation != 1:
			return whyNotModern("orientation")
		case w.ICC != nil && imgmeta.ClassifyICC(w.ICC) != imgmeta.ICCsRGB:
			return whyNotModern("colour profile")
		}
		return nil
	case AVIF:
		a, err := imgmeta.ParseAVIF(raw)
		switch {
		case err != nil:
			return whyNotModern("unreadable header")
		case a.Animated:
			return whyNotModern("animated")
		case a.Transformed:
			return whyNotModern("rotated, mirrored or cropped")
		case a.ICC != nil && imgmeta.ClassifyICC(a.ICC) != imgmeta.ICCsRGB:
			return whyNotModern("colour profile")
		}
		for _, c := range a.NCLX {
			if !c.SDRsRGB() {
				return whyNotModern("colour space")
			}
		}
		return nil
	case JXL:
		if _, err := imgmeta.ParseJXL(raw); err != nil {
			return whyNotModern("unreadable JPEG XL")
		}
		return nil
	}
	return whyNotModern("format")
}

// optimizeModern recodes a WebP or AVIF picture that a site already sends. It
// is measured the way a JPEG is, against the decoded source, so the result is
// as close to what the site sent as the profile allows. Most of these files
// already are well compressed, and the engine says so by returning the original:
// the answer is "nothing smaller that is still the same picture" far more often
// than for a JPEG, which is why the callers remember it.
func (e *Engine) optimizeModern(ctx context.Context, in Input) (*Result, error) {
	if in.Profile.Lossless {
		return nil, whyNotModern("lossless profile")
	}
	animated := false
	switch in.Format {
	case WebP:
		meta, err := imgmeta.ParseWebP(in.Raw)
		animated = err == nil && meta.Animated
	case AVIF:
		meta, err := imgmeta.ParseAVIF(in.Raw)
		animated = err == nil && meta.Animated
	case JXL:
		meta, err := imgmeta.ParseJXL(in.Raw)
		if err == nil && jxlHasUnsupportedChannels(meta) {
			return &Result{Data: in.Raw, Format: JXL, Lossless: true, Note: "original auxiliary channels preserved"}, nil
		}
		if err == nil && jxlHasUnclassifiedTransfer(meta) {
			return &Result{Data: in.Raw, Format: JXL, Lossless: true, Note: "original unclassified color encoding preserved"}, nil
		}
		if err == nil && jxlNeedsToneMap(meta) {
			return nil, whyNotModern("JPEG XL HDR requires explicit tone mapping")
		}
		animated = err == nil && meta.Animated
	}
	base := &Result{Data: in.Raw, Format: in.Format, Lossless: true, Note: "original"}
	if animated {
		decoder, ok := e.be.(AnimationDecoder)
		if !ok {
			return nil, whyNotModern("animated format has no decoder")
		}
		// AVIF sequences and metadata-bearing WebP animation need a managed
		// animation decoder. JPEG XL's portable decoder already manages every frame.
		if in.Format != JXL {
			return nil, whyNotModern("animated format needs managed frames")
		}
		frames, err := decoder.DecodeAnimation(in.Format, in.Raw)
		if err != nil {
			return nil, whyNotModern("animated format is not decodable")
		}
		in.Caps.JXL = true
		result, err := e.optimizeFrames(ctx, in, frames)
		if err != nil || len(result.Data) >= len(base.Data) {
			return base, nil
		}
		if NeedsColorConversion(in.Raw, in.Format) {
			result.Lossless = false
		}
		return result, nil
	}
	src, err := DecodeDisplay(ctx, e.be, in.Format, in.Raw)
	if err != nil {
		return nil, err
	}
	// the client was sent this format, so it can show it whatever its Accept says
	switch in.Format {
	case WebP:
		in.Caps.WebP = true
	case AVIF:
		in.Caps.AVIF = true
	case JXL:
		in.Caps.JXL = true
	}
	return e.lossy(ctx, in, src, 1, base)
}
