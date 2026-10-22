//go:build vips

package img

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"sync"

	"github.com/davidbyttow/govips/v2/vips"
)

func init() {
	RegisterBackend("vips", func() (Backend, error) { return NewVips() })
}

var (
	vipsStartOnce sync.Once
	vipsStartErr  error
)

// startVips initialises libvips once per process. The proxy bounds its own
// parallelism, so libvips runs with a single worker thread per operation and
// without its operation cache, which would only pin decoded pixels in memory.
// vips.Shutdown is never called: libvips can not be restarted after it and the
// process owns the library until it exits.
func startVips() error {
	vipsStartOnce.Do(func() {
		vips.LoggingSettings(nil, vips.LogLevelError)
		vipsStartErr = vips.Startup(&vips.Config{
			ConcurrencyLevel: 1,
			MaxCacheMem:      0,
			MaxCacheFiles:    0,
			MaxCacheSize:     0,
		})
	})
	return vipsStartErr
}

// Vips is the optional backend built on libvips, available with the vips build
// tag. Which formats it encodes depends on the libvips build installed on the
// machine: it is probed once, at creation, by encoding a tiny image.
type Vips struct {
	encoders map[Format]bool
	decoders map[Format]bool
	// mozjpeg is true when the libvips build links mozjpeg, which is the case
	// when its extra jpegsave options change the output.
	mozjpeg bool
}

// NewVips starts libvips if needed and probes the codecs it was built with. Do
// not call it from a package init function: libvips deadlocks when it first
// runs before main has started.
func NewVips() (*Vips, error) {
	if err := startVips(); err != nil {
		return nil, fmt.Errorf("img: starting libvips: %w", err)
	}
	v := &Vips{encoders: map[Format]bool{}, decoders: map[Format]bool{}}
	probe := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for i := range probe.Pix {
		probe.Pix[i] = byte(i * 7)
		if i%4 == 3 {
			probe.Pix[i] = 255
		}
	}
	for _, f := range []Format{JPEG, WebP, AVIF, JXL} {
		data, err := v.Encode(f, probe, EncodeOptions{Quality: 80, Effort: 1})
		if err != nil {
			continue
		}
		v.encoders[f] = true
		if _, err := v.Decode(f, data); err == nil {
			v.decoders[f] = true
		}
	}
	v.mozjpeg = v.detectMozjpeg()
	return v, nil
}

// detectMozjpeg encodes a picture with and without the mozjpeg-only options:
// libjpeg-turbo ignores them, so identical bytes mean there is no mozjpeg.
func (v *Vips) detectMozjpeg() bool {
	if !v.encoders[JPEG] {
		return false
	}
	pic := photoProbe()
	plain, err := v.Encode(JPEG, pic, EncodeOptions{Quality: 75})
	if err != nil {
		return false
	}
	v.mozjpeg = true
	defer func() { v.mozjpeg = false }()
	moz, err := v.Encode(JPEG, pic, EncodeOptions{Quality: 75})
	return err == nil && !bytes.Equal(plain, moz)
}

func photoProbe() *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, 96, 96))
	x := uint32(12345)
	for i := 0; i < len(m.Pix); i += 4 {
		x = x*1664525 + 1013904223
		y := i / 4 / 96
		m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = byte(y*2+int(x>>28)), byte(255-y*2+int(x>>29)), byte(x>>26), 255
	}
	return m
}

func (*Vips) Name() string { return "vips" }

// Supports reports whether the installed libvips can encode f and read it back.
func (v *Vips) Supports(f Format) bool { return v.encoders[f] && v.decoders[f] }

// Native is true for every format: libvips and its codecs are C libraries.
func (*Vips) Native(Format) bool { return true }

// vipsImage converts pixels to a libvips image: 3 bands when m is opaque, so
// that no needless alpha plane reaches the encoder, 4 bands with straight alpha
// otherwise. The caller must Close it.
func vipsImage(m image.Image) (*vips.ImageRef, error) {
	norm, alpha := Normalize(m)
	b := norm.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return nil, errors.New("img: empty image")
	}
	if alpha {
		return vips.NewImageFromMemory(norm.(*image.NRGBA).Pix, w, h, 4, vips.BandFormatUchar, vips.InterpretationSRGB)
	}
	src := norm.(*image.RGBA)
	rgb := make([]byte, w*h*3)
	for y := range h {
		row := src.Pix[y*src.Stride : y*src.Stride+w*4]
		out := rgb[y*w*3 : (y+1)*w*3]
		for x := range w {
			out[x*3], out[x*3+1], out[x*3+2] = row[x*4], row[x*4+1], row[x*4+2]
		}
	}
	return vips.NewImageFromMemory(rgb, w, h, 3, vips.BandFormatUchar, vips.InterpretationSRGB)
}

// vipsJXLDistance is libjxl's mapping from a quality of 1..100 to a Butteraugli
// distance.
func vipsJXLDistance(q int) float64 {
	qf := float64(max(1, min(q, 100)))
	switch {
	case qf >= 100:
		return 0
	case qf >= 30:
		return 0.1 + (100-qf)*0.09
	}
	return 53.0/3000.0*qf*qf - 23.0/20.0*qf + 25.0
}

