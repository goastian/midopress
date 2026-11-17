package imageapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/goastian/midopress/internal/codecworker"
	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/sandbox"
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
	Sandbox  sandbox.Status
}

func requestFor(j Job) codecworker.Request {
	f := img.Sniff(j.Raw)
	output := img.Unknown
	for _, candidate := range []img.Format{img.JPEG, img.PNG, img.WebP, img.AVIF, img.JXL} {
		if candidate.String() == j.Request.Format {
			output = candidate
		}
	}
	if j.Request.Width > 0 {
		j.Profile.ResizeToViewport = true
	}
	if j.Request.Format != "auto" && j.Request.Format != "" {
		j.Caps = img.Caps{WebP: j.Request.Format == "webp", AVIF: j.Request.Format == "avif", JXL: j.Request.Format == "jxl"}
		j.Profile.Formats = nil
		if modern, err := img.ParseFormat(j.Request.Format); err == nil {
			j.Profile.Formats = []img.Format{modern}
		}
	}
	op := "image"
	if j.Request.Mode == "original" {
		op = "original"
	}
	return codecworker.Request{Operation: op, Backend: j.Backend, MaxPixels: j.MaxPixels,
		Image: img.Input{Raw: j.Raw, Format: f, Profile: &j.Profile, VerifyQuality: true, OutputFormat: output,
			Caps: j.Caps, Hints: img.Hints{ViewportWidth: j.Request.Width, DPR: j.Request.DPR}}}
}

func outputFor(j Job, r *codecworker.Response) (*Output, error) {
	if r == nil || r.Image == nil || !r.Sandbox.Enforced {
		return nil, codecworker.ErrWorker
	}
	out := &Output{Data: j.Raw, Format: img.Sniff(j.Raw), Decision: "original", Backend: "original", Sandbox: r.Sandbox}
	result := r.Image
	if len(result.Data) < len(j.Raw) && (j.Request.Format == "auto" || j.Request.Format == "" || j.Request.Format == result.Format.String()) {
		out.Data, out.Format, out.Decision, out.Backend = result.Data, result.Format, "optimized", r.Path
		out.Metric, out.Measure = result.Metric, result.Measure
		if result.Intentional {
			out.Decision = "appearance_change"
		}
	}
	return out, nil
}

func Process(executable string) Transform {
	return func(ctx context.Context, j Job) (*Output, error) {
		r, err := codecworker.Call(ctx, executable, requestFor(j))
		if err != nil {
			return nil, err
		}
		return outputFor(j, r)
	}
}

func RunWorker(in io.Reader, out io.Writer) error {
	if _, err := sandbox.Prepare(sandbox.Limits{}); err != nil {
		return err
	}
	var j Job
	d := json.NewDecoder(io.LimitReader(in, 96<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&j); err != nil {
		return err
	}
	var input, output bytes.Buffer
	if err := json.NewEncoder(&input).Encode(requestFor(j)); err != nil {
		return err
	}
	if err := codecworker.Run(&input, &output); err != nil {
		return err
	}
	var response codecworker.Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		return err
	}
	if response.Code != "" {
		return codecworker.ErrWorker
	}
	result, err := outputFor(j, &response)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(result)
}

func checkPixels(raw []byte, f img.Format, limit int) error {
	return codecworker.CheckPixels(raw, f, limit)
}
