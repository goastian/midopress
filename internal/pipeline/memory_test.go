package pipeline

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/img"
)

// room is the budget that fits one picture of the tests and little more.
func room(jpgBytes, w, h int) int64 {
	return int64(jpgBytes)*2 + pixelWeight(int64(w*h), img.Caps{WebP: true}, balanced()) + 4096
}

func balanced() *img.Profile { p, _ := img.Lookup(img.ProfileBalanced); return p }

func TestAnImageWaitsForRoomInsteadOfGivingUp(t *testing.T) {
	jpg := jpegBytes(t, 160, 120, 92)
	budget := room(len(jpg), 160, 120)

	t.Run("room that frees up in time", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.MemoryBudget = budget; c.TranscodeWait = 3 * time.Second })
		ctx := context.Background()
		require.True(t, p.reserve(ctx, budget-1000), "something else holds nearly all of it")
		go func() {
			time.Sleep(150 * time.Millisecond)
			p.unreserve(budget - 1000)
		}()
		start := time.Now()
		rec, out := run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", jpg))
		assert.Equal(t, Transformed, out.Reason, "it waited and got the room")
		assert.GreaterOrEqual(t, time.Since(start), 100*time.Millisecond)
		assert.Less(t, rec.Body.Len(), len(jpg))
		assert.Zero(t, p.memUsed.Load(), "everything was given back")
	})

	t.Run("room that does not free up", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.MemoryBudget = budget; c.TranscodeWait = 80 * time.Millisecond })
		require.True(t, p.reserve(context.Background(), budget-1000))
		start := time.Now()
		rec, out := run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", jpg))
		assert.Equal(t, PassBusy, out.Reason)
		assert.Contains(t, []string{CauseMemoryBody, CauseMemoryPixels}, out.Cause)
		assert.Less(t, time.Since(start), 2*time.Second, "the wait is bounded")
		assert.Equal(t, jpg, rec.Body.Bytes(), "the original goes")
		p.unreserve(budget - 1000)
		assert.Zero(t, p.memUsed.Load())
	})

	t.Run("more than the whole budget is not waited for", func(t *testing.T) {
		p := newPipeline(func(c *Config) { c.MemoryBudget = 1000; c.TranscodeWait = 10 * time.Second })
		start := time.Now()
		_, out := run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", jpg))
		assert.Equal(t, PassBusy, out.Reason)
		assert.Less(t, time.Since(start), time.Second)
	})
}

func TestADoneImageNeedsNoMemoryForItsPixels(t *testing.T) {
	jpg := jpegBytes(t, 160, 120, 92)
	budget := room(len(jpg), 160, 120)
	p := newPipeline(func(c *Config) { c.MemoryBudget = budget; c.TranscodeWait = 20 * time.Millisecond })
	_, first := run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", jpg))
	require.Equal(t, Transformed, first.Reason)

	// what is left is enough for the bytes of the body and not for a picture
	require.True(t, p.reserve(context.Background(), budget-int64(len(jpg))*2-1000))
	rec, again := run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", jpg))
	assert.Equal(t, Transformed, again.Reason)
	assert.True(t, again.CacheHit)
	assert.Less(t, rec.Body.Len(), len(jpg))

	// a picture that is not done does need it
	other := jpegBytes(t, 161, 120, 91)
	_, busy := run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", other))
	assert.Equal(t, PassBusy, busy.Reason)
	assert.Equal(t, CauseMemoryPixels, busy.Cause)
}

func TestABodyOfUnknownLengthIsOnlyChargedForWhatArrived(t *testing.T) {
	jpg := jpegBytes(t, 160, 120, 92)
	// the old way asked for twice the largest image there could be (8 MiB)
	budget := room(len(jpg), 160, 120) + 2*bodyStep
	p := newPipeline(func(c *Config) { c.MemoryBudget = budget })
	rec, out := run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", jpg, withUnknownLength()))
	assert.Equal(t, Transformed, out.Reason)
	assert.Less(t, rec.Body.Len(), len(jpg))
	assert.Zero(t, p.memUsed.Load())

	t.Run("and when the memory runs out in the middle, the whole body still reaches the client", func(t *testing.T) {
		// the body is read before anything looks at it, so it does not have to be a picture
		big := pseudoRandom(900 << 10)
		require.Greater(t, len(big), 3*bodyStep, "the test needs a body that takes several steps")
		p := newPipeline(func(c *Config) { c.MemoryBudget = 2*bodyStep + 2*bodyStep/2; c.TranscodeWait = 20 * time.Millisecond })
		rec, out := run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", big, withUnknownLength()))
		assert.Equal(t, PassBusy, out.Reason)
		assert.Equal(t, CauseMemoryBody, out.Cause)
		assert.Equal(t, big, rec.Body.Bytes(), "what was read and what was not")
		assert.Zero(t, p.memUsed.Load())
	})
}

func TestNoCPUSlotLeavesTheResultForTheNextRequest(t *testing.T) {
	jpg := jpegBytes(t, 200, 150, 92)
	p := newPipeline(func(c *Config) { c.MaxConcurrent = 1; c.TranscodeWait = 5 * time.Millisecond })
	require.True(t, p.acquireCPU())
	rec, out := run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", jpg))
	require.Equal(t, PassBusy, out.Reason)
	require.Equal(t, CauseCPU, out.Cause)
	assert.Equal(t, jpg, rec.Body.Bytes())
	p.releaseCPU()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	require.True(t, p.WaitBackground(ctx))
	rec, out = run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", jpg))
	assert.Equal(t, Transformed, out.Reason, "it was done in the background")
	assert.True(t, out.CacheHit)
	assert.Less(t, rec.Body.Len(), len(jpg))
}

