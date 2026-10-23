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

// checkModern says whether a WebP, AVIF or JPEG XL file can be decoded and
// recoded without changing what a browser shows. A browser reads things from
// the container that the pixels do not carry: a colour profile, an orientation,
// a crop, an animation. A recode would drop them, so a file that has any is left
// alone. Colour is never guessed.
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
			return whyNotModern("not plain sRGB without alpha")
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
	if err := checkModern(in.Format, in.Raw); err != nil {
		return nil, err
	}
	if !e.be.Supports(in.Format) {
		return nil, whyNotModern("no decoder")
	}
	src, err := e.be.Decode(in.Format, in.Raw)
	if err != nil {
		return nil, whyNotModern("not decodable")
	}
	base := &Result{Data: in.Raw, Format: in.Format, Lossless: true, Note: "original"}
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
