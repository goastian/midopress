package codecworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"net/url"
	"os"
	"os/exec"
	"sync/atomic"
	"time"

	"github.com/goastian/midopress/internal/codec"
	"github.com/goastian/midopress/internal/fonts"
	"github.com/goastian/midopress/internal/htmlhints"
	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/quality"
	"github.com/goastian/midopress/internal/sandbox"
	"github.com/goastian/midopress/internal/textmin"
)

const maxBytes = 64 << 20

var ErrWorker = errors.New("isolated codec operation failed")

var slots = make(chan struct{}, 4)
var active, waiting atomic.Int64

func Workers() (int64, int64, int) { return active.Load(), waiting.Load(), cap(slots) }

type Request struct {
	Operation  string
	Backend    string
	Image      img.Input
	Raw        []byte
	Other      []byte
	Trace      bool
	Quality    int
	Encoding   codec.Encoding
	Levels     codec.Levels
	MediaType  string
	Minify     textmin.Config
	MaxPixels  int
	MaxDecoded int64
	BaseURL    string
	Limits     sandbox.Limits
}

type Response struct {
	Image      *img.Result
	Data       []byte
	Path       string
	Sandbox    sandbox.Status
	Code       string
	Trace      []string
	Comparison Comparison
	Width      int
	Height     int
	Page       *htmlhints.Page
}

func PageHints(ctx context.Context, executable string, base *url.URL, raw []byte) (htmlhints.Page, error) {
	if len(raw) > 768<<10 {
		raw = raw[:768<<10]
	}
	r, err := Call(ctx, executable, Request{Operation: "page_hints", BaseURL: base.String(), Raw: raw})
	if err != nil {
		return htmlhints.Page{}, err
	}
	if r.Page == nil {
		return htmlhints.Page{}, ErrWorker
	}
	return *r.Page, nil
}

func ImageSize(ctx context.Context, executable string, raw []byte, maxPixels int) (int, int, error) {
	r, err := Call(ctx, executable, Request{Operation: "dimensions", Raw: raw, MaxPixels: maxPixels})
	if err != nil {
		return 0, 0, err
	}
	return r.Width, r.Height, nil
}

type Comparison struct {
	Width      int
	Height     int
	Comparable bool
	Distance   float64
}

func Compare(ctx context.Context, executable, backend string, original, result []byte, maxPixels int) (Comparison, error) {
	r, err := Call(ctx, executable, Request{Operation: "compare", Backend: backend, Raw: original, Other: result, MaxPixels: maxPixels})
	if err != nil {
		return Comparison{}, err
	}
	return r.Comparison, nil
}

func JPEG(ctx context.Context, executable, backend string, raw []byte, quality int) ([]byte, error) {
	r, err := Call(ctx, executable, Request{Operation: "jpeg", Backend: backend, Raw: raw, Quality: quality, MaxPixels: 16_000_000})
	if err != nil {
		return nil, err
	}
	return r.Data, nil
}

type boundedBuffer struct {
	bytes.Buffer
	max int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.max-b.Len() {
		return 0, ErrWorker
	}
	return b.Buffer.Write(p)
}

