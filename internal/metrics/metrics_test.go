package metrics

import (
	"compress/gzip"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gather(t testing.TB, r *Registry) string {
	t.Helper()
	return string(r.Gather(nil))
}

func mustParse(t testing.TB, text string) *exposition {
	t.Helper()
	e, err := parseExposition(text)
	require.NoError(t, err, text)
	return e
}

func TestGoldenCounter(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounter("a_total", "Help with \\ and\nnewline.")
	c.Add(3)
	c.Inc()
	v := r.NewCounterVec("b_total", "Labeled.", "path", "code")
	v.With(`a"b\c`+"\nd", "200").Add(2)
	v.With("/", "500").Inc()
	fc := r.NewFloatCounter("c_seconds_total", "Seconds.")
	fc.Add(0.25)
	fc.Add(-5)
	fc.Add(math.NaN())
	fc.Add(0.5)

	want := `# HELP a_total Help with \\ and\nnewline.
# TYPE a_total counter
a_total 4
# HELP b_total Labeled.
# TYPE b_total counter
b_total{path="/",code="500"} 1
b_total{path="a\"b\\c\nd",code="200"} 2
# HELP c_seconds_total Seconds.
# TYPE c_seconds_total counter
c_seconds_total 0.75
# HELP midopress_metrics_overflow_total Label sets folded into the overflow series because a metric reached its cardinality cap.
# TYPE midopress_metrics_overflow_total counter
midopress_metrics_overflow_total ` + fmt.Sprint(overflowTotal.Load()) + "\n"
	got := gather(t, r)
	assert.Equal(t, want, got)
	mustParse(t, got)
}

func TestGoldenGauge(t *testing.T) {
	r := NewRegistry()
	g := r.NewGauge("g", "A gauge.")
	g.Set(1.5)
	g.Inc()
	g.Dec()
	g.Add(2)
	g.Sub(0.5)
	gv := r.NewGaugeVec("gv", "Vec.", "k")
	gv.With("nan").Set(math.NaN())
	gv.With("pinf").Set(math.Inf(1))
	gv.With("ninf").Set(math.Inf(-1))
	r.NewGaugeFunc("gf", "Func.", func() float64 { return 42 })
	r.NewCounterFunc("cf_total", "Func.", func() float64 { return 7 })

	got := gather(t, r)
	for _, line := range []string{
		"g 3\n", "gf 42\n", "cf_total 7\n",
		`gv{k="nan"} NaN` + "\n", `gv{k="pinf"} +Inf` + "\n", `gv{k="ninf"} -Inf` + "\n",
	} {
		assert.Contains(t, got, line)
	}
	assert.Less(t, strings.Index(got, `k="ninf"`), strings.Index(got, `k="pinf"`))
	mustParse(t, got)
}

func TestGoldenHistogram(t *testing.T) {
	r := NewRegistry()
	h := r.NewHistogram("h_seconds", "Hist.", []float64{0.005, 1, 10, math.Inf(1)})
	for _, v := range []float64{0.001, 0.005, 0.5, 1, 11, math.NaN()} {
		h.Observe(v)
	}
	hv := r.NewHistogramVec("hv", "Vec.", []float64{1, 2.5}, "who")
	hv.With(`q"`).Observe(2)

	want := `# HELP h_seconds Hist.
# TYPE h_seconds histogram
h_seconds_bucket{le="0.005"} 2
h_seconds_bucket{le="1"} 4
h_seconds_bucket{le="10"} 4
h_seconds_bucket{le="+Inf"} 5
h_seconds_sum 12.506
h_seconds_count 5
# HELP hv Vec.
# TYPE hv histogram
hv_bucket{who="q\"",le="1"} 0
hv_bucket{who="q\"",le="2.5"} 1
hv_bucket{who="q\"",le="+Inf"} 1
hv_sum{who="q\""} 2
hv_count{who="q\""} 1
`
	got := gather(t, r)
	assert.Contains(t, got, want)
	mustParse(t, got)
	assert.Equal(t, uint64(5), h.Count())
	assert.InDelta(t, 12.506, h.Sum(), 1e-9)
}

func TestFullRegistryParses(t *testing.T) {
	r := NewRegistry()
	r.MustRegisterGoCollector()
	r.MustRegisterProcessCollector()
	bi := r.NewBuildInfo("v0")
	bi.Set(`1.2"3`)
	r.NewCounterVec("cv_total", "x", "a").With("1").Inc()
	r.NewFloatCounterVec("fcv_total", "x", "a").With("1").Add(1)
	r.NewGaugeVec("gv", "x", "a").With("1").Set(1)
	r.NewHistogramVec("hv", "x", nil, "a").With("1").Observe(1)
	e := mustParse(t, gather(t, r))
	for _, n := range []string{"go_goroutines", "go_threads", "go_memstats_alloc_bytes", "go_memstats_heap_inuse_bytes", "process_start_time_seconds"} {
		v, ok := e.find(n)
		assert.True(t, ok, n)
		assert.Positive(t, v, n)
	}
	_, ok := e.find("go_gc_duration_seconds_count")
	assert.True(t, ok)
	_, ok = e.find("midopress_build_info", "version", `1.2"3`)
	assert.True(t, ok)
	if v, ok := e.find("process_resident_memory_bytes"); ok {
		assert.Positive(t, v)
	}
	if v, ok := e.find("process_open_fds"); ok {
		assert.Positive(t, v)
	}
}

func randomValue(rnd *rand.Rand) string {
	alphabet := []string{"a", "Z", "0", " ", "\"", "\\", "\n", "{", "}", ",", "=", "é", "世", "\xff", "\t", "#", "le"}
	n := rnd.Intn(8)
	var b strings.Builder
	for range n {
		b.WriteString(alphabet[rnd.Intn(len(alphabet))])
	}
	return b.String()
}

func TestFuzzedLabelValuesRoundTrip(t *testing.T) {
	r := NewRegistry()
	cv := r.NewCounterVec("cv_total", "help \\ \n \"", "a", "b")
	hv := r.NewHistogramVec("hv", "x", []float64{1, 5}, "a", "b")
	gv := r.NewGaugeVec("gv", "x", "a", "b")
	rnd := rand.New(rand.NewSource(1))
	type pair struct{ a, b string }
	want := map[pair]struct{}{}
	for range 300 {
		p := pair{randomValue(rnd), randomValue(rnd)}
		want[p] = struct{}{}
		cv.With(p.a, p.b).Inc()
		hv.With(p.a, p.b).Observe(rnd.Float64() * 8)
		gv.With(p.a, p.b).Set(rnd.NormFloat64())
	}
	e := mustParse(t, gather(t, r))
	for p := range want {
		v, ok := e.find("cv_total", "a", p.a, "b", p.b)
		require.True(t, ok, "%q", p)
		assert.Positive(t, v)
	}
}

func FuzzExposition(f *testing.F) {
	f.Add("a", "b")
	f.Add("\"\\\n", "}{")
	f.Add("\xff\xfe", "")
	f.Fuzz(func(t *testing.T, a, b string) {
		r := NewRegistry()
		r.NewCounterVec("c_total", "h", "x", "y").With(a, b).Inc()
		r.NewHistogramVec("h", "h", nil, "x", "y").With(a, b).Observe(0.3)
		e, err := parseExposition(gather(t, r))
		require.NoError(t, err)
		_, ok := e.find("c_total", "x", a, "y", b)
		require.True(t, ok)
	})
}

func TestVecKeyNoCollision(t *testing.T) {
	r := NewRegistry()
	cv := r.NewCounterVec("c_total", "x", "a", "b")
	cv.With("a\x01b", "c").Inc()
	cv.With("a", "b\x01c").Inc()
	cv.With("", "ab").Inc()
	cv.With("ab", "").Inc()
	assert.Equal(t, 4, cv.Len())
}

func TestVecPanicsOnWrongArity(t *testing.T) {
	r := NewRegistry()
	cv := r.NewCounterVec("c_total", "x", "a")
	assert.Panics(t, func() { cv.With() })
	assert.Panics(t, func() { cv.With("a", "b") })
}

func TestCardinalityCap(t *testing.T) {
	r := NewRegistry()
	cv := r.NewCounterVec("c_total", "x", "a", "b")
	cv.SetMaxSeries(10)
	before := overflowTotal.Load()
	for i := range 10 {
		cv.With(fmt.Sprint(i), "x").Inc()
	}
	assert.Equal(t, before, overflowTotal.Load())
	for i := range 100 {
		cv.With(fmt.Sprint("hostile", i), "y").Inc()
	}
	assert.Equal(t, before+100, overflowTotal.Load())
	assert.Equal(t, 11, cv.Len())
	cv.With("3", "x").Inc()

	e := mustParse(t, gather(t, r))
	v, ok := e.find("c_total", "a", "other", "b", "other")
	require.True(t, ok)
	assert.EqualValues(t, 100, v)
	v, _ = e.find("c_total", "a", "3", "b", "x")
	assert.EqualValues(t, 2, v)
	v, ok = e.find("midopress_metrics_overflow_total")
	require.True(t, ok)
	assert.EqualValues(t, before+100, v)
}

func TestCardinalityCapOtherAlreadyLegit(t *testing.T) {
	r := NewRegistry()
	cv := r.NewCounterVec("c_total", "x", "a")
	cv.SetMaxSeries(2)
	cv.With("other").Inc()
	cv.With("b").Inc()
	cv.With("c").Inc()
	cv.With("d").Inc()
	assert.Equal(t, 2, cv.Len())
	e := mustParse(t, gather(t, r))
	v, _ := e.find("c_total", "a", "other")
	assert.EqualValues(t, 3, v)
}

func TestCardinalityCapConcurrent(t *testing.T) {
	r := NewRegistry()
	hv := r.NewHistogramVec("h", "x", nil, "a")
	hv.SetMaxSeries(50)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 500 {
				hv.With(fmt.Sprint(g, "-", i)).Observe(1)
			}
		})
	}
	wg.Wait()
	assert.Equal(t, 51, hv.Len())
	mustParse(t, gather(t, r))
}

