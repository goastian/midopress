package img

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// ProfileOverride changes a built-in profile or defines a new one on top of it.
// Every field is optional: what is not set is taken from the base profile.
type ProfileOverride struct {
	// Base is the profile to start from, balanced when empty. For a built-in
	// name it defaults to that profile itself.
	Base string `yaml:"base" json:"base,omitempty"`

	TargetDSSIM       *float64      `yaml:"target_dssim" json:"target_dssim,omitempty"`
	TargetSSIM        *float64      `yaml:"target_ssim" json:"target_ssim,omitempty"`
	TargetSSIMULACRA2 *float64      `yaml:"target_ssimulacra2" json:"target_ssimulacra2,omitempty"`
	Attempts          *int          `yaml:"attempts" json:"attempts,omitempty"`
	Formats           []string      `yaml:"formats" json:"formats,omitempty"`
	ResizeToViewport  *bool         `yaml:"resize_to_viewport" json:"resize_to_viewport,omitempty"`
	PhotoColors       *int          `yaml:"photo_colors" json:"photo_colors,omitempty"`
	QuantizePNG       *bool         `yaml:"quantize_png" json:"quantize_png,omitempty"`
	AnimatedToStill   *bool         `yaml:"animated_to_still" json:"animated_to_still,omitempty"`
	AnimatedQuality   *int          `yaml:"animated_quality" json:"animated_quality,omitempty"`
	AVIFEffort        *int          `yaml:"avif_effort" json:"avif_effort,omitempty"`
	NoTransform       *string       `yaml:"no_transform" json:"no_transform,omitempty"`
	MinifyJS          *bool         `yaml:"minify_js" json:"minify_js,omitempty"`
	Text              *TextOverride `yaml:"text" json:"text,omitempty"`
}

// TextOverride changes the text compression levels of a profile.
type TextOverride struct {
	Brotli       *int `yaml:"brotli" json:"brotli,omitempty"`
	Gzip         *int `yaml:"gzip" json:"gzip,omitempty"`
	Zstd         *int `yaml:"zstd" json:"zstd,omitempty"`
	BrotliStatic *int `yaml:"brotli_static" json:"brotli_static,omitempty"`
	StaticWait   *int `yaml:"static_wait_ms" json:"static_wait_ms,omitempty"`
}

// ParseFormat reads the name of a modern format: jxl, avif or webp.
func ParseFormat(s string) (Format, error) {
	switch s {
	case "jxl":
		return JXL, nil
	case "avif":
		return AVIF, nil
	case "webp":
		return WebP, nil
	}
	return Unknown, fmt.Errorf("unknown format %q, use jxl, avif or webp", s)
}