func TestABurstOfImagesIsServedWithinTheBudget(t *testing.T) {
	const n, w, h = 12, 200, 150
	jpgs := make([][]byte, n)
	for i := range jpgs {
		jpgs[i] = jpegBytes(t, w+i, h, 90)
	}
	// room for three pictures at a time, twelve asking
	per := room(len(jpgs[0]), w, h)
	p := newPipeline(func(c *Config) { c.MemoryBudget = 3 * per; c.TranscodeWait = time.Minute; c.MaxConcurrent = 12 })

	var wg sync.WaitGroup
	reasons := make([]Reason, n)
	var peak int64
	var mu sync.Mutex
	stop, sampled := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(sampled)
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
				mu.Lock()
				peak = max(peak, p.memUsed.Load())
				mu.Unlock()
			}
		}
	}()
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, out := run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", jpgs[i]))
			reasons[i] = out.Reason
		}()
	}
	wg.Wait()
	close(stop)
	<-sampled
	for i, r := range reasons {
		assert.Equal(t, Transformed, r, "image %d", i)
	}
	assert.LessOrEqual(t, peak, 3*per+int64(n)*8192, "never more than the budget")
	assert.Zero(t, p.memUsed.Load())
}

func pseudoRandom(n int) []byte {
	b := make([]byte, n)
	x := uint64(88172645463325252)
	for i := range b {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		b[i] = byte(x)
	}
	return b
}

func TestAnImageThatFoundNoRoomIsDoneInTheBackground(t *testing.T) {
	jpg := jpegBytes(t, 160, 120, 92)
	budget := room(len(jpg), 160, 120)
	p := newPipeline(func(c *Config) { c.MemoryBudget = budget; c.TranscodeWait = 20 * time.Millisecond })

	// something else holds the room for the picture
	held := budget - int64(len(jpg))*2 - 1000
	require.True(t, p.reserve(context.Background(), held))
	rec, out := run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", jpg))
	require.Equal(t, PassBusy, out.Reason)
	require.Equal(t, CauseMemoryPixels, out.Cause)
	assert.Equal(t, jpg, rec.Body.Bytes(), "the original goes now")
	assert.Positive(t, p.bg.queued.Load(), "and the job waits for the room, holding the picture")

	p.unreserve(held)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	require.True(t, p.WaitBackground(ctx))
	assert.Zero(t, p.bg.queued.Load(), "what a job held while it waited is given back")
	assert.Zero(t, p.memUsed.Load(), "and so is its memory")

	rec, out = run(p, newReq("Accept", "image/webp"), newResp("image/jpeg", jpg))
	assert.Equal(t, Transformed, out.Reason, "the next request finds it")
	assert.True(t, out.CacheHit)
	assert.Less(t, rec.Body.Len(), len(jpg))
}

func TestWhatWaitsInTheBackgroundNeverHoldsBackAResponse(t *testing.T) {
	jpg := jpegBytes(t, 160, 120, 92)
	budget := room(len(jpg), 160, 120)
	p := newPipeline(func(c *Config) { c.MemoryBudget = budget; c.TranscodeWait = 50 * time.Millisecond })
	held := budget - 100
	require.True(t, p.reserve(context.Background(), held))
	p.bg.submit("waiting", img.Input{Raw: jpg}, time.Now().Add(time.Hour), budget/2)
	require.Eventually(t, func() bool { return len(p.bg.queue) == 0 }, 2*time.Second, time.Millisecond, "a worker took the job")
	time.Sleep(100 * time.Millisecond) // and is now waiting for room

	// the job is waiting for room that is not there; a request that needs a
	// little must not queue behind it
	got := make(chan bool, 1)
	go func() { got <- p.reserve(context.Background(), 50) }()
	select {
	case ok := <-got:
		assert.True(t, ok, "the small request is served at once, the job waits")
	case <-time.After(2 * time.Second):
		t.Fatal("a response was held back by a job that waits in the background")
	}
	p.unreserve(50)
	p.unreserve(held)
}

func TestTheBackgroundQueueHoldsAtMostAQuarterOfTheBudget(t *testing.T) {
	jpg := jpegBytes(t, 160, 120, 92)
	p := newPipeline(func(c *Config) { c.MemoryBudget = int64(len(jpg))*4 + 100 })
	require.True(t, p.reserve(context.Background(), p.cfg.MemoryBudget), "nothing can run, so the jobs stay queued")
	for i := range 6 {
		p.bg.submit(fmt.Sprintf("k%d", i), img.Input{Raw: jpg}, time.Now().Add(time.Hour), 1)
	}
	assert.LessOrEqual(t, p.bg.queued.Load(), p.cfg.MemoryBudget/4)
	assert.Equal(t, int64(len(jpg)), p.bg.queued.Load(), "one fit, the others were left out")

	p.bg.submit("never", img.Input{Raw: jpg}, time.Now().Add(time.Hour), p.cfg.MemoryBudget+1)
	_, queued := p.bg.inflight["never"]
	assert.False(t, queued, "a job that needs more than the whole budget would never run")
	p.unreserve(p.cfg.MemoryBudget)
}
