package img

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
)

func (e *Engine) optimizeEffects(ctx context.Context, in Input) (*Result, error) {
	if in.Profile.Lossless || ctx.Err() != nil {
		return nil, ErrUnsupported
	}
	src, err := DecodeDisplay(ctx, e.be, in.Format, in.Raw)
	if err != nil {
		return nil, err
	}
	resized := false
	if w := in.Profile.PlaceholderWidth; w > 0 && src.Bounds().Dx() > w {
		src = Resize(src, w, max(1, src.Bounds().Dy()*w/src.Bounds().Dx()))
		resized = true
	}
	if in.Profile.Grayscale {
		b := src.Bounds()
		gray := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		for y := 0; y < b.Dy(); y++ {
			for x := 0; x < b.Dx(); x++ {
				c := color.NRGBAModel.Convert(src.At(x+b.Min.X, y+b.Min.Y)).(color.NRGBA)
				v := color.GrayModel.Convert(color.RGBA{R: c.R, G: c.G, B: c.B, A: 255}).(color.Gray).Y
				gray.SetNRGBA(x, y, color.NRGBA{R: v, G: v, B: v, A: c.A})
			}
		}
		src = gray
	}
	var b bytes.Buffer
	if err := png.Encode(&b, src); err != nil {
		return nil, err
	}
	profile := *in.Profile
	profile.Grayscale, profile.PlaceholderWidth = false, 0
	res, err := e.Optimize(ctx, Input{Raw: b.Bytes(), Format: PNG, Caps: in.Caps, Profile: &profile,
		Hints: in.Hints, Fixed: in.Fixed, Metric: in.Metric, VerifyQuality: in.VerifyQuality, OutputFormat: in.OutputFormat, Budget: in.Budget})
	if err != nil {
		return nil, err
	}
	res.Intentional, res.Lossless, res.Resized = true, false, res.Resized || resized
	res.Metric, res.Measure = "", 0
	res.Note = "explicit appearance change"
	return res, nil
}