// BuildProfiles returns the built-in profiles with the overrides applied, and
// the profiles that the overrides define. It reports every problem it finds.
func BuildProfiles(overrides map[string]ProfileOverride) (map[string]*Profile, error) {
	out := Profiles()
	names := make([]string, 0, len(overrides))
	for n := range overrides {
		names = append(names, n)
	}
	sort.Strings(names)

	var errs []error
	// a profile may be based on another that is also overridden, so resolve in
	// dependency order and stop on cycles
	done := map[string]bool{}
	var resolve func(name string, stack []string)
	resolve = func(name string, stack []string) {
		if done[name] {
			return
		}
		o, ok := overrides[name]
		if !ok {
			return
		}
		for _, s := range stack {
			if s == name {
				errs = append(errs, fmt.Errorf("profile %q: base cycle %v", name, append(stack, name)))
				done[name] = true
				return
			}
		}
		base := o.Base
		if base == "" {
			base = name
			if _, builtin := out[name]; !builtin {
				base = ProfileBalanced
			}
		}
		if base != name {
			resolve(base, append(stack, name))
		}
		src, ok := out[base]
		if !ok {
			errs = append(errs, fmt.Errorf("profile %q: unknown base %q", name, base))
			done[name] = true
			return
		}
		p := *src
		p.Formats = append([]Format(nil), src.Formats...)
		p.Name = name
		if err := apply(&p, o); err != nil {
			errs = append(errs, fmt.Errorf("profile %q: %w", name, err))
		}
		out[name] = &p
		done[name] = true
	}
	for _, n := range names {
		resolve(n, nil)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

func apply(p *Profile, o ProfileOverride) error {
	var errs []error
	check := func(name string, v *float64, lo, hi float64) {
		if v == nil {
			return
		}
		if math.IsNaN(*v) || *v < lo || *v > hi {
			errs = append(errs, fmt.Errorf("%s must be between %g and %g", name, lo, hi))
		}
	}
	check("target_dssim", o.TargetDSSIM, 0.0001, 1)
	check("target_ssim", o.TargetSSIM, 0.5, 1)
	check("target_ssimulacra2", o.TargetSSIMULACRA2, 0, 100)
	if o.TargetDSSIM != nil {
		p.TargetDSSIM = *o.TargetDSSIM
	}
	if o.TargetSSIM != nil {
		p.TargetSSIM = *o.TargetSSIM
	}
	if o.TargetSSIMULACRA2 != nil {
		p.TargetSSIMULACRA2 = *o.TargetSSIMULACRA2
	}
	if o.Attempts != nil {
		if *o.Attempts < 1 || *o.Attempts > 8 {
			errs = append(errs, errors.New("attempts must be between 1 and 8"))
		} else {
			p.Attempts = *o.Attempts
		}
	}
	if o.Formats != nil {
		var fs []Format
		for _, n := range o.Formats {
			f, err := ParseFormat(n)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			fs = append(fs, f)
		}
		p.Formats = fs
	}
	if o.ResizeToViewport != nil {
		p.ResizeToViewport = *o.ResizeToViewport
	}
	if o.PhotoColors != nil {
		if *o.PhotoColors < 1 {
			errs = append(errs, errors.New("photo_colors must be positive"))
		} else {
			p.PhotoColors = *o.PhotoColors
		}
	}
	if o.QuantizePNG != nil {
		p.QuantizePNG = *o.QuantizePNG
	}
	if o.AnimatedToStill != nil {
		p.AnimatedToStill = *o.AnimatedToStill
	}
	if o.AnimatedQuality != nil {
		if *o.AnimatedQuality < 0 || *o.AnimatedQuality > 100 {
			errs = append(errs, errors.New("animated_quality must be between 0 and 100"))
		} else {
			p.AnimatedQuality = *o.AnimatedQuality
		}
	}
	if o.AVIFEffort != nil {
		if *o.AVIFEffort < 0 || *o.AVIFEffort > 10 {
			errs = append(errs, errors.New("avif_effort must be between 0 (the engine chooses) and 10"))
		} else {
			p.AVIFEffort = *o.AVIFEffort
		}
	}
	if o.NoTransform != nil {
		switch *o.NoTransform {
		case NoTransformRespect, NoTransformImages, NoTransformAll:
			p.NoTransform = *o.NoTransform
		default:
			errs = append(errs, errors.New("no_transform must be respect, images or all"))
		}
	}
	if o.MinifyJS != nil {
		p.MinifyJS = *o.MinifyJS
	}
	if t := o.Text; t != nil {
		set := func(name string, v *int, dst *int, lo, hi int) {
			if v == nil {
				return
			}
			if *v < lo || *v > hi {
				errs = append(errs, fmt.Errorf("text.%s must be between %d and %d", name, lo, hi))
				return
			}
			*dst = *v
		}
		set("brotli", t.Brotli, &p.Text.Brotli, 0, 11)
		set("gzip", t.Gzip, &p.Text.Gzip, 1, 9)
		set("zstd", t.Zstd, &p.Text.Zstd, 1, 22)
		set("brotli_static", t.BrotliStatic, &p.Text.BrotliStatic, 0, 11)
		set("static_wait_ms", t.StaticWait, &p.Text.StaticWaitMS, 0, 60000)
	}
	if p.Lossless {
		// a lossless profile never searches
		p.TargetSSIM, p.TargetSSIMULACRA2, p.TargetDSSIM = 1, 100, 0
	}
	return errors.Join(errs...)
}
