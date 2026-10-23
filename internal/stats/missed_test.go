package stats

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestByReasonSplitsKindsAndBytes(t *testing.T) {
	c, _ := fake()
	c.Record(Event{Kind: "image", Reason: "transformed", Transformed: true, BytesIn: 1000, BytesOut: 400})
	c.Record(Event{Kind: "image", Reason: "no-transform", BytesIn: 700, BytesOut: 700})
	c.Record(Event{Kind: "image", Reason: "no-transform", BytesIn: 300, BytesOut: 300})
	c.Record(Event{Kind: "text", Reason: "no-transform", BytesIn: 50, BytesOut: 50})
	c.Record(Event{Kind: "media", Reason: "type", Cause: "application/vnd.yt-ump", BytesIn: 5000, BytesOut: 5000})

	s := c.Snapshot(5)
	assert.Equal(t, []ReasonStat{
		{Reason: "type", Kind: "media", Requests: 1, BytesIn: 5000, BytesOut: 5000},
		{Reason: "no-transform", Kind: "image", Requests: 2, BytesIn: 1000, BytesOut: 1000},
		{Reason: "transformed", Kind: "image", Requests: 1, BytesIn: 1000, BytesOut: 400},
		{Reason: "no-transform", Kind: "text", Requests: 1, BytesIn: 50, BytesOut: 50},
	}, s.ByReason)
	assert.Equal(t, []CauseStat{{Reason: "type", Cause: "application/vnd.yt-ump", Requests: 1, BytesIn: 5000}}, s.ByCause)
	assert.Equal(t, Bucket{Requests: 1, BytesIn: 5000, BytesOut: 5000}, s.ByKind["media"])
}

func TestCausesKeepReasonAndCauseApart(t *testing.T) {
	c, _ := fake()
	for _, cause := range []string{"memory-body", "memory-pixels", "cpu", "cpu"} {
		c.Record(Event{Kind: "image", Reason: "busy", Cause: cause, BytesIn: 10, BytesOut: 10})
	}
	c.Record(Event{Kind: "other", Reason: "status", Cause: "206", BytesIn: 99, BytesOut: 99})
	got := map[string]CauseStat{}
	for _, cs := range c.Snapshot(1).ByCause {
		got[cs.Reason+"/"+cs.Cause] = cs
	}
	assert.Equal(t, uint64(2), got["busy/cpu"].Requests)
	assert.Equal(t, uint64(20), got["busy/cpu"].BytesIn)
	assert.Equal(t, uint64(1), got["busy/memory-body"].Requests)
	assert.Equal(t, uint64(99), got["status/206"].BytesIn)
}

func TestNoProfileIsADash(t *testing.T) {
	c, _ := fake()
	c.Record(Event{Kind: "other", Reason: "type", BytesIn: 1, BytesOut: 1})
	_, ok := c.Snapshot(1).ByProfile["-"]
	assert.True(t, ok)
	_, ok = c.Snapshot(1).ByProfile["unknown"]
	assert.False(t, ok)
}

func TestTopMissedRanksBytesLeftAlone(t *testing.T) {
	c, _ := fake()
	c.SetDomains(true)
	// a CDN whose images are never touched, a site that is fully shrunk and a
	// video host that only counts as media
	for range 3 {
		c.Record(Event{Host: "img.cdn.test", Kind: "image", Reason: "no-transform", BytesIn: 400, BytesOut: 400})
	}
	c.Record(Event{Host: "img.cdn.test", Kind: "image", Reason: "transformed", Transformed: true, BytesIn: 900, BytesOut: 300})
	c.Record(Event{Host: "shrunk.test", Kind: "text", Reason: "transformed", Transformed: true, BytesIn: 5000, BytesOut: 900})
	c.Record(Event{Host: "video.test", Kind: "media", Reason: "type", BytesIn: 100000, BytesOut: 100000})
	c.Record(Event{Host: "small.test", Kind: "text", Reason: "busy", Cause: "memory-text", BytesIn: 100, BytesOut: 100})

	s := c.Snapshot(5)
	require.NotNil(t, s.TopMissed)
	m := *s.TopMissed
	require.Len(t, m, 3, "shrunk.test left nothing alone")
	assert.Equal(t, "img.cdn.test", m[0].Domain)
	assert.Equal(t, uint64(1200), m[0].Missed)
	assert.Equal(t, uint64(4), m[0].Requests)
	assert.Equal(t, []HostReason{{Reason: "no-transform", Kind: "image", Requests: 3, BytesIn: 1200}}, m[0].Reasons)
	assert.Equal(t, "small.test", m[1].Domain)
	assert.Equal(t, "video.test", m[2].Domain)
	assert.Equal(t, uint64(100000), m[2].Media)
	assert.Zero(t, m[2].Missed)
}

