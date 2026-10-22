// Package metrics is a small dependency-free Prometheus metrics library
// that renders the text exposition format 0.0.4. It is built for a proxy
// hot path: counter increments are single atomic adds, and looking up an
// existing series in a vector does not allocate.
//
// Registration mistakes (invalid or duplicate names, bad label sets, bad
// buckets) panic, like prometheus client_golang.
package metrics

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	typeCounter = "counter"
	typeGauge   = "gauge"
	typeHist    = "histogram"
	typeSummary = "summary"
)

// DefaultMaxSeries is the default cap on distinct label sets per vector.
const DefaultMaxSeries = 1000

// OverflowLabelValue is the value given to every label of the series that
// absorbs label sets created past a vector's cardinality cap.
const OverflowLabelValue = "other"

var (
	metricNameRE = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	labelNameRE  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// overflowTotal counts label sets folded into overflow series, across all
// registries.
var overflowTotal atomic.Uint64

type desc struct {
	name     string
	help     string
	typ      string
	reserved []string
}

type family interface {
	describe() *desc
	write(b []byte) []byte
}

// Registry holds metric families and renders them. The zero value is not
// usable; use NewRegistry.
type Registry struct {
	mu    sync.Mutex
	names map[string]struct{}
	fams  []family
}

// NewRegistry returns an empty registry that already exposes
// midopress_metrics_overflow_total.
func NewRegistry() *Registry {
	r := &Registry{names: make(map[string]struct{})}
	r.register(&funcFamily{
		d: desc{
			name: "midopress_metrics_overflow_total",
			help: "Label sets folded into the overflow series because a metric reached its cardinality cap.",
			typ:  typeCounter,
		},
		fn: func() float64 { return float64(overflowTotal.Load()) },
	})
	return r
}

func (r *Registry) register(f family) {
	d := f.describe()
	if !metricNameRE.MatchString(d.name) {
		panic(fmt.Sprintf("metrics: invalid metric name %q", d.name))
	}
	all := append([]string{d.name}, d.reserved...)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range all {
		if _, dup := r.names[n]; dup {
			panic(fmt.Sprintf("metrics: duplicate metric name %q", n))
		}
	}
	for _, n := range all {
		r.names[n] = struct{}{}
	}
	r.fams = append(r.fams, f)
}

func (r *Registry) snapshot() []family {
	r.mu.Lock()
	fams := slices.Clone(r.fams)
	r.mu.Unlock()
	slices.SortFunc(fams, func(a, b family) int {
		return strings.Compare(a.describe().name, b.describe().name)
	})
	return fams
}

// Gather renders every registered metric in text format 0.0.4, appending to b.
func (r *Registry) Gather(b []byte) []byte {
	for _, f := range r.snapshot() {
		b = f.write(b)
	}
	return b
}

func checkLabels(labels []string, reserveLE bool) {
	seen := make(map[string]struct{}, len(labels))
	for _, l := range labels {
		if !labelNameRE.MatchString(l) || strings.HasPrefix(l, "__") {
			panic(fmt.Sprintf("metrics: invalid label name %q", l))
		}
		if reserveLE && l == "le" {
			panic(`metrics: label name "le" is reserved for histograms`)
		}
		if _, dup := seen[l]; dup {
			panic(fmt.Sprintf("metrics: duplicate label name %q", l))
		}
		seen[l] = struct{}{}
	}
}