func TestRegistrationPanics(t *testing.T) {
	r := NewRegistry()
	r.NewCounter("ok_total", "x")
	r.NewHistogram("hist", "x", nil)
	for name, f := range map[string]func(){
		"dup counter":       func() { r.NewCounter("ok_total", "x") },
		"dup across types":  func() { r.NewGauge("ok_total", "x") },
		"dup builtin":       func() { r.NewCounter("midopress_metrics_overflow_total", "x") },
		"derived histogram": func() { r.NewCounter("hist_count", "x") },
		"empty name":        func() { r.NewCounter("", "x") },
		"digit start":       func() { r.NewCounter("1abc", "x") },
		"dash":              func() { r.NewGauge("a-b", "x") },
		"space":             func() { r.NewGaugeFunc("a b", "x", nil) },
		"bad label":         func() { r.NewCounterVec("x1", "x", "a-b") },
		"dunder label":      func() { r.NewCounterVec("x2", "x", "__a") },
		"dup label":         func() { r.NewCounterVec("x3", "x", "a", "a") },
		"le label":          func() { r.NewHistogramVec("x4", "x", nil, "le") },
		"unsorted buckets":  func() { r.NewHistogram("x5", "x", []float64{2, 1}) },
		"equal buckets":     func() { r.NewHistogram("x6", "x", []float64{1, 1}) },
		"nan bucket":        func() { r.NewHistogram("x7", "x", []float64{math.NaN()}) },
		"zero max":          func() { r.NewCounterVec("x8", "x", "a").SetMaxSeries(0) },
	} {
		assert.Panics(t, f, name)
	}
	assert.NotPanics(t, func() { r.NewCounter("colon:name_total", "x") })
	r.MustRegisterGoCollector()
	assert.Panics(t, r.MustRegisterGoCollector)
}

