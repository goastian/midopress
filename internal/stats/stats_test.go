package stats

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type clock struct{ ns atomic.Int64 }

func newClock(t time.Time) *clock {
	c := &clock{}
	c.ns.Store(t.UnixNano())
	return c
}

func (c *clock) Now() time.Time          { return time.Unix(0, c.ns.Load()) }
func (c *clock) Advance(d time.Duration) { c.ns.Add(int64(d)) }

var epoch = time.Unix(1_800_000_000, 0)

func fake() (*Collector, *clock) {
	ck := newClock(epoch)
	return New(Options{Now: ck.Now}), ck
}

func TestConcurrentExactness(t *testing.T) {
	c, _ := fake()
	c.SetDomains(true)
	kinds := []string{"image", "text", "font", "other", "weird"}
	codecs := []string{"avif", "webp", "br", "", "identity"}
	reasons := []string{"transformed", "type", "", "not-smaller"}

	const workers, per = 32, 2000
	events := make([][]Event, workers)
	for w := range events {
		r := rand.New(rand.NewPCG(uint64(w), 1))
		for range per {
			in := r.Int64N(100000)
			events[w] = append(events[w], Event{
				Kind: kinds[r.IntN(len(kinds))], Codec: codecs[r.IntN(len(codecs))],
				Profile: fmt.Sprintf("p%d", r.IntN(3)), Reason: reasons[r.IntN(len(reasons))],
				Transformed: r.IntN(2) == 0, BytesIn: in, BytesOut: in / 2,
				Host: fmt.Sprintf("h%d.test", r.IntN(10)), ClientKey: fmt.Sprintf("c%d", r.IntN(5)),
			})
		}
	}

	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for _, e := range events[w] {
				c.Record(e)
			}
		})
	}
	wg.Wait()

	wantKind := map[string]Bucket{}
	wantCodec := map[string]Bucket{}
	wantProf := map[string]Bucket{}
	wantDec := map[string]uint64{}
	wantDom := map[string]Bucket{}
	var total Bucket
	for _, evs := range events {
		for _, e := range evs {
			var tr uint64
			if e.Transformed {
				tr = 1
			}
			b := Bucket{1, tr, uint64(e.BytesIn), uint64(e.BytesOut)}
			k := e.Kind
			if k != "image" && k != "text" && k != "font" {
				k = "other"
			}
			wantKind[k] = wantKind[k].plus(b)
			cd := e.Codec
			if cd == "" {
				cd = "identity"
			}
			wantCodec[cd] = wantCodec[cd].plus(b)
			wantProf[e.Profile] = wantProf[e.Profile].plus(b)
			r := e.Reason
			if r == "" {
				r = "unknown"
			}
			wantDec[r]++
			wantDom[e.Host] = wantDom[e.Host].plus(b)
			total = total.plus(b)
		}
	}
	s := c.Snapshot(100)
	assert.Equal(t, newTotals(total), s.Totals)
	assert.Equal(t, wantKind, s.ByKind)
	assert.Equal(t, wantCodec, s.ByCodec)
	assert.Equal(t, wantProf, s.ByProfile)
	assert.Equal(t, wantDec, s.ByDecision)
	require.NotNil(t, s.TopDomains)
	require.Len(t, *s.TopDomains, 10)
	for _, d := range *s.TopDomains {
		assert.Equal(t, wantDom[d.Domain], d.Bucket, d.Domain)
	}
	assert.Equal(t, int64(total.BytesIn-total.BytesOut), s.Totals.Saved)
	assert.InDelta(t, 50.0, s.Totals.SavedPercent, 0.01)

	var n uint64
	for _, cl := range c.Clients(time.Minute) {
		n += cl.Requests
	}
	assert.Equal(t, uint64(workers*per), n)
	var series uint64
	for _, p := range s.Series.Seconds {
		series += p.Requests
	}
	assert.Equal(t, uint64(workers*per), series)
}

