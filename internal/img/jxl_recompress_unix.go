//go:build (linux || darwin || freebsd) && (amd64 || arm64) && !nodynamic

package img

import (
	"debug/elf"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	jxlStatusSuccess        = 0
	jxlStatusNeedMoreOutput = 2

	jxlDecNeedMoreInput     = 2
	jxlDecJPEGNeedMoreOut   = 6
	jxlDecFullImage         = 0x1000
	jxlDecJPEGReconstructed = 0x2000

	jxlFrameSettingEffort = 0

	jxlEncodeEffort = 7
)

var jxlLibNames = func() []string {
	if runtime.GOOS == "darwin" {
		return []string{"libjxl.0.11.dylib", "libjxl.0.10.dylib", "libjxl.dylib", "/opt/homebrew/lib/libjxl.dylib", "/usr/local/lib/libjxl.dylib"}
	}
	return []string{"libjxl.so.0.11", "libjxl.so.0.10", "libjxl.so"}
}()

var (
	jxlOnce sync.Once
	jxlErr  error

	jxlEncoderCreate              func(mm uintptr) uintptr
	jxlEncoderDestroy             func(enc uintptr)
	jxlEncoderStoreJPEGMetadata   func(enc uintptr, v int32) int32
	jxlEncoderFrameSettingsCreate func(enc uintptr, src uintptr) uintptr
	jxlEncoderFrameSettingsSetOpt func(fs uintptr, id int32, v int64) int32
	jxlEncoderAddJPEGFrame        func(fs uintptr, data unsafe.Pointer, size uint64) int32
	jxlEncoderCloseInput          func(enc uintptr)
	jxlEncoderProcessOutput       func(enc uintptr, next, avail unsafe.Pointer) int32
	jxlDecoderCreate              func(mm uintptr) uintptr
	jxlDecoderDestroy             func(dec uintptr)
	jxlDecoderSubscribeEvents     func(dec uintptr, events int32) int32
	jxlDecoderSetInput            func(dec uintptr, data unsafe.Pointer, size uint64) int32
	jxlDecoderCloseInput          func(dec uintptr)
	jxlDecoderProcessInput        func(dec uintptr) int32
	jxlDecoderSetJPEGBuffer       func(dec uintptr, data unsafe.Pointer, size uint64) int32
	jxlDecoderReleaseJPEGBuffer   func(dec uintptr) uint64
)

func jxlLoad() {
	if runtime.GOOS == "linux" && !jxlIsDynamicBinary() {
		jxlErr = errors.New("not a dynamic binary")
		return
	}
	var handle uintptr
	var err error
	for _, name := range jxlLibNames {
		if handle, err = purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_LOCAL); err == nil {
			break
		}
	}
	if err != nil {
		jxlErr = fmt.Errorf("cannot load libjxl: %w", err)
		return
	}
	defer func() {
		if r := recover(); r != nil {
			jxlErr = fmt.Errorf("libjxl lacks a required symbol: %v", r)
		}
	}()
	purego.RegisterLibFunc(&jxlEncoderCreate, handle, "JxlEncoderCreate")
	purego.RegisterLibFunc(&jxlEncoderDestroy, handle, "JxlEncoderDestroy")
	purego.RegisterLibFunc(&jxlEncoderStoreJPEGMetadata, handle, "JxlEncoderStoreJPEGMetadata")
	purego.RegisterLibFunc(&jxlEncoderFrameSettingsCreate, handle, "JxlEncoderFrameSettingsCreate")
	purego.RegisterLibFunc(&jxlEncoderFrameSettingsSetOpt, handle, "JxlEncoderFrameSettingsSetOption")
	purego.RegisterLibFunc(&jxlEncoderAddJPEGFrame, handle, "JxlEncoderAddJPEGFrame")
	purego.RegisterLibFunc(&jxlEncoderCloseInput, handle, "JxlEncoderCloseInput")
	purego.RegisterLibFunc(&jxlEncoderProcessOutput, handle, "JxlEncoderProcessOutput")
	purego.RegisterLibFunc(&jxlDecoderCreate, handle, "JxlDecoderCreate")
	purego.RegisterLibFunc(&jxlDecoderDestroy, handle, "JxlDecoderDestroy")
	purego.RegisterLibFunc(&jxlDecoderSubscribeEvents, handle, "JxlDecoderSubscribeEvents")
	purego.RegisterLibFunc(&jxlDecoderSetInput, handle, "JxlDecoderSetInput")
	purego.RegisterLibFunc(&jxlDecoderCloseInput, handle, "JxlDecoderCloseInput")
	purego.RegisterLibFunc(&jxlDecoderProcessInput, handle, "JxlDecoderProcessInput")
	purego.RegisterLibFunc(&jxlDecoderSetJPEGBuffer, handle, "JxlDecoderSetJPEGBuffer")
	purego.RegisterLibFunc(&jxlDecoderReleaseJPEGBuffer, handle, "JxlDecoderReleaseJPEGBuffer")
}

func jxlIsDynamicBinary() bool {
	name, err := os.Executable()
	if err != nil {
		return false
	}
	f, err := elf.Open(name)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	_, err = f.DynamicSymbols()
	return err == nil
}

func jxlNativeAvailable() bool {
	jxlOnce.Do(jxlLoad)
	return jxlErr == nil
}

func jxlUnavailable() error {
	if !jxlNativeAvailable() {
		return fmt.Errorf("%w: %v", ErrUnsupported, jxlErr)
	}
	return nil
}

