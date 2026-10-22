package metrics

import (
	"math"
	"os"
	"runtime/metrics"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var processStart = time.Now()

type funcWriter struct {
	d  desc
	fn func(b []byte, name string) []byte
}

func (f *funcWriter) describe() *desc { return &f.d }

func (f *funcWriter) write(b []byte) []byte {
	n := len(b)
	b = appendHeader(b, &f.d)
	hdr := len(b)
	b = f.fn(b, f.d.name)
	if len(b) == hdr {
		return b[:n]
	}
	return b
}

func readRuntimeUint(names ...string) float64 {
	samples := make([]metrics.Sample, len(names))
	for i, n := range names {
		samples[i].Name = n
	}
	metrics.Read(samples)
	var total uint64
	for _, s := range samples {
		if s.Value.Kind() == metrics.KindUint64 {
			total += s.Value.Uint64()
		}
	}
	return float64(total)
}

func (r *Registry) runtimeGauge(name, help string, rt ...string) {
	r.register(&funcFamily{
		d:  desc{name: name, help: help, typ: typeGauge},
		fn: func() float64 { return readRuntimeUint(rt...) },
	})
}

// MustRegisterGoCollector registers go_goroutines, go_threads,
// go_memstats_alloc_bytes, go_memstats_heap_inuse_bytes and
// go_gc_duration_seconds (summary with only _sum and _count).
//
// Everything is read through runtime/metrics, which does not stop the world,
// unlike runtime.ReadMemStats. go_memstats_heap_inuse_bytes is approximated
// as live-object bytes plus unused bytes in in-use spans. The GC pause sum is
// approximated from bucket midpoints of /gc/pauses:seconds; the count is exact.
// It panics on duplicate registration.
func (r *Registry) MustRegisterGoCollector() {
	r.runtimeGauge("go_goroutines", "Number of goroutines that currently exist.", "/sched/goroutines:goroutines")
	r.runtimeGauge("go_threads", "Number of OS threads created.", "/sched/threads/total:threads")
	r.runtimeGauge("go_memstats_alloc_bytes", "Number of bytes allocated in heap and currently in use.",
		"/memory/classes/heap/objects:bytes")
	r.runtimeGauge("go_memstats_heap_inuse_bytes", "Number of heap bytes that are in use.",
		"/memory/classes/heap/objects:bytes", "/memory/classes/heap/unused:bytes")
	r.register(&funcWriter{
		d: desc{
			name:     "go_gc_duration_seconds",
			help:     "A summary of the wall-time pause (stop-the-world) duration in garbage collection cycles.",
			typ:      typeSummary,
			reserved: []string{"go_gc_duration_seconds_sum", "go_gc_duration_seconds_count"},
		},
		fn: func(b []byte, name string) []byte {
			count, sum := gcPauses()
			b = appendFloatSample(b, name+"_sum", nil, nil, sum)
			return appendUintSample(b, name+"_count", nil, nil, count)
		},
	})
}

func gcPauses() (uint64, float64) {
	s := []metrics.Sample{{Name: "/gc/pauses:seconds"}}
	metrics.Read(s)
	if s[0].Value.Kind() != metrics.KindFloat64Histogram {
		return 0, 0
	}
	h := s[0].Value.Float64Histogram()
	var count uint64
	var sum float64
	for i, c := range h.Counts {
		lo, hi := h.Buckets[i], h.Buckets[i+1]
		switch {
		case math.IsInf(lo, -1):
			lo = hi
		case math.IsInf(hi, 1):
			hi = lo
		}
		count += c
		sum += float64(c) * (lo + hi) / 2
	}
	return count, sum
}

// MustRegisterProcessCollector registers process_start_time_seconds, and on
// Linux process_resident_memory_bytes and process_open_fds, which are
// silently omitted where /proc is unavailable. The start time is the time
// this package was initialised, which is within milliseconds of process start.
// It panics on duplicate registration.
func (r *Registry) MustRegisterProcessCollector() {
	r.register(&funcFamily{
		d:  desc{name: "process_start_time_seconds", help: "Start time of the process since unix epoch in seconds.", typ: typeGauge},
		fn: func() float64 { return float64(processStart.UnixNano()) / 1e9 },
	})
	if _, err := os.Stat("/proc/self/statm"); err != nil {
		return
	}
	r.register(&funcWriter{
		d: desc{name: "process_resident_memory_bytes", help: "Resident memory size in bytes.", typ: typeGauge},
		fn: func(b []byte, name string) []byte {
			raw, err := os.ReadFile("/proc/self/statm")
			if err != nil {
				return b
			}
			f := strings.Fields(string(raw))
			if len(f) < 2 {
				return b
			}
			pages, err := strconv.ParseUint(f[1], 10, 64)
			if err != nil {
				return b
			}
			return appendFloatSample(b, name, nil, nil, float64(pages)*float64(os.Getpagesize()))
		},
	})
	r.register(&funcWriter{
		d: desc{name: "process_open_fds", help: "Number of open file descriptors.", typ: typeGauge},
		fn: func(b []byte, name string) []byte {
			d, err := os.Open("/proc/self/fd")
			if err != nil {
				return b
			}
			defer func() { _ = d.Close() }()
			names, err := d.Readdirnames(-1)
			if err != nil {
				return b
			}
			return appendFloatSample(b, name, nil, nil, float64(len(names)))
		},
	})
}

// BuildInfo is the midopress_build_info metric, a constant 1 labeled with
// the version, which can be updated after registration.
type BuildInfo struct{ version atomic.Pointer[string] }

// Set changes the exposed version label.
func (bi *BuildInfo) Set(version string) { bi.version.Store(&version) }

// NewBuildInfo registers midopress_build_info{version="..."} 1.
func (r *Registry) NewBuildInfo(version string) *BuildInfo {
	bi := &BuildInfo{}
	bi.Set(version)
	r.register(&funcWriter{
		d: desc{name: "midopress_build_info", help: "A metric with a constant '1' value labeled by version.", typ: typeGauge},
		fn: func(b []byte, name string) []byte {
			return appendFloatSample(b, name, []string{"version"}, []string{*bi.version.Load()}, 1)
		},
	})
	return bi
}
