package img

import (
	"math"
	"sync/atomic"
	"time"
)

// costModel learns how long each format takes on this machine, with an
// exponentially weighted average of seconds per megapixel. Only the real
// measurements are trusted once there are some: the constants in attemptCost
// are what a typical machine did when they were written.
type costModel struct {
	perMP [8]atomic.Uint64 // float64 bits, indexed by Format; 0 means unknown
}

const costWeight = 0.3

// observe records that encoding, decoding and measuring pixels of f took d.
func (c *costModel) observe(f Format, pixels int, d time.Duration) {
	if int(f) >= len(c.perMP) || pixels < 20_000 {
		return
	}
	sample := d.Seconds() / (float64(pixels) / 1e6)
	for {
		old := c.perMP[f].Load()
		next := sample
		if old != 0 {
			next = costWeight*sample + (1-costWeight)*math.Float64frombits(old)
		}
		if c.perMP[f].CompareAndSwap(old, math.Float64bits(next)) {
			return
		}
	}
}

// estimate returns the learned seconds per megapixel of f, or false when none
// has been measured yet.
func (c *costModel) estimate(f Format) (float64, bool) {
	if int(f) >= len(c.perMP) {
		return 0, false
	}
	v := c.perMP[f].Load()
	return math.Float64frombits(v), v != 0
}
