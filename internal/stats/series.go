package stats

import (
	"math"
	"sync"
)

const (
	secondPoints = 120
	minutePoints = 180

	latBuckets = 128
	latMinutes = 5
	latMin     = 50e3 // nanoseconds
	latMax     = 60e9

	nLabels = maxLabels + 1
)

type point struct {
	epoch    int64
	requests uint64
	bytesIn  uint64
	bytesOut uint64
}

// slot returns the point of the interval ep in the ring, reset when it still
// holds an older interval, or nil when it already holds a newer one (the event
// is older than the window).
func slot(ring []point, ep int64) *point {
	p := &ring[ep%int64(len(ring))]
	if p.epoch != ep {
		if p.epoch > ep {
			return nil
		}
		*p = point{epoch: ep}
	}
	return p
}

type histSlot struct {
	epoch  int64
	counts [latBuckets]uint64
}

// stripe is an independent set of counters behind its own lock. The lock is
// held for a few plain increments, and Record spreads over the stripes, so
// there is no shared cache line on the hot path. Time series and the latency
// histograms live here too, and a slot is reset lazily by the first event of a
// newer interval.
type stripe struct {
	mu       sync.Mutex
	kinds    [len(kindNames)]Bucket
	codecs   [nLabels]Bucket
	profiles [nLabels]Bucket
	reasons  [nLabels]uint64
	seconds  [secondPoints]point
	minutes  [minutePoints]point
	hist     [latMinutes]histSlot
	_        [64]byte
}

func (s *stripe) record(unix int64, ki, ci, pi, ri int, tr, in, out uint64, lat int) {
	b := Bucket{1, tr, in, out}
	s.kinds[ki] = s.kinds[ki].plus(b)
	s.codecs[ci] = s.codecs[ci].plus(b)
	s.profiles[pi] = s.profiles[pi].plus(b)
	s.reasons[ri]++
	if p := slot(s.seconds[:], unix); p != nil {
		p.requests++
		p.bytesIn += in
		p.bytesOut += out
	}
	if p := slot(s.minutes[:], unix/60); p != nil {
		p.requests++
		p.bytesIn += in
		p.bytesOut += out
	}
	if lat >= 0 {
		m := unix / 60
		h := &s.hist[m%latMinutes]
		if h.epoch != m {
			if h.epoch > m {
				return
			}
			*h = histSlot{epoch: m}
		}
		h.counts[lat]++
	}
}

// addPoints adds the intervals of the ring that fall in out, whose last point
// is the interval now and which step seconds long each.
func addPoints(out []SeriesPoint, ring []point, now int64) {
	last := now - int64(len(out)-1)
	for i := range ring {
		p := &ring[i]
		if p.epoch < last || p.epoch > now {
			continue
		}
		o := &out[p.epoch-last]
		o.Requests += p.requests
		o.BytesIn += p.bytesIn
		o.BytesOut += p.bytesOut
	}
}

func newPoints(n int, now, step int64) []SeriesPoint {
	out := make([]SeriesPoint, n)
	for i := range out {
		out[i].T = (now - int64(n-1) + int64(i)) * step
	}
	return out
}

var (
	latLogRatio = math.Log(latMax/latMin) / latBuckets
	latBounds   = func() (b [latBuckets + 1]float64) {
		for i := range b {
			b[i] = latMin * math.Exp(float64(i)*latLogRatio) / 1e6
		}
		return b
	}()
)

var latPerOctave = math.Ln2 / latLogRatio

var latBoundsNs = func() (b [latBuckets]int64) {
	for i := range b {
		b[i] = int64(latBounds[i] * 1e6)
	}
	return b
}()

// latIndex is the bucket of a duration. The buckets are log-spaced from 50
// microseconds to 60 seconds, so a quantile is off by at most the width of a
// bucket, about 11.6 percent, for durations in that range; shorter ones fall
// in the first bucket and longer ones in the last.
func latIndex(ns int64) int {
	if ns <= latMin {
		return 0
	}
	// A linear approximation of log2 from the float bits gives a first guess
	// that is at most one bucket off; the bounds table settles it.
	bits := math.Float64bits(float64(ns) / latMin)
	l2 := float64(int(bits>>52)-1023) + float64(bits&(1<<52-1))/(1<<52)
	i := min(int(l2*latPerOctave), latBuckets-1)
	for i < latBuckets-1 && latBoundsNs[i+1] <= ns {
		i++
	}
	for i > 0 && latBoundsNs[i] > ns {
		i--
	}
	return i
}

// addHist adds the histograms of the last five minutes, the current one
// included, and returns how many samples they hold.
func (s *stripe) addHist(h *[latBuckets]uint64, now int64) uint64 {
	var n uint64
	for i := range s.hist {
		hs := &s.hist[i]
		if hs.epoch > now || hs.epoch <= now-latMinutes {
			continue
		}
		for k, c := range hs.counts {
			h[k] += c
			n += c
		}
	}
	return n
}

// quantile interpolates linearly inside the bucket that holds the rank.
func quantile(h *[latBuckets]uint64, total uint64, q float64) float64 {
	target := q * float64(total)
	var cum float64
	for i, n := range h {
		if n == 0 {
			continue
		}
		if cum+float64(n) >= target {
			frac := (target - cum) / float64(n)
			v := latBounds[i] + frac*(latBounds[i+1]-latBounds[i])
			return math.Round(v*1000) / 1000
		}
		cum += float64(n)
	}
	return latBounds[latBuckets]
}
