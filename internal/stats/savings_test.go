package stats

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFailedResponsesKeepTheirBytesWithoutCountingThemAsSavings(t *testing.T) {
	c, _ := fake()
	c.SetDomains(true)
	events := []Event{
		{Kind: "image", Reason: "transformed", Transformed: true, BytesIn: 1000, BytesOut: 400},
		{Kind: "image", Reason: "error", BytesIn: 500, Failed: true},
		{Kind: "image", Reason: "transformed", Transformed: true, BytesIn: 800, BytesOut: 50, Failed: true},
		{Kind: "text", Reason: "type", BytesIn: 200, BytesOut: 100, Failed: true},
		{Kind: "text", Reason: "type", BytesIn: 100, BytesOut: 150},
	}
	for _, e := range events {
		e.Host, e.ClientKey, e.Device, e.Codec, e.Profile = "site.test", "client", "device", "br", "balanced"
		c.Record(e)
	}
	want := Bucket{Requests: 5, Transformed: 2, BytesIn: 2600, BytesOut: 700, Saved: 550}
	s := c.Snapshot(5)
	assert.Equal(t, want, s.Totals.Bucket)
	assert.InDelta(t, 550.0/2600*100, s.Totals.SavedPercent, 1e-9)
	assert.Equal(t, want, s.ByCodec["br"])
	assert.Equal(t, want, s.ByProfile["balanced"])
	assert.Equal(t, int64(600), s.ByKind["image"].Saved)
	assert.Equal(t, int64(-50), s.ByKind["text"].Saved)
	for _, r := range s.ByReason {
		if r.Reason == "transformed" {
			assert.Equal(t, int64(600), r.Saved)
		}
		if r.Reason == "error" {
			assert.Zero(t, r.Saved)
			assert.Equal(t, uint64(500), r.BytesIn)
			assert.Zero(t, r.BytesOut)
		}
	}
	for _, points := range [][]SeriesPoint{s.Series.Seconds, s.Series.Minutes} {
		p := points[len(points)-1]
		assert.Equal(t, want.Requests, p.Requests)
		assert.Equal(t, want.BytesIn, p.BytesIn)
		assert.Equal(t, want.BytesOut, p.BytesOut)
		assert.Equal(t, want.Saved, p.Saved)
	}
	require.NotNil(t, s.TopDomains)
	require.Len(t, *s.TopDomains, 1)
	assert.Equal(t, want, (*s.TopDomains)[0].Bucket)
	cl, ok := c.Client("client")
	require.True(t, ok)
	assert.Equal(t, want.BytesIn, cl.BytesIn)
	assert.Equal(t, want.BytesOut, cl.BytesOut)
	assert.Equal(t, want.Saved, cl.Saved)
	assert.Equal(t, int64(600), cl.ByKind["image"].Saved)
	dev := c.Devices()["device"]
	assert.Equal(t, want.BytesIn, dev.BytesIn)
	assert.Equal(t, want.BytesOut, dev.BytesOut)
	assert.Equal(t, want.Saved, dev.Saved)
	assert.Len(t, c.Clients(time.Minute), 1)
}

func TestDomainRankingDoesNotRewardFailedTransfers(t *testing.T) {
	c, _ := fake()
	c.SetDomains(true)
	c.Record(Event{Host: "failed.test", BytesIn: 1 << 20, BytesOut: 10, Failed: true})
	c.Record(Event{Host: "saved.test", BytesIn: 1000, BytesOut: 900})
	top := *c.Snapshot(2).TopDomains
	require.Len(t, top, 2)
	assert.Equal(t, "saved.test", top[0].Domain)
	assert.Equal(t, int64(100), top[0].Saved)
	assert.Equal(t, "failed.test", top[1].Domain)
	assert.Zero(t, top[1].Saved)
	assert.Equal(t, uint64(1<<20), top[1].BytesIn)
	assert.Equal(t, uint64(10), top[1].BytesOut)
}
