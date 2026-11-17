package imageapi

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/imgmeta"
)

type Job struct {
	Raw       []byte
	Request   Request
	Profile   img.Profile
	Caps      img.Caps
	Backend   string
	MaxPixels int
}

type Output struct {
	Data     []byte
	Format   img.Format
	Decision string
	Backend  string
	Metric   img.Metric
	Measure  float64
}

type boundedBuffer struct {
	bytes.Buffer
	max int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, errSize
	}
	return b.Buffer.Write(p)
}

func Process(executable string) Transform {
	return func(ctx context.Context, j Job) (*Output, error) {
		data, err := json.Marshal(j)
		if err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, executable, "image-worker")
		cmd.Stdin = bytes.NewReader(data)
		cmd.Env = []string{"GOMAXPROCS=2", "GOMEMLIMIT=512MiB"}
		for _, k := range []string{"PATH", "SystemRoot", "TMPDIR", "LD_LIBRARY_PATH", "DYLD_LIBRARY_PATH"} {
			if v, ok := os.LookupEnv(k); ok {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
		}
		var out boundedBuffer
		out.max = len(j.Raw)*2 + 65536
		cmd.Stdout = &out
		cmd.Stderr = io.Discard
		cmd.WaitDelay = time.Second
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errors.New("image worker failed")
		}
		var result Output
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			return nil, errors.New("invalid image worker response")
		}
		return &result, nil
	}
}

func RunWorker(in io.Reader, out io.Writer) error {
	var j Job
	d := json.NewDecoder(io.LimitReader(in, 96<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&j); err != nil {
		return err
	}
	if len(j.Raw) == 0 || len(j.Raw) > 64<<20 || j.MaxPixels < 1 || j.MaxPixels > 16_000_000 {
		return errSize
	}
	f := img.Sniff(j.Raw)
	if err := checkPixels(j.Raw, f, j.MaxPixels); err != nil {
		return err
	}
	be, err := img.NewBackend(j.Backend)
	if err != nil {
		return err
	}
	r := &Output{Data: j.Raw, Format: f, Decision: "original", Backend: "original"}
	if j.Request.Mode != "original" {
		outputFormat := img.Unknown
		for _, candidate := range []img.Format{img.JPEG, img.PNG, img.WebP, img.AVIF, img.JXL} {
			if candidate.String() == j.Request.Format {
				outputFormat = candidate
			}
		}
		if j.Request.Width > 0 {
			j.Profile.ResizeToViewport = true
		}
		if j.Request.Format != "auto" {
			j.Caps = img.Caps{WebP: j.Request.Format == "webp", AVIF: j.Request.Format == "avif", JXL: j.Request.Format == "jxl"}
			j.Profile.Formats = nil
			if modern, err := img.ParseFormat(j.Request.Format); err == nil {
				j.Profile.Formats = []img.Format{modern}
			}
		}
		result, err := img.NewEngine(be).Optimize(context.Background(), img.Input{Raw: j.Raw, Format: f, Profile: &j.Profile, VerifyQuality: true, OutputFormat: outputFormat,
			Caps: j.Caps, Hints: img.Hints{ViewportWidth: j.Request.Width, DPR: j.Request.DPR}})
		if err == nil && result != nil && len(result.Data) < len(r.Data) &&
			(j.Request.Format == "auto" || j.Request.Format == result.Format.String()) {
			r.Data, r.Format, r.Decision = result.Data, result.Format, "optimized"
			r.Metric, r.Measure = result.Metric, result.Measure
			r.Backend = img.ExecutionPath(be, result.Format)
			if result.Note == "metadata stripped" {
				r.Backend = "go"
			}
			if result.Note == "jpeg to jxl, reversible" {
				r.Backend = "native"
			}
			if result.Intentional {
				r.Decision = "appearance_change"
			}
			if result.Note == "metadata stripped" {
				r.Backend = "go"
			}
			if result.Note == "jpeg to jxl, reversible" {
				r.Backend = "native"
			}
		}
	}
	return json.NewEncoder(out).Encode(r)
}

func dimensions(raw []byte, f img.Format) (int, int, error) {
	switch f {
	case img.JPEG:
		c, err := jpeg.DecodeConfig(bytes.NewReader(raw))
		return c.Width, c.Height, err
	case img.PNG:
		c, err := png.DecodeConfig(bytes.NewReader(raw))
		return c.Width, c.Height, err
	case img.GIF:
		c, err := gif.DecodeConfig(bytes.NewReader(raw))
		return c.Width, c.Height, err
	case img.WebP:
		c, err := imgmeta.ParseWebP(raw)
		if err == nil {
			return c.Width, c.Height, nil
		}
		return 0, 0, err
	case img.AVIF:
		c, err := imgmeta.ParseAVIF(raw)
		if err == nil {
			return c.Width, c.Height, nil
		}
		return 0, 0, err
	case img.JXL:
		c, err := imgmeta.ParseJXL(raw)
		if err == nil {
			return c.Width, c.Height, nil
		}
		return 0, 0, err
	}
	return 0, 0, img.ErrUnsupported
}

func checkPixels(raw []byte, f img.Format, limit int) error {
	var w, h int
	var err error
	switch f {
	case img.JPEG:
		var c image.Config
		c, err = jpeg.DecodeConfig(bytes.NewReader(raw))
		w, h = c.Width, c.Height
	case img.PNG:
		var c image.Config
		c, err = png.DecodeConfig(bytes.NewReader(raw))
		w, h = c.Width, c.Height
	case img.GIF:
		var c image.Config
		c, err = gif.DecodeConfig(bytes.NewReader(raw))
		w, h = c.Width, c.Height
		if err == nil {
			err = checkGIF(raw, limit)
		}
	case img.WebP:
		m, e := imgmeta.ParseWebP(raw)
		err = e
		if e == nil {
			w, h = m.Width, m.Height
		}
	case img.AVIF:
		m, e := imgmeta.ParseAVIF(raw)
		err = e
		if e == nil {
			w, h = m.Width, m.Height
		}
	case img.JXL:
		m, e := imgmeta.ParseJXL(raw)
		err = e
		if e == nil {
			w, h = m.Width, m.Height
		}
	default:
		return img.ErrUnsupported
	}
	if err != nil || w < 1 || h < 1 || w > limit/h {
		return errors.New("invalid image dimensions or pixel limit exceeded")
	}
	return nil
}

func checkGIF(raw []byte, limit int) error {
	if len(raw) < 13 {
		return img.ErrUnsupported
	}
	w := int(binary.LittleEndian.Uint16(raw[6:]))
	h := int(binary.LittleEndian.Uint16(raw[8:]))
	pos := 13
	if raw[10]&128 != 0 {
		pos += 3 * (2 << (raw[10] & 7))
	}
	frames := 0
	for pos < len(raw) {
		b := raw[pos]
		pos++
		switch b {
		case 0x3b:
			if frames > 0 {
				return nil
			}
			return img.ErrUnsupported
		case 0x21:
			pos++
		case 0x2c:
			if pos+9 >= len(raw) {
				return img.ErrUnsupported
			}
			frames++
			if int64(frames)*int64(w)*int64(h) > int64(limit) {
				return errSize
			}
			packed := raw[pos+8]
			pos += 9
			if packed&128 != 0 {
				pos += 3 * (2 << (packed & 7))
			}
			pos++
		default:
			return img.ErrUnsupported
		}
		for {
			if pos >= len(raw) {
				return img.ErrUnsupported
			}
			n := int(raw[pos])
			pos++
			if n == 0 {
				break
			}
			pos += n
		}
	}
	return img.ErrUnsupported
}