// jxlOut is the memory libjxl writes into: the pointer and counter of an output
// buffer. It lives on the heap and is pinned while native code may touch it,
// since C stores a raw address in it that the collector must not see.
type jxlOut struct {
	next  uintptr
	avail uint64
}

func jxlNativeEncodeJPEG(raw []byte) (out []byte, err error) {
	if err := jxlUnavailable(); err != nil {
		return nil, err
	}
	var pin runtime.Pinner
	defer pin.Unpin()

	enc := jxlEncoderCreate(0)
	if enc == 0 {
		return nil, errors.New("img: jxl encoder creation failed")
	}
	defer jxlEncoderDestroy(enc)

	if jxlEncoderStoreJPEGMetadata(enc, 1) != jxlStatusSuccess {
		return nil, errors.New("img: jxl cannot store jpeg metadata")
	}
	fs := jxlEncoderFrameSettingsCreate(enc, 0)
	if fs == 0 {
		return nil, errors.New("img: jxl frame settings creation failed")
	}
	if jxlEncoderFrameSettingsSetOpt(fs, jxlFrameSettingEffort, jxlEncodeEffort) != jxlStatusSuccess {
		return nil, errors.New("img: jxl cannot set the effort")
	}

	pin.Pin(unsafe.SliceData(raw))
	st := jxlEncoderAddJPEGFrame(fs, unsafe.Pointer(unsafe.SliceData(raw)), uint64(len(raw)))
	runtime.KeepAlive(raw)
	if st != jxlStatusSuccess {
		return nil, fmt.Errorf("%w: libjxl rejected the jpeg", ErrUnsupported)
	}
	jxlEncoderCloseInput(enc)

	buf := make([]byte, len(raw)+len(raw)/8+4096)
	state := new(jxlOut)
	pin.Pin(state)
	used := 0
	for {
		pin.Pin(unsafe.SliceData(buf))
		state.next = uintptr(unsafe.Pointer(unsafe.SliceData(buf))) + uintptr(used)
		state.avail = uint64(len(buf) - used)
		before := state.avail
		st = jxlEncoderProcessOutput(enc, unsafe.Pointer(&state.next), unsafe.Pointer(&state.avail))
		runtime.KeepAlive(buf)
		used += int(before - state.avail)
		switch st {
		case jxlStatusSuccess:
			return buf[:used:used], nil
		case jxlStatusNeedMoreOutput:
			if len(buf) >= maxJPEGRecompressBytes*2 {
				return nil, errors.New("img: jxl output too large")
			}
			nb := make([]byte, len(buf)*2)
			copy(nb, buf[:used])
			buf = nb
		default:
			return nil, errors.New("img: jxl encoding failed")
		}
	}
}

func jxlNativeReconstructJPEG(jxl []byte, sizeHint int) ([]byte, error) {
	if err := jxlUnavailable(); err != nil {
		return nil, err
	}
	var pin runtime.Pinner
	defer pin.Unpin()

	dec := jxlDecoderCreate(0)
	if dec == 0 {
		return nil, errors.New("img: jxl decoder creation failed")
	}
	defer jxlDecoderDestroy(dec)

	if jxlDecoderSubscribeEvents(dec, jxlDecJPEGReconstructed|jxlDecFullImage) != jxlStatusSuccess {
		return nil, errors.New("img: jxl cannot subscribe to events")
	}
	pin.Pin(unsafe.SliceData(jxl))
	if jxlDecoderSetInput(dec, unsafe.Pointer(unsafe.SliceData(jxl)), uint64(len(jxl))) != jxlStatusSuccess {
		return nil, errors.New("img: jxl cannot set input")
	}
	jxlDecoderCloseInput(dec)

	var buf []byte
	setBuffer := func(b []byte) bool {
		pin.Pin(unsafe.SliceData(b))
		return jxlDecoderSetJPEGBuffer(dec, unsafe.Pointer(unsafe.SliceData(b)), uint64(len(b))) == jxlStatusSuccess
	}
	reconstructing := false
	for {
		st := jxlDecoderProcessInput(dec)
		runtime.KeepAlive(jxl)
		runtime.KeepAlive(buf)
		switch st {
		case jxlDecJPEGReconstructed:
			buf = make([]byte, max(sizeHint+16, 4096))
			if !setBuffer(buf) {
				return nil, errors.New("img: jxl cannot set the jpeg buffer")
			}
			reconstructing = true
		case jxlDecJPEGNeedMoreOut:
			remaining := int(jxlDecoderReleaseJPEGBuffer(dec))
			used := len(buf) - remaining
			if len(buf) >= maxJPEGRecompressBytes*2 {
				return nil, errors.New("img: reconstructed jpeg too large")
			}
			nb := make([]byte, len(buf)*2)
			copy(nb, buf[:used])
			// The tail always ends at len(buf), so remaining is relative to it.
			pin.Pin(unsafe.SliceData(nb))
			if jxlDecoderSetJPEGBuffer(dec, unsafe.Pointer(&nb[used]), uint64(len(nb)-used)) != jxlStatusSuccess {
				return nil, errors.New("img: jxl cannot grow the jpeg buffer")
			}
			buf = nb
		case jxlDecFullImage:
		case jxlStatusSuccess:
			if !reconstructing {
				return nil, errors.New("img: jxl file has no jpeg reconstruction data")
			}
			remaining := int(jxlDecoderReleaseJPEGBuffer(dec))
			return buf[: len(buf)-remaining : len(buf)-remaining], nil
		case jxlDecNeedMoreInput:
			return nil, errors.New("img: truncated jxl file")
		default:
			return nil, fmt.Errorf("img: jxl decoding failed (status %d)", st)
		}
	}
}