func TestMissedByReasonListsHosts(t *testing.T) {
	c, _ := fake()
	assert.Empty(t, c.MissedByReason("busy", 10), "off while domains are not tracked")
	c.SetDomains(true)
	c.Record(Event{Host: "a.test", Kind: "image", Reason: "busy", Cause: "cpu", BytesIn: 100, BytesOut: 100})
	c.Record(Event{Host: "a.test", Kind: "text", Reason: "busy", Cause: "memory-text", BytesIn: 50, BytesOut: 50})
	c.Record(Event{Host: "b.test", Kind: "image", Reason: "busy", Cause: "cpu", BytesIn: 500, BytesOut: 500})
	c.Record(Event{Host: "b.test", Kind: "image", Reason: "no-transform", BytesIn: 9000, BytesOut: 9000})

	got := c.MissedByReason("busy", 10)
	require.Len(t, got, 2)
	assert.Equal(t, "b.test", got[0].Domain)
	assert.Equal(t, uint64(500), got[0].BytesIn)
	assert.Equal(t, "a.test", got[1].Domain)
	assert.Equal(t, uint64(2), got[1].Requests)
	assert.Equal(t, []HostReason{
		{Reason: "busy", Kind: "image", Requests: 1, BytesIn: 100},
		{Reason: "busy", Kind: "text", Requests: 1, BytesIn: 50},
	}, got[1].Kinds)
	assert.Len(t, c.MissedByReason("busy", 1), 1)
	assert.Empty(t, c.MissedByReason("no-such-reason", 10))
}

func TestMissedSwitchOffDropsEverything(t *testing.T) {
	c, _ := fake()
	c.SetDomains(true)
	c.Record(Event{Host: "a.test", Kind: "image", Reason: "type", BytesIn: 1, BytesOut: 1})
	c.SetDomains(false)
	assert.Nil(t, c.Snapshot(5).TopMissed)
	c.SetDomains(true)
	assert.Empty(t, *c.Snapshot(5).TopMissed)
}

func TestMissedStaysBoundedAndKeepsHeavyHosts(t *testing.T) {
	c := New(Options{MaxDomains: 8})
	c.SetDomains(true)
	for i := range 5000 {
		c.Record(Event{Host: fmt.Sprintf("noise%d.test", i), Kind: "image", Reason: "type", BytesIn: 1, BytesOut: 1})
		if i%10 == 0 {
			c.Record(Event{Host: "heavy.test", Kind: "image", Reason: "no-transform", BytesIn: 1000, BytesOut: 1000})
		}
	}
	top := *c.Snapshot(3).TopMissed
	require.NotEmpty(t, top)
	assert.Equal(t, "heavy.test", top[0].Domain)
	assert.LessOrEqual(t, c.st.Load().missed.count(), 8)
}

func TestMissedConcurrent(t *testing.T) {
	c, _ := fake()
	c.SetDomains(true)
	var wg sync.WaitGroup
	for w := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 1000 {
				c.Record(Event{Host: fmt.Sprintf("h%d.test", (w+i)%7), Kind: "image", Reason: "no-transform", Cause: "response", BytesIn: 10, BytesOut: 10})
				if i%100 == 0 {
					_ = c.Snapshot(5)
					_ = c.MissedByReason("no-transform", 5)
				}
			}
		}()
	}
	wg.Wait()
	var total uint64
	for _, h := range c.MissedByReason("no-transform", 100) {
		total += h.Requests
	}
	assert.Equal(t, uint64(16000), total)
}
