package jxlcodec

import (
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"math"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed lib/codec.wasm.gz
var module []byte

const (
	MaxBytes  = 64 << 20
	MaxPixels = 16_000_000
	MaxFrames = 256
	Version   = "libjxl-0.11.2-wasm32-wasi-v1"
)

var ErrInvalid = errors.New("jxl: unsupported, invalid or over-limit image")

func init() {
	read := func(r io.Reader) ([]byte, error) {
		b, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
		if err != nil {
			return nil, err
		}
		if len(b) > MaxBytes {
			return nil, ErrInvalid
		}
		return b, nil
	}
	decode := func(r io.Reader) (image.Image, error) {
		b, err := read(r)
		if err != nil {
			return nil, err
		}
		a, err := Decode(b, false)
		if err != nil {
			return nil, err
		}
		return a.Frames[0], nil
	}
	config := func(r io.Reader) (image.Config, error) {
		b, err := read(r)
		if err != nil {
			return image.Config{}, err
		}
		m, err := Inspect(b)
		if err != nil {
			return image.Config{}, err
		}
		return image.Config{ColorModel: color.NRGBA64Model, Width: m.Width, Height: m.Height}, nil
	}
	image.RegisterFormat("jxl", "????JXL ", decode, config)
	image.RegisterFormat("jxl", "\xff\x0a", decode, config)
}

var engine struct {
	sync.Once
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	err      error
}

func prepare() {
	engine.Do(func() {
		z, err := gzip.NewReader(bytes.NewReader(module))
		if err != nil {
			engine.err = err
			return
		}
		defer z.Close()
		code, err := io.ReadAll(io.LimitReader(z, 32<<20))
		if err != nil {
			engine.err = err
			return
		}
		ctx := context.Background()
		engine.runtime = wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(8192).WithCloseOnContextDone(true))
		if _, err := wasi_snapshot_preview1.Instantiate(ctx, engine.runtime); err != nil {
			engine.err = err
			return
		}
		engine.compiled, engine.err = engine.runtime.CompileModule(ctx, code)
	})
}

type instance struct {
	ctx    context.Context
	cancel context.CancelFunc
	mod    api.Module
}

