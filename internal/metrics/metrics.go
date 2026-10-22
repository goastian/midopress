package metrics

import (
	"math"
	"sort"
	"strconv"
	"sync/atomic"
)

func addFloat(u *atomic.Uint64, d float64) {
	for {
		old := u.Load()
		if u.CompareAndSwap(old, math.Float64bits(math.Float64frombits(old)+d)) {
			return
		}
	}
}

// Counter is a monotonically increasing integer metric.
type Counter struct{ v atomic.Uint64 }

// Inc adds 1 with a single atomic add.
func (c *Counter) Inc() { c.v.Add(1) }

// Add adds n.
func (c *Counter) Add(n uint64) { c.v.Add(n) }

// Value returns the current count.
func (c *Counter) Value() uint64 { return c.v.Load() }

// FloatCounter is a monotonically increasing float metric, for quantities
// such as seconds. Negative and NaN additions are ignored.
type FloatCounter struct{ v atomic.Uint64 }

// Add adds d if it is positive.
func (c *FloatCounter) Add(d float64) {
	if d > 0 {
		addFloat(&c.v, d)
	}
}

// Value returns the current value.
func (c *FloatCounter) Value() float64 { return math.Float64frombits(c.v.Load()) }

// Gauge is a float metric that can go up and down.
type Gauge struct{ v atomic.Uint64 }

// Set sets the gauge to f.
func (g *Gauge) Set(f float64) { g.v.Store(math.Float64bits(f)) }

// Add adds d, which may be negative.
func (g *Gauge) Add(d float64) { addFloat(&g.v, d) }

// Sub subtracts d.
func (g *Gauge) Sub(d float64) { addFloat(&g.v, -d) }

// Inc adds 1.
func (g *Gauge) Inc() { addFloat(&g.v, 1) }

// Dec subtracts 1.
func (g *Gauge) Dec() { addFloat(&g.v, -1) }

// Value returns the current value.
func (g *Gauge) Value() float64 { return math.Float64frombits(g.v.Load()) }

// DefBuckets are default histogram buckets, in seconds.
var DefBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

// Histogram samples observations into cumulative buckets.
type Histogram struct {
	upper  []float64
	counts []atomic.Uint64 // per-bucket (non-cumulative); last is +Inf
	sum    atomic.Uint64
}

func newHistogram(upper []float64) *Histogram {
	return &Histogram{upper: upper, counts: make([]atomic.Uint64, len(upper)+1)}
}

// Observe records v. NaN observations are dropped.
func (h *Histogram) Observe(v float64) {
	if v != v {
		return
	}
	i := sort.SearchFloat64s(h.upper, v)
	h.counts[i].Add(1)
	addFloat(&h.sum, v)
}

// Count returns the number of observations.
func (h *Histogram) Count() uint64 {
	var n uint64
	for i := range h.counts {
		n += h.counts[i].Load()
	}
	return n
}

// Sum returns the sum of observations.
func (h *Histogram) Sum() float64 { return math.Float64frombits(h.sum.Load()) }

func checkBuckets(buckets []float64) []float64 {
	if len(buckets) == 0 {
		buckets = DefBuckets
	}
	out := append([]float64(nil), buckets...)
	if math.IsInf(out[len(out)-1], 1) {
		out = out[:len(out)-1]
	}
	for i, b := range out {
		if math.IsNaN(b) || math.IsInf(b, 0) {
			panic("metrics: histogram bucket must be finite")
		}
		if i > 0 && b <= out[i-1] {
			panic("metrics: histogram buckets must be strictly increasing")
		}
	}
	return out
}

func histogramRenderer(upper []float64) func(b []byte, d *desc, labels, values []string, h *Histogram) []byte {
	le := make([]string, len(upper))
	for i, u := range upper {
		le[i] = string(strconv.AppendFloat(nil, u, 'g', -1, 64))
	}
	return func(b []byte, d *desc, labels, values []string, h *Histogram) []byte {
		var cum uint64
		for i := range le {
			cum += h.counts[i].Load()
			b = appendSeries(b, d.name+"_bucket", labels, values, "le", le[i])
			b = strconv.AppendUint(b, cum, 10)
			b = append(b, '\n')
		}
		cum += h.counts[len(le)].Load()
		b = appendSeries(b, d.name+"_bucket", labels, values, "le", "+Inf")
		b = strconv.AppendUint(b, cum, 10)
		b = append(b, '\n')
		b = appendFloatSample(b, d.name+"_sum", labels, values, h.Sum())
		return appendUintSample(b, d.name+"_count", labels, values, cum)
	}
}

