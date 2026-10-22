package pipeline

import (
	"context"
	"errors"
	"runtime"
	"sync"
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
}

type bgJob struct {
	key     string
	in      img.Input
	expires time.Time
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
func (b *background) submit(key string, in img.Input, expires time.Time) {
	if !b.enabled() || b.ctx.Err() != nil {
		return
	}
	b.once.Do(func() {
		workers := max(1, runtime.GOMAXPROCS(0)/4)
		for range workers {
			b.wg.Add(1)
			go b.work()
		}
	})
	b.mu.Lock()
	if _, ok := b.inflight[key]; ok {
		b.mu.Unlock()
		return
	}
	b.inflight[key] = struct{}{}
	b.mu.Unlock()
	select {
	case b.queue <- bgJob{key, in, expires}:
	default:
		b.done(key)
	}
}

func (b *background) done(key string) {
	b.mu.Lock()
	delete(b.inflight, key)
	b.mu.Unlock()
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
	defer b.done(j.key)
	defer func() { _ = recover() }()
	p := b.p
	select {
	case p.sem <- struct{}{}:
	case <-b.ctx.Done():
		return
	}
	defer p.releaseCPU()
	ctx, cancel := context.WithTimeout(b.ctx, p.cfg.Image.BackgroundTimeout)
	defer cancel()
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
	return &xcache.Entry{Data: res.Data, ContentType: res.Format.MIME(), Expires: expires}
}

// idle reports whether no job is waiting or running.
func (b *background) idle() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.inflight) == 0
}