func TestFailedRegistrationDoesNotReserve(t *testing.T) {
	r := NewRegistry()
	assert.Panics(t, func() { r.NewCounterVec("a_total", "x", "bad-label") })
	assert.NotPanics(t, func() { r.NewCounter("a_total", "x") })
}

func TestHandler(t *testing.T) {
	r := NewRegistry()
	r.NewCounter("a_total", "x").Add(5)
	h := r.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	assert.Equal(t, 200, rec.Code)
	assert.Equal(t, "text/plain; version=0.0.4; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Empty(t, rec.Header().Get("Content-Encoding"))
	assert.Contains(t, rec.Header().Get("Vary"), "Accept-Encoding")
	assert.Equal(t, fmt.Sprint(rec.Body.Len()), rec.Header().Get("Content-Length"))
	plain := rec.Body.String()
	assert.Contains(t, plain, "a_total 5\n")

	for _, ae := range []string{"gzip", "br, gzip;q=0.5", "GZIP", "deflate,gzip"} {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.Header.Set("Accept-Encoding", ae)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"), ae)
		zr, err := gzip.NewReader(rec.Body)
		require.NoError(t, err)
		body, err := io.ReadAll(zr)
		require.NoError(t, err)
		assert.Equal(t, plain, string(body), ae)
	}

	for _, ae := range []string{"", "identity", "br", "gzip;q=0", "gzip; q=0.0", "xgzip"} {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		if ae != "" {
			req.Header.Set("Accept-Encoding", ae)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Empty(t, rec.Header().Get("Content-Encoding"), ae)
		assert.Equal(t, plain, rec.Body.String(), ae)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/metrics", nil))
	assert.Equal(t, 200, rec.Code)
	assert.Zero(t, rec.Body.Len())

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestDeterministicOrdering(t *testing.T) {
	r := NewRegistry()
	r.NewGauge("zzz", "x")
	cv := r.NewCounterVec("mmm_total", "x", "a", "b")
	r.NewGauge("aaa", "x")
	for _, v := range [][2]string{{"b", "1"}, {"a", "2"}, {"b", "0"}, {"a", "1"}} {
		cv.With(v[0], v[1]).Inc()
	}
	first := gather(t, r)
	for range 5 {
		require.Equal(t, first, gather(t, r))
	}
	idx := func(s string) int { return strings.Index(first, s) }
	assert.Less(t, idx("# HELP aaa"), idx("# HELP mmm_total"))
	assert.Less(t, idx("# HELP mmm_total"), idx("# HELP zzz"))
	assert.Less(t, idx(`a="a",b="1"`), idx(`a="a",b="2"`))
	assert.Less(t, idx(`a="a",b="2"`), idx(`a="b",b="0"`))
	assert.Less(t, idx(`a="b",b="0"`), idx(`a="b",b="1"`))
}

func TestCounterNeverDecreases(t *testing.T) {
	var c Counter
	c.Add(5)
	c.Inc()
	assert.EqualValues(t, 6, c.Value())
	var f FloatCounter
	f.Add(2)
	f.Add(-1)
	f.Add(0)
	f.Add(math.Inf(-1))
	assert.Equal(t, 2.0, f.Value())
}

func TestConcurrentStress(t *testing.T) {
	r := NewRegistry()
	cv := r.NewCounterVec("c_total", "x", "k")
	hv := r.NewHistogramVec("h", "x", []float64{1, 10, 100}, "k")
	gv := r.NewGaugeVec("g", "x", "k")
	fc := r.NewFloatCounter("f_total", "x")
	h := r.NewHistogram("h2", "x", nil)
	cv.SetMaxSeries(20)
	hv.SetMaxSeries(20)

	const workers, iters = 8, 3000
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var scrapes sync.WaitGroup
	scrapes.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := parseExposition(gather(t, r)); err != nil {
				t.Error(err)
				return
			}
		}
	})
	for w := range workers {
		wg.Go(func() {
			for i := range iters {
				k := fmt.Sprint((w + i) % 30)
				cv.With(k).Inc()
				hv.With(k).Observe(float64(i % 150))
				gv.With(k).Add(1)
				fc.Add(0.5)
				h.Observe(float64(i))
			}
		})
	}
	wg.Wait()
	close(stop)
	scrapes.Wait()

	e := mustParse(t, gather(t, r))
	var total float64
	for _, s := range e.families["c_total"].samples {
		total += s.value
	}
	assert.EqualValues(t, workers*iters, total)
	v, _ := e.find("h2_count")
	assert.EqualValues(t, workers*iters, v)
	v, _ = e.find("f_total")
	assert.InDelta(t, workers*iters*0.5, v, 1e-6)
}

func BenchmarkCounterVecWithInc(b *testing.B) {
	r := NewRegistry()
	cv := r.NewCounterVec("c_total", "x", "method", "code", "codec")
	cv.With("GET", "200", "br").Inc()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cv.With("GET", "200", "br").Inc()
		}
	})
}

