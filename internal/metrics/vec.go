package metrics

import (
	"encoding/binary"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

type entry[T any] struct {
	values []string
	m      *T
}

// vec is a copy-on-write map of label sets to metrics: reads are lock-free
// and allocation-free, creation (rare, bounded by max) takes a mutex.
type vec[T any] struct {
	d      desc
	labels []string
	newFn  func() *T
	render func(b []byte, d *desc, labels, values []string, m *T) []byte

	maxSeries atomic.Int64
	series    atomic.Int64
	m         atomic.Pointer[map[string]*entry[T]]
	overflow  atomic.Pointer[entry[T]]
	mu        sync.Mutex
}

func newVec[T any](d desc, labels []string, newFn func() *T, render func(b []byte, d *desc, labels, values []string, m *T) []byte) *vec[T] {
	v := &vec[T]{d: d, labels: slices.Clone(labels), newFn: newFn, render: render}
	v.maxSeries.Store(DefaultMaxSeries)
	m := map[string]*entry[T]{}
	if len(labels) == 0 {
		m[""] = &entry[T]{m: newFn()}
	}
	v.m.Store(&m)
	return v
}

func (v *vec[T]) describe() *desc { return &v.d }

// SetMaxSeries changes the cardinality cap. Label sets created beyond it
// are folded into one series whose label values are all "other".
func (v *vec[T]) SetMaxSeries(n int) {
	if n < 1 {
		panic("metrics: max series must be at least 1")
	}
	v.maxSeries.Store(int64(n))
}

// Len returns the number of series currently held, including the overflow
// series if it exists.
func (v *vec[T]) Len() int { return len(*v.m.Load()) }

func appendKey(b []byte, values []string) []byte {
	for _, s := range values {
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	return b
}

// With returns the metric for the given label values, creating it on first
// use. It panics if the number of values differs from the number of labels.
func (v *vec[T]) With(values ...string) *T {
	if len(values) != len(v.labels) {
		panic(fmt.Sprintf("metrics: %s expects %d label values, got %d", v.d.name, len(v.labels), len(values)))
	}
	var buf [128]byte
	key := appendKey(buf[:0], values)
	if e, ok := (*v.m.Load())[string(key)]; ok {
		return e.m
	}
	return v.create(values, string(key))
}

func (v *vec[T]) create(values []string, key string) *T {
	if v.series.Load() >= v.maxSeries.Load() {
		return v.fold()
	}
	v.mu.Lock()
	cur := *v.m.Load()
	if e, ok := cur[key]; ok {
		v.mu.Unlock()
		return e.m
	}
	if v.series.Load() >= v.maxSeries.Load() {
		v.mu.Unlock()
		return v.fold()
	}
	e := &entry[T]{values: cloneStrings(values), m: v.newFn()}
	v.publish(cur, key, e)
	v.series.Add(1)
	v.mu.Unlock()
	return e.m
}

func (v *vec[T]) fold() *T {
	overflowTotal.Add(1)
	if e := v.overflow.Load(); e != nil {
		return e.m
	}
	other := make([]string, len(v.labels))
	for i := range other {
		other[i] = OverflowLabelValue
	}
	key := string(appendKey(nil, other))
	v.mu.Lock()
	defer v.mu.Unlock()
	if e := v.overflow.Load(); e != nil {
		return e.m
	}
	cur := *v.m.Load()
	e, ok := cur[key]
	if !ok {
		e = &entry[T]{values: other, m: v.newFn()}
		v.publish(cur, key, e)
	}
	v.overflow.Store(e)
	return e.m
}

func (v *vec[T]) publish(cur map[string]*entry[T], key string, e *entry[T]) {
	next := make(map[string]*entry[T], len(cur)+1)
	for k, x := range cur {
		next[k] = x
	}
	next[key] = e
	v.m.Store(&next)
}

func cloneStrings(s []string) []string {
	out := make([]string, len(s))
	for i, x := range s {
		out[i] = strings.Clone(x)
	}
	return out
}

func (v *vec[T]) write(b []byte) []byte {
	cur := *v.m.Load()
	entries := make([]*entry[T], 0, len(cur))
	for _, e := range cur {
		entries = append(entries, e)
	}
	slices.SortFunc(entries, func(a, b *entry[T]) int { return slices.Compare(a.values, b.values) })
	b = appendHeader(b, &v.d)
	for _, e := range entries {
		b = v.render(b, &v.d, v.labels, e.values, e.m)
	}
	return b
}

type funcFamily struct {
	d  desc
	fn func() float64
}

func (f *funcFamily) describe() *desc { return &f.d }

func (f *funcFamily) write(b []byte) []byte {
	b = appendHeader(b, &f.d)
	return appendFloatSample(b, f.d.name, nil, nil, f.fn())
}
