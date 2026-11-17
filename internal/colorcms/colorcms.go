// Package colorcms converts ICC-tagged pixels with an embedded LittleCMS engine.
package colorcms

import (
	"context"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed lcms2.wasm
var module []byte

var runtimeOnce sync.Once
var engine wazero.Runtime
var compiled wazero.CompiledModule
var compileErr error

// RGBProfile describes PNG cHRM/gAMA or SDR CICP values as an ICC profile.
// transfer is 13 (sRGB), 1/6 (BT.709/601), 8 (linear), or 0 (explicit gamma).
func RGBProfile(ctx context.Context, chroma [8]float64, transfer int, gamma float64) ([]byte, error) {
	for _, v := range chroma {
		if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v >= 1 {
			return nil, errors.New("colorcms: invalid chromaticity")
		}
	}
	for i := 0; i < len(chroma); i += 2 {
		if chroma[i]+chroma[i+1] > 1.000001 {
			return nil, errors.New("colorcms: invalid chromaticity")
		}
	}
	if transfer != 13 && transfer != 1 && transfer != 6 && transfer != 8 && (transfer != 0 || gamma <= 0 || gamma > 10 || math.IsNaN(gamma)) {
		return nil, errors.New("colorcms: unsupported transfer")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	runtimeOnce.Do(initialize)
	if compileErr != nil {
		return nil, compileErr
	}
	m, err := engine.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithName("").WithStartFunctions())
	if err != nil {
		return nil, err
	}
	defer func() { _ = m.Close(context.Background()) }()
	r, err := m.ExportedFunction("malloc").Call(ctx, 64+65536)
	if err != nil || len(r) != 1 || r[0] == 0 {
		return nil, errors.New("colorcms: profile memory")
	}
	p := uint32(r[0])
	buf := make([]byte, 64)
	for i, v := range chroma {
		binary.LittleEndian.PutUint64(buf[i*8:], math.Float64bits(v))
	}
	if !m.Memory().Write(p, buf) {
		return nil, errors.New("colorcms: profile memory")
	}
	r, err = m.ExportedFunction("cms_rgb_profile").Call(ctx, uint64(p), uint64(transfer), math.Float64bits(gamma), uint64(p+64), 65536)
	if err != nil || len(r) != 1 || r[0] == 0 || r[0] > 65536 {
		return nil, errors.New("colorcms: cannot build RGB profile")
	}
	data, ok := m.Memory().Read(p+64, uint32(r[0]))
	if !ok {
		return nil, errors.New("colorcms: profile memory")
	}
	return append([]byte(nil), data...), nil
}

func initialize() {
	ctx := context.Background()
	engine = wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(8192).WithCloseOnContextDone(true))
	if _, compileErr = wasi_snapshot_preview1.Instantiate(ctx, engine); compileErr == nil {
		compiled, compileErr = engine.CompileModule(ctx, module)
	}
}

// ToSRGB uses relative colorimetric ICC rendering, clipping colors outside sRGB.
// RGB, grayscale and CMYK matrix/TRC or LUT profiles are supported. Alpha is copied unchanged.
func ToSRGB(ctx context.Context, src image.Image, profile []byte) (image.Image, error) {
	if len(profile) < 132 || len(profile) > 8<<20 || string(profile[36:40]) != "acsp" {
		return nil, errors.New("colorcms: invalid ICC profile")
	}
	channels := map[string]int{"RGB ": 3, "GRAY": 1, "CMYK": 4}[string(profile[16:20])]
	if channels == 4 {
		if _, ok := src.(*image.CMYK); !ok {
			return nil, errors.New("colorcms: CMYK profile requires CMYK samples")
		}
	}
	b := src.Bounds()
	n := int64(b.Dx()) * int64(b.Dy())
	if channels == 0 || n <= 0 || n > 16_000_000 {
		return nil, errors.New("colorcms: unsupported color space or image size")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	runtimeOnce.Do(initialize)
	if compileErr != nil {
		return nil, fmt.Errorf("colorcms: compile: %w", compileErr)
	}
	m, err := engine.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithName("").WithStartFunctions())
	if err != nil {
		return nil, err
	}
	defer func() { _ = m.Close(context.Background()) }()
	alloc := func(size int) (uint32, error) {
		r, err := m.ExportedFunction("malloc").Call(ctx, uint64(size))
		if err != nil || len(r) != 1 || r[0] == 0 {
			return 0, errors.New("colorcms: memory limit")
		}
		return uint32(r[0]), nil
	}
	p, err := alloc(len(profile))
	if err != nil {
		return nil, err
	}
	input, err := alloc(int(n) * channels * 2)
	if err != nil {
		return nil, err
	}
	output, err := alloc(int(n) * 6)
	if err != nil {
		return nil, err
	}
	if !m.Memory().Write(p, profile) {
		return nil, errors.New("colorcms: profile memory")
	}
	buf, ok := m.Memory().Read(input, uint32(n)*uint32(channels)*2)
	if !ok {
		return nil, errors.New("colorcms: input memory")
	}
	alpha := make([]uint16, int(n))
	i := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBA64Model.Convert(src.At(x, y)).(color.NRGBA64)
			alpha[i] = c.A
			values := [4]uint16{c.R, c.G, c.B, 0}
			if channels == 1 {
				values[0] = color.Gray16Model.Convert(src.At(x, y)).(color.Gray16).Y
			}
			if channels == 4 {
				v := color.CMYKModel.Convert(src.At(x, y)).(color.CMYK)
				values = [4]uint16{uint16(v.C) * 257, uint16(v.M) * 257, uint16(v.Y) * 257, uint16(v.K) * 257}
			}
			for j := 0; j < channels; j++ {
				binary.LittleEndian.PutUint16(buf[(i*channels+j)*2:], values[j])
			}
			i++
		}
	}
	status, err := m.ExportedFunction("cms_convert").Call(ctx, uint64(p), uint64(len(profile)), uint64(input), uint64(output), uint64(n), uint64(channels))
	if err != nil {
		return nil, fmt.Errorf("colorcms: transform: %w", err)
	}
	if len(status) != 1 || status[0] != 0 {
		return nil, fmt.Errorf("colorcms: ICC transform rejected (%v)", status)
	}
	buf, ok = m.Memory().Read(output, uint32(n)*6)
	if !ok {
		return nil, errors.New("colorcms: output memory")
	}
	dst := image.NewNRGBA64(image.Rect(0, 0, b.Dx(), b.Dy()))
	for i := 0; i < int(n); i++ {
		for c := 0; c < 3; c++ {
			binary.BigEndian.PutUint16(dst.Pix[i*8+c*2:], binary.LittleEndian.Uint16(buf[i*6+c*2:]))
		}
		binary.BigEndian.PutUint16(dst.Pix[i*8+6:], alpha[i])
	}
	return dst, nil
}