func TestLabelFolding(t *testing.T) {
	c, _ := fake()
	for i := range 200 {
		c.Record(Event{Codec: fmt.Sprintf("c%d", i), Profile: fmt.Sprintf("p%d", i), Reason: fmt.Sprintf("r%d", i)})
	}
	s := c.Snapshot(0)
	assert.LessOrEqual(t, len(s.ByCodec), maxLabels+1)
	assert.LessOrEqual(t, len(s.ByProfile), maxLabels+1)
	assert.LessOrEqual(t, len(s.ByDecision), maxLabels+1)
	assert.Equal(t, uint64(200-maxLabels), s.ByCodec["other"].Requests)
	assert.Nil(t, s.TopDomains)
}

func TestSavedPercentAndNegative(t *testing.T) {
	c, _ := fake()
	assert.Zero(t, c.Snapshot(0).Totals.SavedPercent)
	c.Record(Event{BytesIn: 100, BytesOut: 150})
	s := c.Snapshot(0)
	assert.Equal(t, int64(-50), s.Totals.Saved)
	assert.InDelta(t, -50.0, s.Totals.SavedPercent, 1e-9)
	assert.Zero(t, s.Totals.Transformed)
}

func TestSeriesGapsAndWrap(t *testing.T) {
	c, ck := fake()
	check := func() Series {
		s := c.Snapshot(0).Series
		require.Len(t, s.Seconds, 120)
		require.Len(t, s.Minutes, 180)
		now := ck.Now().Unix()
		assert.Equal(t, now, s.Seconds[119].T)
		assert.Equal(t, now-119, s.Seconds[0].T)
		assert.Equal(t, now/60*60, s.Minutes[179].T)
		for i := 1; i < 120; i++ {
			assert.Equal(t, s.Seconds[i-1].T+1, s.Seconds[i].T)
		}
		for i := 1; i < 180; i++ {
			assert.Equal(t, s.Minutes[i-1].T+60, s.Minutes[i].T)
		}
		return s
	}
	c.Record(Event{BytesIn: 10, BytesOut: 5})
	c.Record(Event{BytesIn: 10, BytesOut: 5})
	s := check()
	assert.Equal(t, uint64(2), s.Seconds[119].Requests)
	assert.Equal(t, uint64(20), s.Seconds[119].BytesIn)

	ck.Advance(5 * time.Second)
	c.Record(Event{BytesIn: 7, BytesOut: 1})
	s = check()
	assert.Equal(t, uint64(2), s.Seconds[114].Requests)
	assert.Equal(t, uint64(0), s.Seconds[116].Requests)
	assert.Equal(t, uint64(1), s.Seconds[119].Requests)

	ck.Advance(119 * time.Second)
	s = check()
	assert.Equal(t, uint64(1), s.Seconds[0].Requests, "the second record is the oldest point")
	ck.Advance(time.Second)
	s = check()
	var sum uint64
	for _, p := range s.Seconds {
		sum += p.Requests
	}
	assert.Zero(t, sum)

	ck.Advance(180 * time.Minute)
	s = check()
	sum = 0
	for _, p := range s.Minutes {
		sum += p.Requests
	}
	assert.Zero(t, sum, "everything is older than 180 minutes")

	c.Record(Event{BytesIn: 3})
	ck.Advance(61 * time.Second)
	c.Record(Event{BytesIn: 4})
	s = check()
	assert.Equal(t, uint64(2), s.Minutes[178].Requests+s.Minutes[179].Requests)
	assert.Equal(t, uint64(1), s.Minutes[178].Requests)
	assert.Equal(t, uint64(3), s.Minutes[178].BytesIn)
}

func TestSeriesOldEventIgnored(t *testing.T) {
	c, ck := fake()
	c.Record(Event{Time: ck.Now()})
	c.Record(Event{Time: ck.Now().Add(-10 * time.Minute)})
	s := c.Snapshot(0).Series
	var n uint64
	for _, p := range s.Seconds {
		n += p.Requests
	}
	assert.Equal(t, uint64(1), n)
	assert.Equal(t, uint64(2), s.Minutes[169].Requests+s.Minutes[179].Requests)
}

