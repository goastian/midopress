package img

import (
	"sync"
	"sync/atomic"
)

// unreachable remembers which formats could not reach a target even at their
// highest quality. WebP is the usual case with dssim: its chroma subsampling
// keeps it above 0.002 whatever the quality, so every image would spend two
// encodes to find that out again. After a few images in a row, the format is
// skipped for a profile and metric, and given another chance now and then.
type unreachable struct {
	m sync.Map // key -> *atomic.Int32 of consecutive images that failed
}

const (
	unreachableAfter = 3  // images in a row
	unreachableRetry = 24 // one in this many is tried anyway
)

func (u *unreachable) counter(f Format, p *Profile, m Metric) *atomic.Int32 {
	key := p.Name + "/" + string(m) + "/" + f.String()
	if c, ok := u.m.Load(key); ok {
		return c.(*atomic.Int32)
	}
	c, _ := u.m.LoadOrStore(key, new(atomic.Int32))
	return c.(*atomic.Int32)
}

// skip reports whether f should not be tried now.
func (u *unreachable) skip(f Format, p *Profile, m Metric) bool {
	n := u.counter(f, p, m).Add(0)
	if n < unreachableAfter {
		return false
	}
	// the count keeps growing while skipping, so that the retry comes by itself
	return u.counter(f, p, m).Add(1)%unreachableRetry != 0
}

// record notes how a search ended: reached the target, failed at the highest
// quality (ceiling), or ended for another reason that says nothing.
func (u *unreachable) record(f Format, p *Profile, m Metric, reached, ceiling bool) {
	c := u.counter(f, p, m)
	switch {
	case reached:
		c.Store(0)
	case ceiling:
		c.Add(1)
	}
}
