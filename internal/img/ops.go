package img

import (
	"image"
	"image/draw"

	xdraw "golang.org/x/image/draw"
)

// Normalize returns m in the representation the encoders take: *image.RGBA when
// the image is opaque and *image.NRGBA when it has transparency, both with the
// origin at (0,0). It reports whether there is any transparency. An image that
// already is in that form is returned as is.
func Normalize(m image.Image) (image.Image, bool) {
	opaque := IsOpaque(m)
	b := m.Bounds()
	switch v := m.(type) {
	case *image.RGBA:
		if opaque && b.Min == (image.Point{}) {
			return v, false
		}
	case *image.NRGBA:
		if !opaque && b.Min == (image.Point{}) {
			return v, true
		}
	}
	r := image.Rect(0, 0, b.Dx(), b.Dy())
	if opaque {
		dst := image.NewRGBA(r)
		draw.Draw(dst, r, m, b.Min, draw.Src)
		return dst, false
	}
	dst := image.NewNRGBA(r)
	draw.Draw(dst, r, m, b.Min, draw.Src)
	return dst, true
}

// IsOpaque reports whether every pixel of m is fully opaque.
func IsOpaque(m image.Image) bool {
	switch v := m.(type) {
	case *image.YCbCr, *image.Gray, *image.Gray16, *image.CMYK:
		return true
	case interface{ Opaque() bool }:
		return v.Opaque()
	}
	b := m.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := m.At(x, y).RGBA(); a != 0xffff {
				return false
			}
		}
	}
	return true
}

// pixel size of both representations Normalize returns
const pixelBytes = 4

// Orient applies an EXIF orientation (1..8) to the pixels, so that the result
// can be saved with no orientation tag and still display upright.
func Orient(m image.Image, orientation int) image.Image {
	if orientation < 2 || orientation > 8 {
		return m
	}
	norm, alpha := Normalize(m)
	var pix []byte
	var stride int
	switch v := norm.(type) {
	case *image.RGBA:
		pix, stride = v.Pix, v.Stride
	case *image.NRGBA:
		pix, stride = v.Pix, v.Stride
	}
	sw, sh := norm.Bounds().Dx(), norm.Bounds().Dy()
	dw, dh := sw, sh
	if orientation >= 5 {
		dw, dh = sh, sw
	}

	out := make([]byte, dw*dh*pixelBytes)
	for y := range dh {
		row := out[y*dw*pixelBytes : (y+1)*dw*pixelBytes]
		for x := range dw {
			var sx, sy int
			switch orientation {
			case 2:
				sx, sy = sw-1-x, y
			case 3:
				sx, sy = sw-1-x, sh-1-y
			case 4:
				sx, sy = x, sh-1-y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, sh-1-x
			case 7:
				sx, sy = sw-1-y, sh-1-x
			case 8:
				sx, sy = sw-1-y, x
			}
			copy(row[x*pixelBytes:x*pixelBytes+pixelBytes], pix[sy*stride+sx*pixelBytes:sy*stride+sx*pixelBytes+pixelBytes])
		}
	}
	r := image.Rect(0, 0, dw, dh)
	if alpha {
		return &image.NRGBA{Pix: out, Stride: dw * pixelBytes, Rect: r}
	}
	return &image.RGBA{Pix: out, Stride: dw * pixelBytes, Rect: r}
}

// Resize scales m to w by h with a Catmull-Rom filter, which keeps detail when
// shrinking. The result is *image.RGBA or *image.NRGBA like Normalize.
func Resize(m image.Image, w, h int) image.Image {
	opaque := IsOpaque(m)
	r := image.Rect(0, 0, w, h)
	if opaque {
		dst := image.NewRGBA(r)
		xdraw.CatmullRom.Scale(dst, r, m, m.Bounds(), xdraw.Src, nil)
		return dst
	}
	dst := image.NewNRGBA(r)
	xdraw.CatmullRom.Scale(dst, r, m, m.Bounds(), xdraw.Src, nil)
	return dst
}

// Hints describe how the client will show the image. They come from the
// browser or from the MidoPress extension and are optional.
type Hints struct {
	// ViewportWidth is the width of the viewport in CSS pixels, 0 when unknown.
	ViewportWidth int
	// DPR is the device pixel ratio, 0 when unknown.
	DPR float64
}

// scaledSize returns the size an image of w by h should be reduced to for a
// client with these hints, or ok=false to keep it. It never enlarges, and it
// only shrinks when the saving is real: a width within 15% of the target is
// kept, because resampling for a few pixels costs quality for nothing.
func scaledSize(w, h int, hints Hints) (nw, nh int, ok bool) {
	if hints.ViewportWidth <= 0 {
		return w, h, false
	}
	dpr := hints.DPR
	if dpr < 1 {
		dpr = 1
	}
	target := int(float64(hints.ViewportWidth)*dpr + 0.5)
	if target < 64 || w*100 <= target*115 {
		return w, h, false
	}
	nh = max((h*target+w/2)/w, 1)
	return target, nh, true
}