func newInstance() (*instance, error) {
	prepare()
	if engine.err != nil {
		return nil, fmt.Errorf("jxl: compile portable codec: %w", engine.err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	m, err := engine.runtime.InstantiateModule(ctx, engine.compiled, wazero.NewModuleConfig().WithName("").WithStartFunctions("_initialize"))
	if err != nil {
		cancel()
		return nil, err
	}
	return &instance{ctx: ctx, cancel: cancel, mod: m}, nil
}

func (i *instance) close() { i.mod.Close(i.ctx); i.cancel() }

func (i *instance) alloc(data []byte, length int) (uint32, error) {
	if length < len(data) {
		length = len(data)
	}
	v, err := i.mod.ExportedFunction("malloc").Call(i.ctx, uint64(max(1, length)))
	if err != nil || len(v) != 1 || v[0] == 0 {
		return 0, ErrInvalid
	}
	p := uint32(v[0])
	if !i.mod.Memory().Write(p, data) {
		return 0, ErrInvalid
	}
	return p, nil
}

func (i *instance) call(name string, args ...uint64) (uint32, error) {
	v, err := i.mod.ExportedFunction(name).Call(i.ctx, args...)
	if err != nil {
		return 0, fmt.Errorf("jxl: %s: %w", name, err)
	}
	if len(v) != 1 || v[0] == 0 {
		return 0, ErrInvalid
	}
	return uint32(v[0]), nil
}

func (i *instance) copy(ptr uint32, n uint32) ([]byte, error) {
	if n > 128<<20 {
		return nil, ErrInvalid
	}
	b, ok := i.mod.Memory().Read(ptr, n)
	if !ok {
		return nil, ErrInvalid
	}
	return bytes.Clone(b), nil
}

func transform(raw []byte, name string) ([]byte, error) {
	if len(raw) == 0 || len(raw) > MaxBytes {
		return nil, ErrInvalid
	}
	i, err := newInstance()
	if err != nil {
		return nil, err
	}
	defer i.close()
	in, err := i.alloc(raw, 0)
	if err != nil {
		return nil, err
	}
	size, err := i.alloc(make([]byte, 4), 4)
	if err != nil {
		return nil, err
	}
	out, err := i.call(name, uint64(in), uint64(len(raw)), uint64(size))
	if err != nil {
		return nil, err
	}
	n, ok := i.mod.Memory().ReadUint32Le(size)
	if !ok || n > MaxBytes {
		return nil, ErrInvalid
	}
	return i.copy(out, n)
}

func Recompress(raw []byte) ([]byte, error)  { return transform(raw, "mp_recompress") }
func Reconstruct(raw []byte) ([]byte, error) { return transform(raw, "mp_reconstruct") }

type Metadata struct {
	Width, Height                    int
	RawWidth, RawHeight              int
	Orientation                      int
	AlphaBits                        int
	BitsPerSample, ExponentBits      int
	Animated                         bool
	Frames, LoopCount                int
	TicksNumerator, TicksDenominator uint32
	IntensityTarget                  float32
	ExtraChannels                    int
	ICC                              []byte
	Primaries, TransferFunction      uint32
	ColorSpace, WhitePoint           uint32
	OutputSRGB                       bool
}

func (i *instance) metadata(ptr uint32, inspect bool) (*Metadata, []int, error) {
	data, ok := i.mod.Memory().Read(ptr, 4*(24+MaxFrames))
	if !ok {
		return nil, nil, ErrInvalid
	}
	w := func(n int) uint32 { return binary.LittleEndian.Uint32(data[4*n:]) }
	m := &Metadata{Width: int(w(0)), Height: int(w(1)), RawWidth: int(w(0)), RawHeight: int(w(1)), Orientation: int(w(2)), AlphaBits: int(w(3)), BitsPerSample: int(w(4)), ExponentBits: int(w(5)), Animated: w(6) != 0, LoopCount: int(w(7)), TicksNumerator: w(8), TicksDenominator: w(9), IntensityTarget: math.Float32frombits(w(10)), ExtraChannels: int(w(11)), Frames: int(w(14)), OutputSRGB: true}
	if m.Frames < 1 || m.Frames > MaxFrames {
		return nil, nil, ErrInvalid
	}
	if inspect {
		if m.Orientation >= 5 {
			m.Width, m.Height = m.Height, m.Width
		}
		var err error
		m.ICC, err = i.copy(w(12), w(13))
		if err != nil {
			return nil, nil, err
		}
		m.Primaries, m.TransferFunction = w(17), w(18)
		m.ColorSpace, m.WhitePoint = w(16), w(19)
	}
	delays := make([]int, m.Frames)
	for f := range delays {
		if m.TicksNumerator > 0 {
			ms := uint64(w(24+f)) * uint64(max(1, m.TicksDenominator)) * 1000 / uint64(m.TicksNumerator)
			if ms > math.MaxInt32 {
				return nil, nil, ErrInvalid
			}
			delays[f] = int(ms)
		}
	}
	return m, delays, nil
}

func Inspect(raw []byte) (*Metadata, error) {
	if len(raw) == 0 || len(raw) > MaxBytes {
		return nil, ErrInvalid
	}
	i, err := newInstance()
	if err != nil {
		return nil, err
	}
	defer i.close()
	in, err := i.alloc(raw, 0)
	if err != nil {
		return nil, err
	}
	words, err := i.alloc(make([]byte, 4*(24+MaxFrames)), 0)
	if err != nil {
		return nil, err
	}
	_, err = i.call("mp_inspect", uint64(in), uint64(len(raw)), uint64(words), MaxPixels)
	if err != nil {
		return nil, err
	}
	m, _, err := i.metadata(words, true)
	return m, err
}

type Animation struct {
	Frames    []image.Image
	DelaysMS  []int
	LoopCount int
	Metadata  Metadata
}

func Decode(raw []byte, all bool) (*Animation, error) {
	if len(raw) == 0 || len(raw) > MaxBytes {
		return nil, ErrInvalid
	}
	i, err := newInstance()
	if err != nil {
		return nil, err
	}
	defer i.close()
	in, err := i.alloc(raw, 0)
	if err != nil {
		return nil, err
	}
	words, err := i.alloc(make([]byte, 4*(24+MaxFrames)), 0)
	if err != nil {
		return nil, err
	}
	var allArg uint64
	if all {
		allArg = 1
	}
	out, err := i.call("mp_decode", uint64(in), uint64(len(raw)), uint64(words), MaxPixels, allArg)
	if err != nil {
		return nil, err
	}
	m, delays, err := i.metadata(words, false)
	if err != nil {
		return nil, err
	}
	n, ok := i.mod.Memory().ReadUint32Le(words + 15*4)
	if !ok {
		return nil, ErrInvalid
	}
	pixels, err := i.copy(out, n)
	if err != nil {
		return nil, err
	}
	frameBytes := m.Width * m.Height * 8
	if frameBytes <= 0 || len(pixels) != frameBytes*m.Frames {
		return nil, ErrInvalid
	}
	a := &Animation{DelaysMS: delays, LoopCount: m.LoopCount, Metadata: *m}
	for f := 0; f < m.Frames; f++ {
		a.Frames = append(a.Frames, &image.NRGBA64{Pix: pixels[f*frameBytes : (f+1)*frameBytes], Stride: m.Width * 8, Rect: image.Rect(0, 0, m.Width, m.Height)})
	}
	return a, nil
}

type Options struct {
	Quality, Effort             int
	Lossless                    bool
	Orientation                 int
	ICC                         []byte
	Primaries, TransferFunction uint32
}

func Encode(frames []image.Image, delays []int, loops int, o Options) ([]byte, error) {
	if len(frames) == 0 || len(frames) > MaxFrames || len(o.ICC) > 4<<20 {
		return nil, ErrInvalid
	}
	b := frames[0].Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 1 || h < 1 || w > MaxPixels/h || len(frames) > MaxPixels/(w*h) {
		return nil, ErrInvalid
	}
	depth := 8
	if _, ok := frames[0].(*image.NRGBA64); ok {
		depth = 16
	}
	pixels := make([]byte, 0, w*h*4*(depth/8)*len(frames))
	for _, src := range frames {
		if src.Bounds().Dx() != w || src.Bounds().Dy() != h {
			return nil, ErrInvalid
		}
		if depth == 16 {
			dst := image.NewNRGBA64(image.Rect(0, 0, w, h))
			draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
			pixels = append(pixels, dst.Pix...)
		} else {
			dst := image.NewNRGBA(image.Rect(0, 0, w, h))
			draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
			pixels = append(pixels, dst.Pix...)
		}
	}
	i, err := newInstance()
	if err != nil {
		return nil, err
	}
	defer i.close()
	in, err := i.alloc(pixels, 0)
	if err != nil {
		return nil, err
	}
	delayBytes := make([]byte, 4*len(frames))
	for f := range frames {
		if f < len(delays) {
			binary.LittleEndian.PutUint32(delayBytes[4*f:], uint32(max(0, delays[f])))
		}
	}
	delayPtr, err := i.alloc(delayBytes, 0)
	if err != nil {
		return nil, err
	}
	iccPtr, err := i.alloc(o.ICC, 0)
	if err != nil {
		return nil, err
	}
	size, err := i.alloc(make([]byte, 4), 0)
	if err != nil {
		return nil, err
	}
	quality := o.Quality
	if o.Lossless {
		quality = 100
	}
	if quality < 1 || quality > 100 {
		quality = 75
	}
	effort := o.Effort
	if effort < 1 || effort > 10 {
		effort = 5
	}
	out, err := i.call("mp_encode", uint64(in), uint64(w), uint64(h), uint64(depth), uint64(quality), uint64(effort), uint64(len(frames)), uint64(delayPtr), uint64(max(0, loops)), uint64(max(1, o.Orientation)), uint64(iccPtr), uint64(len(o.ICC)), uint64(o.Primaries), uint64(o.TransferFunction), uint64(size))
	if err != nil {
		return nil, err
	}
	n, ok := i.mod.Memory().ReadUint32Le(size)
	if !ok || n > MaxBytes {
		return nil, ErrInvalid
	}
	return i.copy(out, n)
}
