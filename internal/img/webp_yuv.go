package img

import "image"

// The WebP decoders of gen2brain hand back the YUV planes of libwebp as an
// image.NYCbCrA. Go would turn those into RGB as JPEG does, with full range
// samples, but WebP uses the studio range of BT.601 (16 to 235), and its decoder
// smooths the chroma when it enlarges it instead of repeating it. Measuring the
// result of an encoder through that conversion makes every WebP look much worse
// than it is, and a browser never shows it like that. This does what libwebp
// does, with its constants.

func mulHi(v, coeff int) int { return (v * coeff) >> 8 }

func clip8(v int) uint8 {
	if v&^16383 == 0 {
		return uint8(v >> 6)
	}
	if v < 0 {
		return 0
	}
	return 255
}

func yuvToRGB(y, u, v int) (r, g, b uint8) {
	yy := mulHi(y, 19077)
	r = clip8(yy + mulHi(v, 26149) - 14234)
	g = clip8(yy - mulHi(u, 6419) - mulHi(v, 13320) + 8708)
	b = clip8(yy + mulHi(u, 33050) - 17685)
	return
}

// webpToRGBA converts what the WebP decoders return into the *image.RGBA or
// *image.NRGBA the engine works with. Images that are not 4:2:0 are returned as
// they are.
func webpToRGBA(m image.Image) image.Image {
	var yc *image.YCbCr
	var alpha []uint8
	var astride int
	switch v := m.(type) {
	case *image.NYCbCrA:
		yc, alpha, astride = &v.YCbCr, v.A, v.AStride
	case *image.YCbCr:
		yc = v
	default:
		return m
	}
	if yc.SubsampleRatio != image.YCbCrSubsampleRatio420 {
		return m
	}
	b := yc.Rect
	w, h := b.Dx(), b.Dy()
	cw, ch := (b.Max.X+1)/2-b.Min.X/2, (b.Max.Y+1)/2-b.Min.Y/2
	opaque := true
	for _, a := range alpha {
		if a != 255 {
			opaque = false
			break
		}
	}
	if alpha == nil {
		opaque = true
	}
	var pix []byte
	stride := w * 4
	pix = make([]byte, stride*h)

	// chroma sample at column cx and row cy of the plane, clamped to its edges
	cb := func(cx, cy int) int {
		cx, cy = min(max(cx, 0), cw-1), min(max(cy, 0), ch-1)
		return int(yc.Cb[cy*yc.CStride+cx])
	}
	cr := func(cx, cy int) int {
		cx, cy = min(max(cx, 0), cw-1), min(max(cy, 0), ch-1)
		return int(yc.Cr[cy*yc.CStride+cx])
	}
	for y := range h {
		cy := y / 2
		vy := cy - 1 + 2*(y&1) // the other row that weighs in: above for even rows, below for odd
		for x := range w {
			cx := x / 2
			hx := cx - 1 + 2*(x&1)
			u := (9*cb(cx, cy) + 3*cb(hx, cy) + 3*cb(cx, vy) + cb(hx, vy) + 8) >> 4
			v := (9*cr(cx, cy) + 3*cr(hx, cy) + 3*cr(cx, vy) + cr(hx, vy) + 8) >> 4
			r, g, bl := yuvToRGB(int(yc.Y[y*yc.YStride+x]), u, v)
			o := y*stride + x*4
			pix[o], pix[o+1], pix[o+2] = r, g, bl
			if opaque {
				pix[o+3] = 255
			} else {
				pix[o+3] = alpha[y*astride+x]
			}
		}
	}
	rect := image.Rect(0, 0, w, h)
	if opaque {
		return &image.RGBA{Pix: pix, Stride: stride, Rect: rect}
	}
	return &image.NRGBA{Pix: pix, Stride: stride, Rect: rect}
}
