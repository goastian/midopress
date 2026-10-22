package admin

import (
	"time"

	"github.com/goastian/midopress/internal/flows"
	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/stats"
)

// The JSON of the API. Every shape is the one of web/src/api/types.ts.

type apiError struct {
	Error  string  `json:"error"`
	Code   string  `json:"code"`
	Issues []Issue `json:"issues,omitempty"`
}

// Issue is a problem found in what the interface sent.
type Issue struct {
	Index   int    `json:"index"`
	Field   string `json:"field"`
	Column  int    `json:"column"`
	Message string `json:"message"`
}

type validateResult struct {
	OK     bool    `json:"ok"`
	Issues []Issue `json:"issues"`
}

type cacheView struct {
	Hits        uint64  `json:"hits"`
	Misses      uint64  `json:"misses"`
	HitRate     float64 `json:"hit_rate"`
	MemoryBytes int64   `json:"memory_bytes"`
	MemoryItems int     `json:"memory_items"`
	DiskBytes   int64   `json:"disk_bytes"`
	DiskItems   int     `json:"disk_items"`
	Evictions   uint64  `json:"evictions"`
	Expired     uint64  `json:"expired"`
	DiskErrors  uint64  `json:"disk_errors"`
}

type connectionsView struct {
	ClientsActive      int    `json:"clients_active"`
	RequestsInFlight   int64  `json:"requests_in_flight"`
	TunnelsActive      int64  `json:"tunnels_active"`
	TunnelsTotal       uint64 `json:"tunnels_total"`
	TunnelBytesUp      uint64 `json:"tunnel_bytes_up"`
	TunnelBytesDown    uint64 `json:"tunnel_bytes_down"`
	TLSHandshakes      uint64 `json:"tls_handshakes"`
	TLSHandshakeErrors uint64 `json:"tls_handshake_errors"`
	UpstreamErrors     uint64 `json:"upstream_errors"`
	Denied             uint64 `json:"denied"`
}

type cpuView struct {
	Slots           int `json:"slots"`
	Busy            int `json:"busy"`
	BackgroundQueue int `json:"background_queue"`
}

// StatsSnapshot is what the dashboard shows.
type StatsSnapshot struct {
	Time          time.Time `json:"time"`
	UptimeSeconds float64   `json:"uptime_seconds"`
	Version       string    `json:"version"`
	stats.Snapshot
	Cache       cacheView       `json:"cache"`
	Connections connectionsView `json:"connections"`
	CPU         cpuView         `json:"cpu"`
}

type flowList struct {
	Enabled  bool         `json:"enabled"`
	Capacity int          `json:"capacity"`
	Total    int          `json:"total"`
	Flows    []flows.Flow `json:"flows"`
}

type flowCompare struct {
	Metric        string  `json:"metric"`
	Distance      float64 `json:"distance"`
	OriginalBytes int     `json:"original_bytes"`
	ResultBytes   int     `json:"result_bytes"`
	Width         int     `json:"width"`
	Height        int     `json:"height"`
	Comparable    bool    `json:"comparable"`
}

// Device is a device with a token.
type Device struct {
	ID       string     `json:"id"`
	Profile  string     `json:"profile"`
	Created  time.Time  `json:"created"`
	LastSeen *time.Time `json:"last_seen"`
	Active   bool       `json:"active"`
	Requests uint64     `json:"requests"`
	BytesIn  uint64     `json:"bytes_in"`
	BytesOut uint64     `json:"bytes_out"`
	Saved    int64      `json:"saved"`
}

type newDevice struct {
	ID          string `json:"id"`
	Profile     string `json:"profile"`
	Token       string `json:"token"`
	ConfigURL   string `json:"config_url"`
	ConfigQRSVG string `json:"config_qr_svg"`
}

type profileInfo struct {
	Name              string              `json:"name"`
	Builtin           bool                `json:"builtin"`
	Modified          bool                `json:"modified"`
	Lossless          bool                `json:"lossless"`
	TargetDSSIM       float64             `json:"target_dssim"`
	TargetSSIM        float64             `json:"target_ssim"`
	TargetSSIMULACRA2 float64             `json:"target_ssimulacra2"`
	Attempts          int                 `json:"attempts"`
	Formats           []string            `json:"formats"`
	ResizeToViewport  bool                `json:"resize_to_viewport"`
	PhotoColors       int                 `json:"photo_colors"`
	QuantizePNG       bool                `json:"quantize_png"`
	AnimatedToStill   bool                `json:"animated_to_still"`
	AnimatedQuality   int                 `json:"animated_quality"`
	Text              textLevels          `json:"text"`
	Override          img.ProfileOverride `json:"override"`
}

type textLevels struct {
	Brotli       int `json:"brotli"`
	Gzip         int `json:"gzip"`
	Zstd         int `json:"zstd"`
	BrotliStatic int `json:"brotli_static"`
}

type managedBy struct {
	DefaultProfile bool `json:"default_profile"`
	Profiles       bool `json:"profiles"`
	Rules          bool `json:"rules"`
}

type configView struct {
	Config         map[string]any `json:"config"`
	DefaultProfile string         `json:"default_profile"`
	Profiles       []profileInfo  `json:"profiles"`
	Rules          []ruleJSON     `json:"rules"`
	StateFile      string         `json:"state_file"`
	ManagedByUI    managedBy      `json:"managed_by_ui"`
}

type testRequest struct {
	URL           string            `json:"url"`
	Method        string            `json:"method"`
	ContentType   string            `json:"content_type"`
	Status        int               `json:"status"`
	Headers       map[string]string `json:"headers"`
	DeviceProfile string            `json:"device_profile"`
	Src           string            `json:"src"`
}

type ruleExplanation struct {
	Index   int    `json:"index"`
	Match   string `json:"match"`
	Matched bool   `json:"matched"`
	Reason  string `json:"reason"`
	Action  string `json:"action"`
	Profile string `json:"profile"`
}

type testResult struct {
	Explanations  []ruleExplanation `json:"explanations"`
	Action        string            `json:"action"`
	Profile       string            `json:"profile"`
	ProfileSource string            `json:"profile_source"`
	Set           map[string]any    `json:"set"`
	Outcome       string            `json:"outcome"`
	Note          string            `json:"note"`
}

type caInfo struct {
	Available bool   `json:"available"`
	Subject   string `json:"subject"`
	NotBefore string `json:"not_before"`
	NotAfter  string `json:"not_after"`
	SHA256    string `json:"sha256"`
	PageURL   string `json:"page_url"`
	MagicHost string `json:"magic_host"`
}

// Settings are the switches that can be flipped while running.
type Settings struct {
	Flows   bool `json:"flows"`
	Domains bool `json:"domains"`
	Capture bool `json:"capture"`
}

type module struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

type about struct {
	Version       string          `json:"version"`
	GoVersion     string          `json:"go_version"`
	OS            string          `json:"os"`
	Arch          string          `json:"arch"`
	Started       time.Time       `json:"started"`
	UptimeSeconds float64         `json:"uptime_seconds"`
	Backend       string          `json:"backend"`
	Backends      []string        `json:"backends"`
	Native        map[string]bool `json:"native"`
	Modules       []module        `json:"modules"`
	StateDir      string          `json:"state_dir"`
	ProxyAddr     string          `json:"proxy_addr"`
	TLS           bool            `json:"tls"`
	MITM          bool            `json:"mitm"`
	AuthMode      string          `json:"auth_mode"`
}
