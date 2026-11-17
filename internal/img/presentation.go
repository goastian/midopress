package img

import (
	"image"
	"image/color"
	"math"

	"github.com/goastian/midopress/internal/imgmeta"
)

func avifPresentation(src image.Image, transforms []imgmeta.AVIFTransform) (image.Image, error) {
	for _, t := range transforms {
		switch t.Kind {
		case "irot":
			src = Orient(src, []int{1, 8, 3, 6}[t.Value])
		case "imir":
			if t.Value == 0 {
				src = Orient(src, 4)
			} else {
				src = Orient(src, 2)
			}
		case "clap":
			p := t.Aperture
			if p[1] <= 0 || p[3] <= 0 || p[5] <= 0 || p[7] <= 0 {
				return nil, whyNotModern("invalid clean aperture")
			}
			w, h := float64(p[0])/float64(p[1]), float64(p[2])/float64(p[3])
			x := (float64(src.Bounds().Dx())-w)/2 + float64(p[4])/float64(p[5])
			y := (float64(src.Bounds().Dy())-h)/2 + float64(p[6])/float64(p[7])
			for _, v := range []float64{x, y, w, h} {
				if v != math.Trunc(v) || v < 0 {
					return nil, whyNotModern("fractional clean aperture")
				}
			}
			if w <= 0 || h <= 0 || x+w > float64(src.Bounds().Dx()) || y+h > float64(src.Bounds().Dy()) {
				return nil, whyNotModern("clean aperture outside image")
			}
			out := image.NewNRGBA64(image.Rect(0, 0, int(w), int(h)))
			for dy := 0; dy < int(h); dy++ {
				for dx := 0; dx < int(w); dx++ {
					out.SetNRGBA64(dx, dy, color.NRGBA64Model.Convert(src.At(src.Bounds().Min.X+int(x)+dx, src.Bounds().Min.Y+int(y)+dy)).(color.NRGBA64))
				}
			}
			src = out
		}
	}
	return src, nil
}