func TestLatencyQuantiles(t *testing.T) {
	c, ck := fake()
	r := rand.New(rand.NewPCG(7, 7))
	var samples []float64
	for range 100000 {
		d := time.Duration(math.Exp(r.NormFloat64()*1.2 + math.Log(20e6)))
		d = min(max(d, 100*time.Microsecond), 30*time.Second)
		samples = append(samples, float64(d)/1e6)
		c.Record(Event{Duration: d})
	}
	sort.Float64s(samples)
	l := c.Snapshot(0).Latency
	assert.Equal(t, uint64(len(samples)), l.Samples)
	for q, got := range map[float64]float64{0.5: l.P50Ms, 0.95: l.P95Ms, 0.99: l.P99Ms} {
		want := samples[int(q*float64(len(samples)))-1]
		assert.InDelta(t, want, got, want*0.12, "q=%v", q)
	}

	ck.Advance(4 * time.Minute)
	assert.Equal(t, uint64(len(samples)), c.Snapshot(0).Latency.Samples)
	ck.Advance(time.Minute)
	assert.Zero(t, c.Snapshot(0).Latency)
}

func TestLatencyEdges(t *testing.T) {
	c, _ := fake()
	c.Record(Event{})
	assert.Zero(t, c.Snapshot(0).Latency.Samples)
	c.Record(Event{Duration: time.Nanosecond})
	c.Record(Event{Duration: time.Hour})
	l := c.Snapshot(0).Latency
	assert.Equal(t, uint64(2), l.Samples)
	assert.LessOrEqual(t, l.P99Ms, 60000.0)
	assert.Greater(t, l.P99Ms, 50000.0)
}

func TestSpaceSavingHeavyHitter(t *testing.T) {
	c, _ := fake()
	c.Record(Event{Host: "ignored.test", BytesIn: 1})
	c.SetDomains(true)
	c.opts.MaxDomains = 1000
	c.Reset()
	require.True(t, c.Domains())

	const heavy = "heavy.example"
	r := rand.New(rand.NewPCG(3, 3))
	for i := range 100000 {
		c.Record(Event{Host: fmt.Sprintf("h%d.hostile.test", i), BytesIn: 200, BytesOut: 100})
		if i%20 == 0 {
			c.Record(Event{Host: heavy, BytesIn: 10000, BytesOut: 1000 + r.Int64N(100), Transformed: true})
		}
	}
	s := c.Snapshot(5)
	require.NotNil(t, s.TopDomains)
	top := *s.TopDomains
	require.Len(t, top, 5)
	assert.Equal(t, heavy, top[0].Domain)
	assert.Equal(t, uint64(5000), top[0].Requests)
	assert.Equal(t, uint64(5000), top[0].Transformed)
	assert.LessOrEqual(t, c.st.Load().domains.count(), 1000)
	for i := 1; i < len(top); i++ {
		assert.False(t, betterDomain(top[i], top[i-1]))
	}
}

func TestDomainOrdering(t *testing.T) {
	c, _ := fake()
	c.SetDomains(true)
	c.Record(Event{Host: "a", BytesIn: 100, BytesOut: 50})
	c.Record(Event{Host: "b", BytesIn: 100, BytesOut: 50})
	c.Record(Event{Host: "b", BytesIn: 10, BytesOut: 10})
	c.Record(Event{Host: "c", BytesIn: 1000, BytesOut: 10})
	c.Record(Event{Host: "d", BytesIn: 10, BytesOut: 100})
	top := *c.Snapshot(3).TopDomains
	require.Len(t, top, 3)
	assert.Equal(t, []string{"c", "b", "a"}, []string{top[0].Domain, top[1].Domain, top[2].Domain})
	assert.Equal(t, Bucket{2, 0, 110, 60}, top[1].Bucket)
	assert.Empty(t, *c.Snapshot(0).TopDomains)
}

func TestDomainSwitch(t *testing.T) {
	c, _ := fake()
	assert.False(t, c.Domains())
	c.Record(Event{Host: "a.test", BytesIn: 1})
	assert.Nil(t, c.Snapshot(5).TopDomains)

	c.SetDomains(true)
	c.Record(Event{Host: "a.test", BytesIn: 1})
	require.NotNil(t, c.Snapshot(5).TopDomains)
	assert.Len(t, *c.Snapshot(5).TopDomains, 1)

	c.SetDomains(false)
	assert.Nil(t, c.Snapshot(5).TopDomains)
	c.Record(Event{Host: "a.test", BytesIn: 1})
	c.SetDomains(true)
	assert.Empty(t, *c.Snapshot(5).TopDomains)
	c.Reset()
	assert.True(t, c.Domains())
	c.Record(Event{Host: "b.test"})
	assert.Len(t, *c.Snapshot(5).TopDomains, 1)
}