func Call(ctx context.Context, executable string, req Request) (*Response, error) {
	if len(req.Raw)+len(req.Other)+len(req.Image.Raw) > maxBytes || executable == "" {
		return nil, ErrWorker
	}
	if req.Limits.CPUSeconds == 0 {
		req.Limits.CPUSeconds = 30
		if deadline, ok := ctx.Deadline(); ok {
			req.Limits.CPUSeconds = uint64(max(1, min(120, int(time.Until(deadline).Seconds())+1)))
		}
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	waiting.Add(1)
	select {
	case slots <- struct{}{}:
		waiting.Add(-1)
		active.Add(1)
		defer func() { active.Add(-1); <-slots }()
	case <-ctx.Done():
		waiting.Add(-1)
		return nil, ctx.Err()
	}
	input, err := json.Marshal(req)
	if err != nil {
		return nil, ErrWorker
	}
	cmd := exec.CommandContext(ctx, executable, "codec-worker")
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = []string{"GOMAXPROCS=2", "GOMEMLIMIT=384MiB", "GORACE=atexit_sleep_ms=0", "TZ=UTC"}
	for _, k := range []string{"LD_LIBRARY_PATH", "DYLD_LIBRARY_PATH", "SystemRoot"} {
		if v, ok := os.LookupEnv(k); ok {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	var out boundedBuffer
	out.max = (maxBytes*4)/3 + 65536
	cmd.Stdout, cmd.Stderr, cmd.WaitDelay = &out, io.Discard, time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrWorker
	}
	var response Response
	if err := json.Unmarshal(out.Bytes(), &response); err != nil || !response.Sandbox.Enforced {
		return nil, fmt.Errorf("%w: permissions not enforced", ErrWorker)
	}
	if response.Code != "" {
		cause := ErrWorker
		switch response.Code {
		case "unsupported_image":
			cause = errors.Join(ErrWorker, img.ErrUnsupported)
		case "budget_exhausted":
			cause = errors.Join(ErrWorker, img.ErrBudget)
		case "decoded_limit", "pixel_or_format_limit", "result_limit":
			cause = errors.Join(ErrWorker, codec.ErrTooLarge)
		}
		return nil, fmt.Errorf("%w: %s", cause, response.Code)
	}
	return &response, nil
}

func Optimize(ctx context.Context, executable, backend string, in img.Input, maxPixels int) (*img.Result, error) {
	r, err := Call(ctx, executable, Request{Operation: "image", Backend: backend, Image: in, MaxPixels: maxPixels})
	if err != nil {
		return nil, err
	}
	if r.Image == nil || len(r.Image.Data) == 0 || len(r.Image.Data) > maxBytes || img.Sniff(r.Image.Data) != r.Image.Format {
		return nil, ErrWorker
	}
	return r.Image, nil
}

func Decode(ctx context.Context, executable string, enc codec.Encoding, raw []byte, limit int64) ([]byte, error) {
	r, err := Call(ctx, executable, Request{Operation: "decode", Encoding: enc, Raw: raw, MaxDecoded: limit})
	if err != nil {
		return nil, err
	}
	return r.Data, nil
}

func Compress(ctx context.Context, executable string, enc codec.Encoding, raw []byte, levels codec.Levels) ([]byte, error) {
	r, err := Call(ctx, executable, Request{Operation: "compress", Encoding: enc, Raw: raw, Levels: levels})
	if err != nil {
		return nil, err
	}
	return r.Data, nil
}

func Minify(ctx context.Context, executable, mt string, raw []byte, cfg textmin.Config) ([]byte, error) {
	r, err := Call(ctx, executable, Request{Operation: "minify", Raw: raw, MediaType: mt, Minify: cfg})
	if err != nil {
		return nil, err
	}
	return r.Data, nil
}

func Font(ctx context.Context, executable string, raw []byte) ([]byte, error) {
	r, err := Call(ctx, executable, Request{Operation: "font", Raw: raw})
	if err != nil {
		return nil, err
	}
	return r.Data, nil
}

func Inspect(ctx context.Context, executable string) (sandbox.Status, error) {
	r, err := Call(ctx, executable, Request{Operation: "probe"})
	if err != nil {
		return sandbox.Status{}, err
	}
	return r.Sandbox, nil
}

// Run is a one-job protocol: no earlier client's bodies remain in the worker.
func Run(in io.Reader, out io.Writer) error {
	if _, err := sandbox.Prepare(sandbox.Limits{}); err != nil {
		return err
	}
	var req Request
	d := json.NewDecoder(io.LimitReader(in, 96<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		return ErrWorker
	}
	if len(req.Raw)+len(req.Other)+len(req.Image.Raw) > maxBytes {
		return ErrWorker
	}
	var be img.Backend
	if req.Operation == "image" || req.Operation == "original" || req.Operation == "compare" || req.Operation == "jpeg" {
		var err error
		be, err = img.NewBackend(req.Backend)
		if err != nil {
			return ErrWorker
		}
		for _, f := range []img.Format{img.JPEG, img.WebP, img.AVIF, img.JXL} {
			if be.Supports(f) {
				_ = be.Native(f)
			}
		}
		_ = img.JPEGRecompressionExecutionPath()
	}
	status, err := sandbox.Enter(req.Limits)
	if err != nil {
		return sandbox.ErrUnavailable
	}
	r := Response{Sandbox: status}
	var data boundedBuffer
	data.max = maxBytes
	switch req.Operation {
	case "probe":
	case "page_hints":
		if len(req.BaseURL) > 16<<10 || len(req.Raw) > 768<<10 {
			r.Code = "page_limit"
			break
		}
		base, e := url.Parse(req.BaseURL)
		if e != nil {
			r.Code = "invalid_page_url"
			break
		}
		page := htmlhints.Scan(req.Raw, base, 768<<10)
		r.Page = &page
	case "dimensions":
		if CheckPixels(req.Raw, img.Sniff(req.Raw), req.MaxPixels) != nil {
			r.Code = "pixel_or_format_limit"
			break
		}
		r.Width, r.Height, err = Dimensions(req.Raw, img.Sniff(req.Raw))
	case "image", "original":
		if len(req.Image.Raw) == 0 || CheckPixels(req.Image.Raw, img.Sniff(req.Image.Raw), req.MaxPixels) != nil {
			r.Code = "pixel_or_format_limit"
			break
		}
		if req.Operation == "original" {
			r.Image = &img.Result{Data: req.Image.Raw, Format: img.Sniff(req.Image.Raw), Lossless: true, Note: "original"}
			break
		}
		engine := img.NewEngine(be)
		if req.Trace {
			engine.Trace = func(f string, args ...any) {
				if len(r.Trace) < 128 {
					line := fmt.Sprintf(f, args...)
					if len(line) > 256 {
						line = line[:256]
					}
					r.Trace = append(r.Trace, line)
				}
			}
		}
		r.Image, err = engine.Optimize(context.Background(), req.Image)
		if err == nil && r.Image != nil {
			r.Path = img.ExecutionPath(be, r.Image.Format)
			if r.Image.Note == "metadata stripped" {
				r.Path = "go"
			}
			if r.Image.Note == "jpeg to jxl, reversible" {
				r.Path = img.JPEGRecompressionExecutionPath()
			}
		}
	case "compare":
		if CheckPixels(req.Raw, img.Sniff(req.Raw), req.MaxPixels) != nil || CheckPixels(req.Other, img.Sniff(req.Other), req.MaxPixels) != nil {
			r.Code = "pixel_or_format_limit"
			break
		}
		if img.NeedsColorConversion(req.Raw, img.Sniff(req.Raw)) || img.NeedsColorConversion(req.Other, img.Sniff(req.Other)) {
			break
		}
		a, e1 := img.DecodeDisplay(context.Background(), be, img.Sniff(req.Raw), req.Raw)
		b, e2 := img.DecodeDisplay(context.Background(), be, img.Sniff(req.Other), req.Other)
		if e1 != nil || e2 != nil {
			break
		}
		r.Comparison.Width, r.Comparison.Height = b.Bounds().Dx(), b.Bounds().Dy()
		if a.Bounds().Size() != b.Bounds().Size() {
			break
		}
		ref, e := quality.NewDSSIMReference(a, quality.Options{MaxPixels: a.Bounds().Dx() * a.Bounds().Dy()})
		if e == nil {
			r.Comparison.Distance, e = ref.Distance(b)
			r.Comparison.Comparable = e == nil
		}
	case "jpeg":
		if req.Quality < 1 || req.Quality > 100 || CheckPixels(req.Raw, img.Sniff(req.Raw), req.MaxPixels) != nil {
			r.Code = "invalid_jpeg_source"
			break
		}
		pic, e := img.DecodeDisplay(context.Background(), be, img.Sniff(req.Raw), req.Raw)
		if e != nil {
			err = e
			break
		}
		err = jpeg.Encode(&data, pic, &jpeg.Options{Quality: req.Quality})
		r.Data = data.Bytes()
	case "font":
		r.Data, err = fonts.ToWOFF2(req.Raw)
	case "minify":
		err = textmin.New(req.Minify).Minify(req.MediaType, &data, req.Raw)
		r.Data = data.Bytes()
	case "decode":
		if req.MaxDecoded < 1 || req.MaxDecoded > maxBytes {
			r.Code = "decoded_limit"
			break
		}
		var decoded bytes.Buffer
		err = codec.NewText(req.Levels).Decode(req.Encoding, req.Raw, &decoded, req.MaxDecoded)
		r.Data = decoded.Bytes()
	case "compress":
		var compressed bytes.Buffer
		err = codec.NewText(req.Levels).Compress(req.Encoding, &compressed, req.Raw)
		r.Data = compressed.Bytes()
	default:
		r.Code = "unsupported_operation"
	}
	if err != nil {
		r.Image, r.Data, r.Code = nil, nil, "codec_rejected"
		switch {
		case errors.Is(err, img.ErrUnsupported):
			r.Code = "unsupported_image"
		case errors.Is(err, img.ErrBudget):
			r.Code = "budget_exhausted"
		case errors.Is(err, codec.ErrTooLarge):
			r.Code = "decoded_limit"
		}
	}
	if len(r.Data) > maxBytes || (r.Image != nil && len(r.Image.Data) > maxBytes) {
		r.Image, r.Data, r.Code = nil, nil, "result_limit"
	}
	return json.NewEncoder(out).Encode(r)
}