func (v *Vips) Encode(f Format, m image.Image, o EncodeOptions) ([]byte, error) {
	ref, err := vipsImage(m)
	if err != nil {
		return nil, err
	}
	defer ref.Close()
	return v.export(f, ref, o)
}

func (v *Vips) export(f Format, ref *vips.ImageRef, o EncodeOptions) ([]byte, error) {
	var data []byte
	var err error
	switch f {
	case JPEG:
		p := vips.NewJpegExportParams()
		p.StripMetadata = true
		p.Quality = o.Quality
		p.Interlace = o.Progressive
		p.OptimizeCoding = true
		// image.YCbCrSubsampleRatio444 is the zero value, which EncodeOptions
		// defines as unset (4:2:0), so full chroma can not be requested.
		p.SubsampleMode = vips.VipsForeignSubsampleOn
		if v.mozjpeg {
			p.TrellisQuant = true
			p.OvershootDeringing = true
			p.OptimizeScans = o.Progressive
		}
		data, _, err = ref.ExportJpeg(p)
	case WebP:
		p := vips.NewWebpExportParams()
		p.StripMetadata = true
		p.Quality = o.Quality
		p.Lossless = o.Lossless
		p.ReductionEffort = webpMethod(o.Effort)
		data, _, err = ref.ExportWebp(p)
	case AVIF:
		p := vips.NewAvifExportParams()
		p.StripMetadata = true
		p.Quality = o.Quality
		p.Lossless = o.Lossless
		p.Effort = max(1, 9-avifSpeed(o.Effort))
		data, _, err = ref.ExportAvif(p)
	case JXL:
		p := vips.NewJxlExportParams()
		p.Lossless = o.Lossless
		p.Effort = min(jxlEffort(o.Effort), 9)
		p.Distance = vipsJXLDistance(o.Quality)
		if o.Lossless {
			p.Distance = 0
		}
		data, _, err = ref.ExportJxl(p)
	default:
		return nil, fmt.Errorf("img: the vips backend can not encode %s", f)
	}
	if err != nil {
		return nil, fmt.Errorf("img: vips %s: %w", f, err)
	}
	return data, nil
}

// EncodeAnimation only handles WebP: libvips can not write animated AVIF or JXL.
func (v *Vips) EncodeAnimation(f Format, a *Animation, o EncodeOptions) ([]byte, error) {
	if f != WebP {
		return nil, fmt.Errorf("img: the vips backend can not encode animated %s", f)
	}
	if len(a.Frames) == 0 {
		return nil, errors.New("img: animation without frames")
	}
	opaque := true
	for _, fr := range a.Frames {
		opaque = opaque && IsOpaque(fr)
	}
	refs := make([]*vips.ImageRef, 0, len(a.Frames))
	defer func() {
		for _, r := range refs {
			r.Close()
		}
	}()
	for _, fr := range a.Frames {
		if !opaque {
			if _, ok := fr.(*image.NRGBA); !ok {
				fr = toNRGBA(fr)
			}
		}
		r, err := vipsFrame(fr, opaque)
		if err != nil {
			return nil, err
		}
		refs = append(refs, r)
	}
	height := refs[0].Height()
	joined := refs[0]
	if len(refs) > 1 {
		if err := joined.ArrayJoin(refs[1:], 1); err != nil {
			return nil, fmt.Errorf("img: vips joining frames: %w", err)
		}
	}
	if err := joined.SetPageHeight(height); err != nil {
		return nil, err
	}
	if err := joined.SetPageDelay(a.DelaysMS); err != nil {
		return nil, err
	}
	if err := joined.SetLoop(a.LoopCount); err != nil {
		return nil, err
	}
	return v.export(WebP, joined, o)
}

func toNRGBA(m image.Image) *image.NRGBA {
	n, _ := Normalize(m)
	if nr, ok := n.(*image.NRGBA); ok {
		return nr
	}
	rgba := n.(*image.RGBA)
	out := image.NewNRGBA(rgba.Bounds())
	for i := 0; i < len(rgba.Pix); i += 4 {
		out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = rgba.Pix[i], rgba.Pix[i+1], rgba.Pix[i+2], 255
	}
	return out
}

// vipsFrame is vipsImage with the band count imposed, so that every frame of an
// animation joins into one image.
func vipsFrame(m image.Image, opaque bool) (*vips.ImageRef, error) {
	if opaque {
		return vipsImage(m)
	}
	n := m.(*image.NRGBA)
	b := n.Bounds()
	return vips.NewImageFromMemory(n.Pix, b.Dx(), b.Dy(), 4, vips.BandFormatUchar, vips.InterpretationSRGB)
}

// Decode returns the pixels as *image.NRGBA (straight alpha), or *image.Gray for
// grey images. The EXIF orientation is not applied.
func (v *Vips) Decode(f Format, data []byte) (image.Image, error) {
	switch f {
	case JPEG, WebP, AVIF, JXL:
	default:
		return nil, fmt.Errorf("img: can not decode %s", f)
	}
	p := vips.NewImportParams()
	p.AutoRotate.Set(false)
	ref, err := vips.LoadImageFromBuffer(data, p)
	if err != nil {
		return nil, fmt.Errorf("img: vips decoding %s: %w", f, err)
	}
	defer ref.Close()
	out, err := ref.ToGoImage()
	if err != nil {
		return nil, fmt.Errorf("img: vips decoding %s: %w", f, err)
	}
	return out, nil
}