func TestClientsEviction(t *testing.T) {
	ck := newClock(epoch)
	c := New(Options{MaxClients: 100, Now: ck.Now})
	for i := range 1000 {
		ck.Advance(time.Millisecond)
		c.Record(Event{ClientKey: fmt.Sprintf("k%d", i), Addr: "a"})
	}
	list := c.Clients(time.Hour)
	assert.LessOrEqual(t, len(list), 100)
	assert.GreaterOrEqual(t, len(list), 80)
	assert.Equal(t, "k999", list[0].Key)
	_, ok := c.Client("k0")
	assert.False(t, ok)
	_, ok = c.Client("k999")
	assert.True(t, ok)
	assert.True(t, slices.IsSortedFunc(list, func(a, b Client) int { return b.LastSeen.Compare(a.LastSeen) }))
}

func TestClientsData(t *testing.T) {
	c, ck := fake()
	c.Record(Event{ClientKey: "dev1", Device: "dev1", Addr: "1.2.3.4:5", UserAgent: "UA", Profile: "saver",
		Kind: "image", Transformed: true, BytesIn: 100, BytesOut: 40})
	c.Record(Event{ClientKey: "dev1", Kind: "text", BytesIn: 10, BytesOut: 10})
	ck.Advance(time.Minute)
	c.Record(Event{ClientKey: "1.1.1.1", Addr: "1.1.1.1"})

	cl, ok := c.Client("dev1")
	require.True(t, ok)
	assert.Equal(t, "dev1", cl.Device)
	assert.Equal(t, "1.2.3.4:5", cl.Addr)
	assert.Equal(t, "UA", cl.UserAgent)
	assert.Equal(t, "saver", cl.Profile)
	assert.Equal(t, uint64(2), cl.Requests)
	assert.Equal(t, int64(60), cl.Saved)
	assert.Equal(t, Bucket{1, 1, 100, 40}, cl.ByKind["image"])
	assert.Equal(t, Bucket{1, 0, 10, 10}, cl.ByKind["text"])
	assert.True(t, cl.Active)
	assert.True(t, cl.FirstSeen.Equal(epoch))

	list := c.Clients(30 * time.Second)
	require.Len(t, list, 2)
	assert.Equal(t, "1.1.1.1", list[0].Key)
	assert.True(t, list[0].Active)
	assert.False(t, list[1].Active)
	assert.Equal(t, 1, c.ActiveClients(30*time.Second))
	assert.Equal(t, 2, c.ActiveClients(2*time.Minute))

	dv := c.Devices()
	require.Len(t, dv, 1)
	assert.Equal(t, DeviceStat{Requests: 2, BytesIn: 110, BytesOut: 50, Saved: 60, LastSeen: epoch}, normalize(dv["dev1"]))
}

func normalize(d DeviceStat) DeviceStat {
	d.LastSeen = time.Unix(d.LastSeen.Unix(), 0)
	return d
}

func TestOnClientRateLimit(t *testing.T) {
	c, ck := fake()
	var mu sync.Mutex
	var got []Client
	c.OnClient(func(cl Client) {
		mu.Lock()
		got = append(got, cl)
		mu.Unlock()
	})
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(got)
	}
	for range 10 {
		c.Record(Event{ClientKey: "a"})
	}
	assert.Equal(t, 1, count())
	ck.Advance(1900 * time.Millisecond)
	c.Record(Event{ClientKey: "a"})
	assert.Equal(t, 1, count())
	ck.Advance(200 * time.Millisecond)
	c.Record(Event{ClientKey: "a"})
	assert.Equal(t, 2, count())
	c.Record(Event{ClientKey: "b"})
	assert.Equal(t, 3, count())
	assert.Equal(t, "b", got[2].Key)
	assert.True(t, got[2].Active)

	c.OnClient(nil)
	ck.Advance(time.Minute)
	c.Record(Event{ClientKey: "a"})
	assert.Equal(t, 3, count())
}

