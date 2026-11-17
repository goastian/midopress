package pipeline

import (
	"bytes"
	"context"
	"time"

	"github.com/goastian/midopress/internal/codec"
	"github.com/goastian/midopress/internal/codecworker"
	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/sandbox"
)

type SandboxInfo struct {
	sandbox.Status
	VerifiedAt time.Time `json:"verified_at,omitempty"`
	Error      string    `json:"error,omitempty"`
	Active     int64     `json:"active"`
	Queued     int64     `json:"queued"`
	Slots      int       `json:"slots"`
}

func (p *Pipeline) Sandbox() SandboxInfo {
	i := p.sandbox
	i.Active, i.Queued, i.Slots = codecworker.Workers()
	return i
}

func (p *Pipeline) isolated() bool { return !p.cfg.UnsafeInProcess }

func (p *Pipeline) decode(ctx context.Context, enc codec.Encoding, raw []byte, out *bytes.Buffer, limit int64) error {
	if !p.isolated() {
		return p.text.Decode(enc, raw, out, limit)
	}
	data, err := codecworker.Decode(ctx, p.cfg.WorkerExecutable, enc, raw, limit)
	if err != nil {
		return err
	}
	_, err = out.Write(data)
	return err
}

func (p *Pipeline) optimize(ctx context.Context, in img.Input) (*img.Result, error) {
	if !p.isolated() {
		return p.engine.Optimize(ctx, in)
	}
	in.Metric = p.engine.Metric
	return codecworker.Optimize(ctx, p.cfg.WorkerExecutable, p.engine.Backend().Name(), in, int(p.cfg.Image.MaxPixels))
}

func (p *Pipeline) encodeText(ctx context.Context, fl *flow, enc codec.Encoding, raw []byte, static bool) ([]byte, error) {
	levels := fl.lv.levels[fl.prof.Name][0]
	if static {
		levels = fl.lv.levels[fl.prof.Name][1]
	}
	return codecworker.Compress(ctx, p.cfg.WorkerExecutable, enc, raw, levels)
}

func (p *Pipeline) minify(ctx context.Context, fl *flow, mediaType string, raw []byte) ([]byte, error) {
	return codecworker.Minify(ctx, p.cfg.WorkerExecutable, mediaType, raw, fl.opts.minify)
}
