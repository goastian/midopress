package stats

import "time"

// Event describes one response the proxy handled.
type Event struct {
	// Time of the response, zero means now.
	Time time.Time
	// Kind is image, text, font or other; anything else counts as other.
	Kind string
	// Codec is what was sent: avif, webp, jxl, jpeg, png, gif, br, zstd, gzip,
	// deflate, woff2 or identity. Empty counts as identity.
	Codec   string
	Profile string
	// Reason is "transformed" or why the response was left alone.
	Reason      string
	Transformed bool
	BytesIn     int64
	BytesOut    int64
	// Duration is the time the pipeline took; zero means not measured and the
	// event does not enter the latency quantiles.
	Duration time.Duration
	// Host is empty when the caller does not want domains aggregated.
	Host string
	// ClientKey is the device id, or the address when there is no token. Empty
	// means the event is not attributed to a client.
	ClientKey string
	Device    string
	Addr      string
	UserAgent string
	CacheHit  bool
}

// Bucket is what a group of responses amounted to.
type Bucket struct {
	Requests    uint64 `json:"requests"`
	Transformed uint64 `json:"transformed"`
	BytesIn     uint64 `json:"bytes_in"`
	BytesOut    uint64 `json:"bytes_out"`
}

// Totals is a Bucket with the savings.
type Totals struct {
	Bucket
	Saved        int64   `json:"saved"`
	SavedPercent float64 `json:"saved_percent"`
}

// Latency holds the quantiles of the pipeline time, in milliseconds, over the
// last five minutes.
type Latency struct {
	P50Ms   float64 `json:"p50_ms"`
	P95Ms   float64 `json:"p95_ms"`
	P99Ms   float64 `json:"p99_ms"`
	Samples uint64  `json:"samples"`
}

// DomainStat is the traffic of one host.
type DomainStat struct {
	Domain string `json:"domain"`
	Bucket
}

// SeriesPoint is the traffic of one interval.
type SeriesPoint struct {
	// T is the unix time in seconds of the start of the interval.
	T        int64  `json:"t"`
	Requests uint64 `json:"requests"`
	BytesIn  uint64 `json:"bytes_in"`
	BytesOut uint64 `json:"bytes_out"`
}

// Series holds the last 120 seconds and the last 180 minutes, oldest first.
type Series struct {
	Seconds []SeriesPoint `json:"seconds"`
	Minutes []SeriesPoint `json:"minutes"`
}

// Client is somebody that is connected, with or without a token.
type Client struct {
	Key       string            `json:"key"`
	Device    string            `json:"device"`
	Addr      string            `json:"addr"`
	UserAgent string            `json:"user_agent"`
	FirstSeen time.Time         `json:"first_seen"`
	LastSeen  time.Time         `json:"last_seen"`
	Active    bool              `json:"active"`
	Requests  uint64            `json:"requests"`
	InFlight  int64             `json:"in_flight"`
	BytesIn   uint64            `json:"bytes_in"`
	BytesOut  uint64            `json:"bytes_out"`
	Saved     int64             `json:"saved"`
	Profile   string            `json:"profile"`
	ByKind    map[string]Bucket `json:"by_kind"`
}

// DeviceStat is what a device with a token has done since the start.
type DeviceStat struct {
	Requests uint64    `json:"requests"`
	BytesIn  uint64    `json:"bytes_in"`
	BytesOut uint64    `json:"bytes_out"`
	Saved    int64     `json:"saved"`
	LastSeen time.Time `json:"last_seen"`
}

// Snapshot is the traffic part of the stats the dashboard shows.
type Snapshot struct {
	Totals     Totals            `json:"totals"`
	ByKind     map[string]Bucket `json:"by_kind"`
	ByCodec    map[string]Bucket `json:"by_codec"`
	ByProfile  map[string]Bucket `json:"by_profile"`
	ByDecision map[string]uint64 `json:"by_decision"`
	Latency    Latency           `json:"latency"`
	// TopDomains is nil, and null in JSON, while the aggregation is off.
	TopDomains *[]DomainStat `json:"top_domains"`
	Series     Series        `json:"series"`
}

func newTotals(b Bucket) Totals {
	t := Totals{Bucket: b, Saved: int64(b.BytesIn) - int64(b.BytesOut)}
	if b.BytesIn > 0 {
		t.SavedPercent = float64(t.Saved) / float64(b.BytesIn) * 100
	}
	return t
}