func TestOnClientNotUnderLock(t *testing.T) {
	c, _ := fake()
	c.OnClient(func(cl Client) {
		_, _ = c.Client(cl.Key)
		_ = c.Clients(time.Minute)
		_ = c.Snapshot(1)
		c.Record(Event{})
	})
	done := make(chan struct{})
	go func() {
		c.Record(Event{ClientKey: "x"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock")
	}
}

func TestBegin(t *testing.T) {
	c, _ := fake()
	d1 := c.Begin("a")
	d2 := c.Begin("a")
	cl, ok := c.Client("a")
	require.True(t, ok)
	assert.Equal(t, int64(2), cl.InFlight)
	d1()
	d1()
	cl, _ = c.Client("a")
	assert.Equal(t, int64(1), cl.InFlight)
	d2()
	cl, _ = c.Client("a")
	assert.Zero(t, cl.InFlight)
	c.Begin("")()
	c.Record(Event{ClientKey: "a"})
	cl, _ = c.Client("a")
	assert.Equal(t, uint64(1), cl.Requests)
}

func TestReset(t *testing.T) {
	c, _ := fake()
	c.SetDomains(true)
	c.Record(Event{ClientKey: "a", Device: "d", Host: "h", Duration: time.Second, BytesIn: 5, Kind: "text", Reason: "x"})
	c.Reset()
	s := c.Snapshot(5)
	assert.Zero(t, s.Totals.Requests)
	assert.Empty(t, s.ByKind)
	assert.Empty(t, s.ByDecision)
	assert.Zero(t, s.Latency)
	assert.Empty(t, *s.TopDomains)
	assert.Empty(t, c.Clients(time.Hour))
	assert.Empty(t, c.Devices())
	for _, p := range s.Series.Seconds {
		assert.Zero(t, p.Requests)
	}
}

func TestSnapshotRaceWithRecord(t *testing.T) {
	c := New(Options{})
	c.SetDomains(true)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				c.Record(Event{Kind: "image", Host: fmt.Sprintf("h%d", i%50), ClientKey: fmt.Sprintf("c%d", i%20),
					Device: "d", Duration: time.Millisecond, BytesIn: 10})
			}
		})
	}
	for range 200 {
		_ = c.Snapshot(10)
		_ = c.Clients(time.Minute)
		_ = c.Devices()
	}
	c.SetDomains(false)
	c.Reset()
	c.SetDomains(true)
	close(stop)
	wg.Wait()
}

func tsInterfaces(t *testing.T) map[string][]string {
	t.Helper()
	src, err := os.ReadFile("../../web/src/api/types.ts")
	require.NoError(t, err)
	head := regexp.MustCompile(`(?m)^export interface (\w+)(?: extends (\w+))? \{$`)
	field := regexp.MustCompile(`^\s+(\w+)\??:`)
	out := map[string][]string{}
	lines := strings.Split(string(src), "\n")
	for i, l := range lines {
		m := head.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		var fields []string
		if m[2] != "" {
			fields = append(fields, out[m[2]]...)
		}
		for _, l := range lines[i+1:] {
			if l == "}" {
				break
			}
			if f := field.FindStringSubmatch(l); f != nil {
				fields = append(fields, f[1])
			}
		}
		out[m[1]] = fields
	}
	return out
}

func keysOf(t *testing.T, v any) []string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(b, &m))
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestJSONMatchesTypes(t *testing.T) {
	ts := tsInterfaces(t)
	for name, v := range map[string]any{
		"Bucket": Bucket{}, "Totals": Totals{}, "Latency": Latency{}, "DomainStat": DomainStat{},
		"SeriesPoint": SeriesPoint{}, "Series": Series{}, "Client": Client{},
	} {
		require.NotEmpty(t, ts[name], name)
		assert.ElementsMatch(t, ts[name], keysOf(t, v), name)
	}
	stats := ts["StatsSnapshot"]
	for _, k := range keysOf(t, Snapshot{}) {
		assert.Contains(t, stats, k)
	}
	dev := keysOf(t, DeviceStat{})
	for _, k := range dev {
		assert.Contains(t, ts["Device"], k)
	}
}