func BenchmarkCounterInc(b *testing.B) {
	c := NewRegistry().NewCounter("c_total", "x")
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Inc()
		}
	})
}

func BenchmarkHistogramObserve(b *testing.B) {
	h := NewRegistry().NewHistogram("h", "x", DefBuckets)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		var i float64
		for pb.Next() {
			h.Observe(i * 0.001)
			i++
			if i > 12000 {
				i = 0
			}
		}
	})
}

func BenchmarkHistogramVecWithObserve(b *testing.B) {
	hv := NewRegistry().NewHistogramVec("h", "x", DefBuckets, "codec")
	hv.With("br").Observe(1)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			hv.With("br").Observe(0.3)
		}
	})
}

func BenchmarkScrape5000Series(b *testing.B) {
	r := NewRegistry()
	cv := r.NewCounterVec("c_total", "x", "a", "b")
	cv.SetMaxSeries(5000)
	hv := r.NewHistogramVec("h", "x", DefBuckets, "a")
	hv.SetMaxSeries(5000)
	for i := range 4000 {
		cv.With(fmt.Sprint(i/100), fmt.Sprint(i%100)).Add(uint64(i))
	}
	for i := range 1000 {
		hv.With(fmt.Sprint(i)).Observe(float64(i) / 100)
	}
	var buf []byte
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		buf = r.Gather(buf[:0])
	}
	b.SetBytes(int64(len(buf)))
}

func TestWithDoesNotAllocate(t *testing.T) {
	r := NewRegistry()
	cv := r.NewCounterVec("c_total", "x", "a", "b")
	hv := r.NewHistogramVec("h", "x", nil, "a")
	cv.With("x", "y")
	hv.With("x")
	allocs := testing.AllocsPerRun(1000, func() {
		cv.With("x", "y").Inc()
		hv.With("x").Observe(1)
	})
	assert.Zero(t, allocs)
}
