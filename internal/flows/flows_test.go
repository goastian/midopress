package flows

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/rules"
)

func flow(host, path, ct, kind string) Flow {
	return Flow{
		Method: "GET", Scheme: "https", Host: host, Path: path, Status: 200, Kind: kind, ContentType: ct,
		Client: ClientRef{Addr: "10.0.0.5:5555"}, BytesIn: 1000, BytesOut: 400, Decision: "transformed", Reason: "transformed",
		RequestHeaders: map[string][]string{"Accept": {"image/webp"}}, ResponseHeader: map[string][]string{"Content-Type": {ct}},
	}
}

func on(t *testing.T, capacity int, budget int64) *Store {
	t.Helper()
	s := NewStore(capacity, budget)
	s.SetEnabled(true)
	return s
}

func TestOffByDefaultAndForgetsWhenTurnedOff(t *testing.T) {
	s := NewStore(10, 1<<20)
	_, ok := s.Add(flow("a.test", "/", "text/html", "text"), nil, nil)
	assert.False(t, ok, "nothing is kept while the list is off")
	s.SetEnabled(true)
	_, ok = s.Add(flow("a.test", "/", "text/html", "text"), nil, nil)
	require.True(t, ok)
	list, total := s.List(ListOptions{})
	assert.Len(t, list, 1)
	assert.Equal(t, 1, total)
	s.SetEnabled(false)
	list, total = s.List(ListOptions{})
	assert.Empty(t, list)
	assert.Zero(t, total)
}

func TestRingKeepsTheNewestAndIdsAreUnique(t *testing.T) {
	s := on(t, 5, 0)
	var ids []string
	for i := range 12 {
		f, _ := s.Add(flow("a.test", fmt.Sprint("/", i), "text/html", "text"), nil, nil)
		ids = append(ids, f.ID)
	}
	assert.Len(t, map[string]bool{ids[0]: true, ids[1]: true, ids[11]: true}, 3)
	list, total := s.List(ListOptions{})
	require.Len(t, list, 5)
	assert.Equal(t, 5, total)
	assert.Equal(t, "/11", list[0].Path, "newest first")
	assert.Equal(t, "/7", list[4].Path)
	_, ok := s.Get(ids[0])
	assert.False(t, ok, "the oldest went")
	got, ok := s.Get(ids[11])
	require.True(t, ok)
	assert.Equal(t, "/11", got.Path)
	limited, _ := s.List(ListOptions{Limit: 2})
	assert.Len(t, limited, 2)
}

func TestFiltering(t *testing.T) {
	s := on(t, 50, 0)
	s.Add(flow("img.example.com", "/a.jpg", "image/jpeg", "image"), nil, nil)
	s.Add(flow("www.example.com", "/", "text/html", "text"), nil, nil)
	s.Add(flow("other.test", "/b.png", "image/png", "image"), nil, nil)

	parse := func(expr string) *rules.Expr {
		e, err := rules.Parse(expr)
		require.NoError(t, err)
		return e
	}
	images, _ := s.List(ListOptions{Kind: "image"})
	assert.Len(t, images, 2)
	byRule, _ := s.List(ListOptions{Filter: parse(`~d example\.com & ~t image/`)})
	require.Len(t, byRule, 1)
	assert.Equal(t, "img.example.com", byRule[0].Host)
	byURL, _ := s.List(ListOptions{Filter: parse(`~u \.png$ | ~t html`)})
	assert.Len(t, byURL, 2)
	both, _ := s.List(ListOptions{Kind: "image", Filter: parse(`!~d other`)})
	assert.Len(t, both, 1)
	bySrc, _ := s.List(ListOptions{Filter: parse(`~src 10.0.0.0/8`)})
	assert.Len(t, bySrc, 3, "the address of a client carries its port")
	byHeader, _ := s.List(ListOptions{Filter: parse(`~hq Accept:.*webp`)})
	assert.Len(t, byHeader, 3)
}

func TestBodiesAreKeptWithinTheBudgetAndOldestGoFirst(t *testing.T) {
	s := on(t, 100, 4000)
	body := func(n int) *Body { return &Body{Data: make([]byte, n), ContentType: "image/png"} }
	f1, _ := s.Add(flow("a.test", "/1", "image/png", "image"), body(500), body(300))
	f2, _ := s.Add(flow("a.test", "/2", "image/png", "image"), body(500), body(300))
	assert.True(t, f1.HasOriginal && f1.HasResult)
	assert.Equal(t, int64(1600), s.HeldBytes())

	for i := 3; i <= 6; i++ {
		s.Add(flow("a.test", fmt.Sprint("/", i), "image/png", "image"), body(500), body(300))
	}
	assert.LessOrEqual(t, s.HeldBytes(), int64(4000))
	_, _, ok := s.Bodies(f1.ID)
	assert.False(t, ok, "the oldest bodies make room")
	got, _ := s.Get(f1.ID)
	assert.False(t, got.HasOriginal, "the flow itself stays, without its bodies")
	_, _, ok = s.Bodies(f2.ID)
	assert.True(t, ok, "only as many as needed go")
	last, _ := s.List(ListOptions{Limit: 1})
	o, r, ok := s.Bodies(last[0].ID)
	require.True(t, ok)
	assert.Len(t, o.Data, 500)
	assert.Len(t, r.Data, 300)

	huge, _ := s.Add(flow("a.test", "/huge", "image/png", "image"), body(3000), body(100))
	assert.False(t, huge.HasOriginal, "bigger than a quarter of the budget is not kept")

	none := on(t, 10, 0)
	f, _ := none.Add(flow("a.test", "/", "image/png", "image"), body(10), body(10))
	assert.False(t, f.HasOriginal)
	assert.False(t, none.CaptureEnabled())
	assert.True(t, s.CaptureEnabled())
}

