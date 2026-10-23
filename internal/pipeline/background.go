package pipeline

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/xcache"
)

// background finishes image jobs that did not fit in the time a request could
// wait. The slow formats (AVIF, JPEG XL) are the ones that squeeze most, so the
// first request gets a quick answer and the next ones get the best one from
// the cache.
type background struct {
	p      *Pipeline
	ctx    context.Context
	cancel context.CancelFunc
	queue  chan bgJob
	once   sync.Once
	wg     sync.WaitGroup

	mu       sync.Mutex
	inflight map[string]struct{}
	// queued is what the jobs that wait hold, in bytes: the picture they will
	// recode, or the body they will compress.
	queued atomic.Int64
}

type bgJob struct {
	key     string
	in      img.Input
	expires time.Time
	// fn, when set, is the job instead of an image: it runs on a CPU slot.
	fn func(ctx context.Context)
	// size is what the job holds while it waits, weight what it needs of the
	// memory budget while it runs: it waits for that before it takes a CPU slot,
	// so a response that found no room is not lost, only late.
	size, weight int64
}

const bgQueueLen = 32

func newBackground(p *Pipeline) *background {
	ctx, cancel := context.WithCancel(context.Background())
	return &background{p: p, ctx: ctx, cancel: cancel, queue: make(chan bgJob, bgQueueLen), inflight: map[string]struct{}{}}
}

func (b *background) enabled() bool {
	return b.p.cfg.Image.Background
}

// submit queues a job unless the same one is already waiting or the queue is
// full. in.Raw is kept until the job is done and must not be modified.
func (b *background) submit(key string, in img.Input, expires time.Time, weight int64) {
	b.enqueue(bgJob{key: key, in: in, expires: expires, size: int64(len(in.Raw)), weight: weight})
}

// enqueue queues a job unless one with the same key is there, the queue is full,
// the jobs that wait already hold a quarter of the memory budget, or the job
// needs more than the whole budget and would never run.
func (b *background) enqueue(j bgJob) {
	if !b.enabled() || b.ctx.Err() != nil || j.weight > b.p.cfg.MemoryBudget {
		return
	}
	if j.size > 0 && b.queued.Add(j.size) > b.p.cfg.MemoryBudget/4 {
		b.queued.Add(-j.size)
		return
	}
	b.start()
	b.mu.Lock()
	if _, ok := b.inflight[j.key]; ok {
		b.mu.Unlock()
		b.queued.Add(-j.size)
		return
	}
	b.inflight[j.key] = struct{}{}
	b.mu.Unlock()
	select {
	case b.queue <- j:
	default:
		b.finish(j)
	}
}

func (b *background) start() {
	b.once.Do(func() {
		workers := max(1, runtime.GOMAXPROCS(0)/4)
		for range workers {
			b.wg.Add(1)
			go b.work()
		}
	})
}

// submitFunc queues fn, which runs on a CPU slot, unless a job with the same
// key is already waiting or the queue is full.
func (b *background) submitFunc(key string, size int64, fn func(ctx context.Context)) {
	b.enqueue(bgJob{key: key, fn: fn, size: size})
}

func (b *background) finish(j bgJob) {
	b.mu.Lock()
	delete(b.inflight, j.key)
	b.mu.Unlock()
	b.queued.Add(-j.size)
}

func (b *background) work() {
	defer b.wg.Done()
	for {
		select {
		case <-b.ctx.Done():
			return
		case j := <-b.queue:
			b.run(j)
		}
	}
}

func (b *background) run(j bgJob) {
	defer b.finish(j)
	defer func() { _ = recover() }()
	p := b.p
	if j.weight > 0 {
		// the memory first, and then the slot: waiting for room must not hold
		// a slot that something else could use
		wctx, cancel := context.WithTimeout(b.ctx, p.cfg.Image.BackgroundTimeout)
		ok := p.reserveWait(wctx, j.weight)
		cancel()
		if !ok {
			p.recordBackground("no-memory")
			return
		}
		defer p.unreserve(j.weight)
	}
	select {
	case p.sem <- struct{}{}:
	case <-b.ctx.Done():
		return
	}
	defer p.releaseCPU()
	ctx, cancel := context.WithTimeout(b.ctx, p.cfg.Image.BackgroundTimeout)
	defer cancel()
	if j.fn != nil {
		j.fn(ctx)
		return
	}
	j.in.Budget = 0
	res, err := p.engine.Optimize(ctx, j.in)
	if err != nil {
		p.recordBackground("failed")
		if errors.Is(err, img.ErrBudget) || errors.Is(err, img.ErrUnsupported) {
			// with no limit of time, that is a verdict on the image itself
			p.cache.Add(j.key, &xcache.Entry{Keep: true, Expires: j.expires})
		}
		return
	}
	p.recordBackground("done")
	p.cache.Add(j.key, entryFor(res, len(j.in.Raw), p.cfg.MinGain, j.expires))
}

func (b *background) close() {
	b.cancel()
	b.wg.Wait()
}

func entryFor(res *img.Result, original int, minGain float64, expires time.Time) *xcache.Entry {
	if float64(len(res.Data)) > float64(original)*(1-minGain) {
		return &xcache.Entry{Keep: true, Expires: expires}
	}
	return &xcache.Entry{Data: res.Data, ContentType: res.Format.MIME(), Expires: expires, Metric: string(res.Metric), Measure: res.Measure}
}

// idle reports whether no job is waiting or running.
func (b *background) idle() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.inflight) == 0
}
