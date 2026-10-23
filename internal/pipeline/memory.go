package pipeline

import (
	"context"
	"time"
)

// The memory budget bounds what the pipeline holds in buffers and pictures at
// the same time. A request that does not find room waits a little, as it does
// for a CPU slot, before the response goes through as it came.

// reserve takes n bytes of the budget, waiting up to TranscodeWait for them. A
// request for more than the whole budget never succeeds.
func (p *Pipeline) reserve(ctx context.Context, n int64) bool {
	if n <= 0 {
		return true
	}
	if n > p.cfg.MemoryBudget {
		return false
	}
	start := time.Now()
	ok := p.mem.TryAcquire(n)
	if !ok {
		wctx, cancel := context.WithTimeout(ctx, p.cfg.TranscodeWait)
		ok = p.mem.Acquire(wctx, n) == nil
		cancel()
		if in := p.inst.Load(); in != nil {
			in.memoryWait.With(waitResult(ok)).Observe(time.Since(start).Seconds())
		}
	}
	if ok {
		p.memUsed.Add(n)
	}
	return ok
}

// reserveWait takes n bytes of the budget and waits for them as long as ctx
// allows, for the work that no response is waiting for. It polls instead of
// queueing: the semaphore serves in order, so a job that waits in it would hold
// back every response that arrives after it. A response that waits for room
// goes first, because TryAcquire fails while anything is queued.
func (p *Pipeline) reserveWait(ctx context.Context, n int64) bool {
	if n <= 0 {
		return true
	}
	if n > p.cfg.MemoryBudget {
		return false
	}
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for {
		if p.mem.TryAcquire(n) {
			p.memUsed.Add(n)
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		}
	}
}

func waitResult(ok bool) string {
	if ok {
		return "got"
	}
	return "gave-up"
}

func (p *Pipeline) unreserve(n int64) {
	if n <= 0 {
		return
	}
	p.mem.Release(n)
	p.memUsed.Add(-n)
}

// reservation is what one response holds of the budget, which grows as the work
// needs more and is given back all at once.
type reservation struct {
	p *Pipeline
	n int64
}

func (p *Pipeline) newReservation() *reservation { return &reservation{p: p} }

// grow makes the reservation total bytes in all, taking the difference. It
// does not shrink one.
func (r *reservation) grow(ctx context.Context, total int64) bool {
	if total <= r.n {
		return true
	}
	if !r.p.reserve(ctx, total-r.n) {
		return false
	}
	r.n = total
	return true
}

func (r *reservation) release() {
	r.p.unreserve(r.n)
	r.n = 0
}
