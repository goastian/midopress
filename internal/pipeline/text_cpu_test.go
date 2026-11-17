package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/goastian/midopress/internal/codec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInlineStaticBrotliDoesNotBypassOrWaitForAnOccupiedCPUSlot(t *testing.T) {
	plain := jsLike(32 << 10)
	origin := brotliAt(t, 5, plain)
	want := brotliAt(t, 10, plain)
	require.Less(t, len(want), len(origin))
	p := newPipeline(func(c *Config) {
		c.MaxConcurrent = 1
		c.TranscodeWait = 5 * time.Second
	})
	defer p.Close()
	held := true
	p.sem <- struct{}{}
	defer func() {
		if held {
			p.releaseCPU()
		}
	}()
	serve := func() (*writerRec, Outcome) {
		rec, out := run(p, newReq("Accept-Encoding", "br"), newResp("text/plain", origin,
			withHeader("Content-Encoding", "br"), withHeader("Cache-Control", static)))
		return &writerRec{body: rec.Body.Bytes(), enc: rec.Header().Get("Content-Encoding")}, out
	}
	start := time.Now()
	first, out := serve()
	require.Equal(t, PassBusy, out.Reason)
	assert.Equal(t, CauseCPU, out.Cause)
	assert.Less(t, time.Since(start), time.Second, "saturated static compression must not use the configured slot wait")
	assert.Equal(t, origin, first.body)
	assert.Equal(t, "br", first.enc)
	_, busy, _ := p.CPU()
	assert.Equal(t, 1, busy)
	p.releaseCPU()
	held = false
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, p.WaitBackground(ctx))
	p.sem <- struct{}{}
	held = true
	start = time.Now()
	again, out := serve()
	require.Equal(t, Transformed, out.Reason, "the completed conversion is usable even while the CPU is occupied")
	assert.Less(t, time.Since(start), time.Second)
	assert.Equal(t, want, again.body)
	assert.Equal(t, plain, decode(t, codec.Brotli, again.body))
}