func TestSnapshotGolden(t *testing.T) {
	c, ck := fake()
	c.SetDomains(true)
	c.Record(Event{Kind: "image", Codec: "webp", Profile: "balanced", Reason: "transformed", Transformed: true,
		BytesIn: 1000, BytesOut: 400, Duration: 10 * time.Millisecond, Host: "a.test"})
	s := c.Snapshot(5)
	s.Series.Seconds = s.Series.Seconds[118:]
	s.Series.Minutes = s.Series.Minutes[178:]
	b, err := json.Marshal(s)
	require.NoError(t, err)
	now := ck.Now().Unix()
	min := now / 60 * 60
	want := fmt.Sprintf(`{"totals":{"requests":1,"transformed":1,"bytes_in":1000,"bytes_out":400,"saved":600,"saved_percent":60},`+
		`"by_kind":{"image":{"requests":1,"transformed":1,"bytes_in":1000,"bytes_out":400}},`+
		`"by_codec":{"webp":{"requests":1,"transformed":1,"bytes_in":1000,"bytes_out":400}},`+
		`"by_profile":{"balanced":{"requests":1,"transformed":1,"bytes_in":1000,"bytes_out":400}},`+
		`"by_decision":{"transformed":1},`+
		`"latency":{"p50_ms":%v,"p95_ms":%v,"p99_ms":%v,"samples":1},`+
		`"top_domains":[{"domain":"a.test","requests":1,"transformed":1,"bytes_in":1000,"bytes_out":400}],`+
		`"series":{"seconds":[{"t":%d,"requests":0,"bytes_in":0,"bytes_out":0},{"t":%d,"requests":1,"bytes_in":1000,"bytes_out":400}],`+
		`"minutes":[{"t":%d,"requests":0,"bytes_in":0,"bytes_out":0},{"t":%d,"requests":1,"bytes_in":1000,"bytes_out":400}]}}`,
		s.Latency.P50Ms, s.Latency.P95Ms, s.Latency.P99Ms, now-1, now, min-60, min)
	assert.JSONEq(t, want, string(b))
	assert.InDelta(t, 10.0, s.Latency.P50Ms, 1.2)

	off, _ := fake()
	b, err = json.Marshal(off.Snapshot(5))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"top_domains":null`)
	assert.Contains(t, string(b), `"by_kind":{}`)
}

func TestClientJSON(t *testing.T) {
	c, _ := fake()
	c.Record(Event{ClientKey: "k", Kind: "font", BytesIn: 5, BytesOut: 1})
	cl, _ := c.Client("k")
	b, err := json.Marshal(cl)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"by_kind":{"font":{"requests":1,"transformed":0,"bytes_in":5,"bytes_out":1}}`)
	assert.Contains(t, string(b), `"first_seen":"20`)
}

func BenchmarkRecord(b *testing.B) {
	c := New(Options{})
	c.SetDomains(true)
	evs := make([]Event, 64)
	for i := range evs {
		evs[i] = Event{
			Time: time.Now(), Kind: kindNames[i%4], Codec: []string{"avif", "webp", "br", "gzip"}[i%4],
			Profile: "balanced", Reason: "transformed", Transformed: true, BytesIn: 5000, BytesOut: 2000,
			Duration: time.Duration(i+1) * time.Millisecond, Host: fmt.Sprintf("h%d.test", i%32),
			ClientKey: fmt.Sprintf("c%d", i%16), Device: "dev", Addr: "1.2.3.4:5", UserAgent: "ua",
		}
	}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := rand.IntN(len(evs))
		for pb.Next() {
			c.Record(evs[i&63])
			i++
		}
	})
}

func BenchmarkRecordNoDomains(b *testing.B) {
	c := New(Options{})
	e := Event{Time: time.Now(), Kind: "image", Codec: "webp", Profile: "balanced", Reason: "transformed",
		Transformed: true, BytesIn: 5000, BytesOut: 2000, Duration: time.Millisecond, ClientKey: "c", Device: "d"}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Record(e)
		}
	})
}

func BenchmarkSnapshot(b *testing.B) {
	c := New(Options{})
	c.SetDomains(true)
	for i := range 5000 {
		c.Record(Event{Kind: kindNames[i%4], Codec: "webp", Profile: "balanced", Reason: "transformed",
			BytesIn: int64(i), BytesOut: int64(i / 2), Duration: time.Duration(i) * time.Microsecond,
			Host: fmt.Sprintf("h%d.test", i%3000)})
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = c.Snapshot(10)
	}
}