func TestEvictedFlowsFreeTheirBodies(t *testing.T) {
	s := on(t, 3, 1<<20)
	body := &Body{Data: make([]byte, 1000)}
	for i := range 3 {
		s.Add(flow("a.test", fmt.Sprint("/", i), "image/png", "image"), body, body)
	}
	assert.Equal(t, int64(6000), s.HeldBytes())
	s.Add(flow("a.test", "/new", "text/html", "text"), nil, nil)
	assert.Equal(t, int64(4000), s.HeldBytes())
	s.Clear()
	assert.Zero(t, s.HeldBytes())
}

func TestSubscribersGetNewFlowsAndSlowOnesDoNotBlock(t *testing.T) {
	s := on(t, 10, 0)
	ch, cancel := s.Subscribe()
	slow, cancelSlow := s.Subscribe()
	defer cancelSlow()
	_ = slow
	s.Add(flow("a.test", "/x", "text/html", "text"), nil, nil)
	select {
	case f := <-ch:
		assert.Equal(t, "/x", f.Path)
	case <-time.After(time.Second):
		t.Fatal("no flow")
	}
	start := time.Now()
	for range 1000 {
		s.Add(flow("a.test", "/y", "text/html", "text"), nil, nil)
	}
	assert.Less(t, time.Since(start), 2*time.Second, "a subscriber that does not read does not slow the proxy")
	cancel()
	s.Add(flow("a.test", "/z", "text/html", "text"), nil, nil)
}

func TestSafeHeadersHideSecrets(t *testing.T) {
	h := http.Header{}
	h.Add("Authorization", "Bearer abc")
	h.Add("Cookie", "a=b")
	h.Add("Set-Cookie", "x=1")
	h.Add("Set-Cookie", "y=2")
	h.Add("Accept", "image/avif")
	got := SafeHeaders(h)
	assert.Equal(t, []string{"<hidden>"}, got["Authorization"])
	assert.Equal(t, []string{"<hidden>", "<hidden>"}, got["Set-Cookie"])
	assert.Equal(t, []string{"image/avif"}, got["Accept"])
	h.Set("Accept", "changed")
	assert.Equal(t, []string{"image/avif"}, got["Accept"], "a copy")
}

func TestHARIsValidAndSayWhatWasSaved(t *testing.T) {
	s := on(t, 10, 0)
	f := flow("img.example.com", "/p/a.jpg?w=300&q=2", "image/jpeg", "image")
	f.OutContentType, f.Codec, f.Measure = "image/avif", "avif", 0.0017
	s.Add(f, nil, nil)
	s.Add(flow("www.example.com", "/", "text/html", "text"), nil, nil)
	raw, err := json.Marshal(ToHAR(s.All(), "0.3.0"))
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	log := doc["log"].(map[string]any)
	assert.Equal(t, "1.2", log["version"])
	assert.Equal(t, "MidoPress", log["creator"].(map[string]any)["name"])
	entries := log["entries"].([]any)
	require.Len(t, entries, 2)
	e := entries[0].(map[string]any)
	req := e["request"].(map[string]any)
	assert.Equal(t, "https://img.example.com/p/a.jpg?w=300&q=2", req["url"])
	assert.Len(t, req["queryString"], 2)
	resp := e["response"].(map[string]any)
	assert.Equal(t, "image/avif", resp["content"].(map[string]any)["mimeType"])
	assert.EqualValues(t, 400, resp["content"].(map[string]any)["size"])
	assert.EqualValues(t, 1000, e["_originalBytes"])
	assert.Equal(t, "avif", e["_codec"])
	for _, key := range []string{"startedDateTime", "time", "request", "response", "cache", "timings"} {
		assert.Contains(t, e, key, "required by HAR 1.2")
	}
	_, err = time.Parse(time.RFC3339Nano, e["startedDateTime"].(string))
	assert.NoError(t, err)
}

func TestConcurrentUse(t *testing.T) {
	s := on(t, 50, 100_000)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 300 {
				b := &Body{Data: make([]byte, 200)}
				f, _ := s.Add(flow("a.test", fmt.Sprint("/", g, "/", i), "image/png", "image"), b, b)
				s.Get(f.ID)
				s.Bodies(f.ID)
				s.List(ListOptions{Limit: 10})
				if i%100 == 99 {
					s.Clear()
				}
			}
		}()
	}
	wg.Wait()
	assert.LessOrEqual(t, s.HeldBytes(), int64(100_000))
}