func renderCounter(b []byte, d *desc, labels, values []string, c *Counter) []byte {
	return appendUintSample(b, d.name, labels, values, c.Value())
}

func renderFloatCounter(b []byte, d *desc, labels, values []string, c *FloatCounter) []byte {
	return appendFloatSample(b, d.name, labels, values, c.Value())
}

func renderGauge(b []byte, d *desc, labels, values []string, g *Gauge) []byte {
	return appendFloatSample(b, d.name, labels, values, g.Value())
}

// CounterVec is a set of Counters keyed by label values.
type CounterVec struct{ *vec[Counter] }

// FloatCounterVec is a set of FloatCounters keyed by label values.
type FloatCounterVec struct{ *vec[FloatCounter] }

// GaugeVec is a set of Gauges keyed by label values.
type GaugeVec struct{ *vec[Gauge] }

// HistogramVec is a set of Histograms keyed by label values.
type HistogramVec struct{ *vec[Histogram] }

func (r *Registry) add(name, help, typ string, reserved ...string) desc {
	return desc{name: name, help: help, typ: typ, reserved: reserved}
}

// NewCounter registers an unlabeled counter.
func (r *Registry) NewCounter(name, help string) *Counter {
	v := newVec(r.add(name, help, typeCounter), nil, func() *Counter { return new(Counter) }, renderCounter)
	r.register(v)
	return v.With()
}

// NewCounterVec registers a labeled counter.
func (r *Registry) NewCounterVec(name, help string, labels ...string) *CounterVec {
	checkLabels(labels, false)
	v := newVec(r.add(name, help, typeCounter), labels, func() *Counter { return new(Counter) }, renderCounter)
	r.register(v)
	return &CounterVec{v}
}

// NewFloatCounter registers an unlabeled float counter.
func (r *Registry) NewFloatCounter(name, help string) *FloatCounter {
	v := newVec(r.add(name, help, typeCounter), nil, func() *FloatCounter { return new(FloatCounter) }, renderFloatCounter)
	r.register(v)
	return v.With()
}

// NewFloatCounterVec registers a labeled float counter.
func (r *Registry) NewFloatCounterVec(name, help string, labels ...string) *FloatCounterVec {
	checkLabels(labels, false)
	v := newVec(r.add(name, help, typeCounter), labels, func() *FloatCounter { return new(FloatCounter) }, renderFloatCounter)
	r.register(v)
	return &FloatCounterVec{v}
}

// NewGauge registers an unlabeled gauge.
func (r *Registry) NewGauge(name, help string) *Gauge {
	v := newVec(r.add(name, help, typeGauge), nil, func() *Gauge { return new(Gauge) }, renderGauge)
	r.register(v)
	return v.With()
}

// NewGaugeVec registers a labeled gauge.
func (r *Registry) NewGaugeVec(name, help string, labels ...string) *GaugeVec {
	checkLabels(labels, false)
	v := newVec(r.add(name, help, typeGauge), labels, func() *Gauge { return new(Gauge) }, renderGauge)
	r.register(v)
	return &GaugeVec{v}
}

// NewGaugeFunc registers a gauge whose value is computed at scrape time.
func (r *Registry) NewGaugeFunc(name, help string, fn func() float64) {
	r.register(&funcFamily{d: r.add(name, help, typeGauge), fn: fn})
}

// NewCounterFunc registers a counter whose value is computed at scrape time.
// The function must never return a smaller value than before.
func (r *Registry) NewCounterFunc(name, help string, fn func() float64) {
	r.register(&funcFamily{d: r.add(name, help, typeCounter), fn: fn})
}

// NewHistogram registers an unlabeled histogram. A trailing +Inf bucket is
// implicit; nil buckets means DefBuckets.
func (r *Registry) NewHistogram(name, help string, buckets []float64) *Histogram {
	upper := checkBuckets(buckets)
	v := newVec(r.add(name, help, typeHist, name+"_bucket", name+"_sum", name+"_count"), nil,
		func() *Histogram { return newHistogram(upper) }, histogramRenderer(upper))
	r.register(v)
	return v.With()
}

// NewHistogramVec registers a labeled histogram. The label name "le" is
// reserved.
func (r *Registry) NewHistogramVec(name, help string, buckets []float64, labels ...string) *HistogramVec {
	checkLabels(labels, true)
	upper := checkBuckets(buckets)
	v := newVec(r.add(name, help, typeHist, name+"_bucket", name+"_sum", name+"_count"), labels,
		func() *Histogram { return newHistogram(upper) }, histogramRenderer(upper))
	r.register(v)
	return &HistogramVec{v}
}
