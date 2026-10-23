package img

import (
	"image"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestNativeCodecsUnderGCStress runs the encoders and decoders that load the
// libraries of the machine from many goroutines while the collector runs as
// often as it can. The bindings call into C without cgo, which is where a
// pointer the collector does not know about would show up as a crash. It is
// skipped unless asked for with MIDOPRESS_STRESS=seconds and the libraries are
// there:
//
//	LD_LIBRARY_PATH=dir-with-libwebp.so-libavif.so-libjxl.so GOGC=1 GODEBUG=invalidptr=1 MIDOPRESS_STRESS=60 go test -run NativeCodecsUnderGCStress ./internal/img/
func TestNativeCodecsUnderGCStress(t *testing.T) {
	secs, _ := strconv.Atoi(os.Getenv("MIDOPRESS_STRESS"))
	if secs <= 0 {
		t.Skip("set MIDOPRESS_STRESS=seconds to run")
	}
	be := NewWASM()
	var formats []Format
	for _, f := range []Format{WebP, AVIF, JXL} {
		if be.Native(f) {
			formats = append(formats, f)
		}
	}
	if len(formats) == 0 {
		t.Skip("no native library found")
	}
	t.Logf("native: %v", formats)

	pics := []*photoPic{{photoLike(160, 120, 1)}, {photoLike(257, 191, 2)}, {photoLike(64, 48, 3)}}
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var rounds int
	for g := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := g; time.Now().Before(deadline); i++ {
				f := formats[i%len(formats)]
				pic := pics[i%len(pics)].m
				data, err := be.Encode(f, pic, EncodeOptions{Quality: 40 + i%50, Effort: 1 + i%3})
				if err != nil {
					t.Errorf("encode %v: %v", f, err)
					return
				}
				got, err := be.Decode(f, data)
				if err != nil || got.Bounds() != pic.Bounds() {
					t.Errorf("decode %v: %v", f, err)
					return
				}
				mu.Lock()
				rounds++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	t.Logf("%d encode and decode rounds with no crash", rounds)
}

type photoPic struct{ m *image.RGBA }
