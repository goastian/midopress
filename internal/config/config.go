// Package config defines the MidoPress configuration and how it is loaded:
// defaults, then a YAML file, then MIDOPRESS_* environment variables. Command
// line flags are applied on top by the caller.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/rules"
)

// Config is the whole configuration.
type Config struct {
	Listen   Listen   `yaml:"listen"`
	MITM     MITM     `yaml:"mitm"`
	Auth     Auth     `yaml:"auth"`
	Upstream Upstream `yaml:"upstream"`
	Server   Server   `yaml:"server"`
	Pipeline Pipeline `yaml:"pipeline"`
	Cache    Cache    `yaml:"cache"`
	// Profiles changes the built-in profiles or defines new ones.
	Profiles map[string]img.ProfileOverride `yaml:"profiles"`
	// Rules decide what happens to each request, in order, see internal/rules.
	Rules   []rules.Rule `yaml:"rules"`
	Metrics Metrics      `yaml:"metrics"`
	Web     Web          `yaml:"web"`
	Log     Log          `yaml:"log"`
}

// Web configures the administration server and the magic host.
type Web struct {
	// Enabled turns the administration server on.
	Enabled bool `yaml:"enabled"`
	// Addr is where it listens. It is meant for the person who runs the proxy:
	// the default is the loopback interface, and anything else needs the token.
	Addr string `yaml:"addr"`
	// AdminTokenFile holds the token that opens the administration interface. It
	// is made, with permissions 600, the first time the proxy starts. Empty
	// means admin.token in StateDir.
	AdminTokenFile string `yaml:"admin_token_file"`
	// StateDir is where the proxy keeps what the interface saves, and the token.
	// Empty means ~/.midopress.
	StateDir string `yaml:"state_dir"`
	// Flows starts with the live list of flows on. The list shows URLs, so it is
	// off unless asked for, and it is only kept in memory.
	Flows bool `yaml:"flows"`
	// Domains starts with the aggregation of traffic by domain on.
	Domains bool `yaml:"domains"`
	// FlowBuffer is how many flows are kept.
	FlowBuffer int `yaml:"flow_buffer"`
	// CaptureBytes is the memory used to keep the bodies of images, so that the
	// original and the result of a flow can be compared. 0 keeps none.
	CaptureBytes ByteSize `yaml:"capture_bytes"`
	// MagicHost is the name the proxy answers by itself, with the page that
	// explains how to install the certificate.
	MagicHost string `yaml:"magic_host"`
	// Pprof serves the Go profiler under /debug/pprof on the administration
	// server.
	Pprof bool `yaml:"pprof"`
}

// StateDirPath returns the directory of the state, with the default applied.
func (w Web) StateDirPath() string {
	if w.StateDir != "" {
		return w.StateDir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".midopress")
	}
	return ".midopress"
}

// TokenPath returns the file of the admin token.
func (w Web) TokenPath() string {
	if w.AdminTokenFile != "" {
		return w.AdminTokenFile
	}
	return filepath.Join(w.StateDirPath(), "admin.token")
}

// UIPath returns the file where the settings saved from the interface live.
func (w Web) UIPath() string { return filepath.Join(w.StateDirPath(), "ui.yaml") }

// Cache configures the cache of recoded images, fonts and slowly compressed text.
type Cache struct {
	// Memory is the size of the memory tier, 0 for none.
	Memory ByteSize `yaml:"memory"`
	// Dir is the directory of the disk tier, empty for no disk tier.
	Dir string `yaml:"dir"`
	// MaxSize is the size the disk tier is kept under.
	MaxSize ByteSize `yaml:"max_size"`
	// MinTTL, MaxTTL and DefaultTTL bound how long a result is kept; the origin's
	// Cache-Control and Expires choose within them.
	MinTTL     time.Duration `yaml:"min_ttl"`
	MaxTTL     time.Duration `yaml:"max_ttl"`
	DefaultTTL time.Duration `yaml:"default_ttl"`
}

// Metrics configures the Prometheus endpoint.
type Metrics struct {
	Prometheus bool `yaml:"prometheus"`
	// Addr serves /metrics on a listener of its own, for example
	// "127.0.0.1:9100". Empty serves it on the proxy port, at /metrics, to the
	// clients the proxy accepts.
	Addr string `yaml:"addr"`
}

// Listen configures the address the proxy accepts connections on.
type Listen struct {
	Addr string `yaml:"addr"`
	TLS  TLS    `yaml:"tls"`
}

// TLS is a certificate and key pair. When set, clients talk TLS to the proxy.
type TLS struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

// MITM configures HTTPS interception.
type MITM struct {
	CACert             string        `yaml:"ca_cert"`
	CAKey              string        `yaml:"ca_key"`
	LeafTTL            time.Duration `yaml:"leaf_ttl"`
	CacheSize          int           `yaml:"cache_size"`
	AutoPassthroughTTL time.Duration `yaml:"auto_passthrough_ttl"`
	Passthrough        []string      `yaml:"passthrough"`
	DefaultPassthrough bool          `yaml:"default_passthrough"`
}

// Enabled reports whether HTTPS interception is configured.
func (m MITM) Enabled() bool { return m.CACert != "" }

// Auth configures client authentication and access control.
type Auth struct {
	Mode          string        `yaml:"mode"` // none, basic or token
	User          string        `yaml:"user"`
	Pass          string        `yaml:"pass"`
	DevicesFile   string        `yaml:"devices_file"`
	BlockGlobal   bool          `yaml:"block_global"`
	MaxFailures   int           `yaml:"max_failures"`
	FailureWindow time.Duration `yaml:"failure_window"`
}

// Upstream configures the connections the proxy makes to origin servers.
type Upstream struct {
	DialTimeout           time.Duration `yaml:"dial_timeout"`
	TLSHandshakeTimeout   time.Duration `yaml:"tls_handshake_timeout"`
	ResponseHeaderTimeout time.Duration `yaml:"response_header_timeout"`
	IdleConnTimeout       time.Duration `yaml:"idle_conn_timeout"`
	MaxIdleConnsPerHost   int           `yaml:"max_idle_conns_per_host"`
	DNSCacheTTL           time.Duration `yaml:"dns_cache_ttl"`
	DenyPrivate           bool          `yaml:"deny_private"`
}

// Server configures the listeners.
type Server struct {
	ReadHeaderTimeout   time.Duration `yaml:"read_header_timeout"`
	IdleTimeout         time.Duration `yaml:"idle_timeout"`
	TLSHandshakeTimeout time.Duration `yaml:"tls_handshake_timeout"`
	ShutdownGrace       time.Duration `yaml:"shutdown_grace"`
}

// Pipeline configures what is done to responses.
type Pipeline struct {
	MinGain                 float64       `yaml:"min_gain"`
	MaxConcurrentTranscodes int           `yaml:"max_concurrent_transcodes"`
	TranscodeWait           time.Duration `yaml:"transcode_wait"`
	// MemoryBudget bounds what the pipeline holds in buffers and pictures at the
	// same time. 0 takes a quarter of the memory of the machine or container,
	// between 256 MiB and 8 GiB.
	MemoryBudget ByteSize `yaml:"memory_budget"`
	// Profile is how far images may be pushed: lossless, balanced, saver or
	// extreme. A client can ask for another one per request.
	Profile string `yaml:"profile"`
	// NoTransform is what to do with a response that says Cache-Control:
	// no-transform: respect it, recode the images that say it, recode anything,
	// or (the default, "profile") do what the profile of the request says.
	NoTransform string `yaml:"no_transform"`
	Text        Text   `yaml:"text"`
	Image       Image  `yaml:"image"`
}

// Text configures the compression of text like content.
type Text struct {
	Brotli     int      `yaml:"brotli"`
	Gzip       int      `yaml:"gzip"`
	Zstd       int      `yaml:"zstd"`
	Minify     Minify   `yaml:"minify"`
	Recompress bool     `yaml:"recompress"`
	BufferMax  ByteSize `yaml:"buffer_max"`
	MinBytes   ByteSize `yaml:"min_bytes"`
	MaxDecoded ByteSize `yaml:"max_decoded"`
}

// Minify says how much each kind of text may be minified: off, safe or
// aggressive. In YAML a plain true or false is accepted too, for the settings
// of earlier versions: true is safe for every kind.
type Minify struct {
	HTML string `yaml:"html"`
	CSS  string `yaml:"css"`
	JS   string `yaml:"js"`
	SVG  string `yaml:"svg"`
	JSON string `yaml:"json"`
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (m *Minify) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var on bool
		if err := n.Decode(&on); err != nil {
			return fmt.Errorf("minify must be true, false or a map: %w", err)
		}
		mode := "off"
		if on {
			mode = "safe"
		}
		*m = Minify{HTML: mode, CSS: mode, JS: mode, SVG: mode, JSON: mode}
		return nil
	}
	type plain Minify
	return n.Decode((*plain)(m))
}

// Image configures image recoding.
type Image struct {
	// JPEG turns JPEG recoding on or off.
	JPEG bool `yaml:"jpeg"`
	// JPEGQuality fixes the quality of recoded images, 1 to 100. 0 lets the
	// proxy find, for each image, the lowest quality that meets the profile.
	JPEGQuality int  `yaml:"jpeg_quality"`
	PNG         bool `yaml:"png"`
	GIF         bool `yaml:"gif"`
	// Modern recodes the WebP and AVIF pictures that sites already send.
	Modern bool `yaml:"modern"`
	// PageHints reads the HTML that goes by to learn how wide each image is shown,
	// for the profiles that resize, and which files have an integrity hash.
	PageHints bool `yaml:"page_hints"`
	// HintDPR is the pixel density assumed for those widths.
	HintDPR   float64       `yaml:"hint_dpr"`
	MinBytes  ByteSize      `yaml:"min_bytes"`
	MaxBytes  ByteSize      `yaml:"max_bytes"`
	MaxPixels int64         `yaml:"max_pixels"`
	Timeout   time.Duration `yaml:"timeout"`
	// Metric measures how close a recoded image is to the original: dssim
	// (default), ssim or ssimulacra2.
	Metric string `yaml:"metric"`
	// Backend is the image codec backend: wasm (default) or vips, which needs a
	// binary built with the vips tag.
	Backend string `yaml:"backend"`
	// Background lets slow formats, which do not fit in timeout, be produced
	// after the response was sent, for the next request to find in the cache.
	Background bool `yaml:"background"`
	// BackgroundTimeout is the longest one such job may take.
	BackgroundTimeout time.Duration `yaml:"background_timeout"`
	// Warmup compiles the image codecs when the proxy starts, so that the first
	// image does not pay for it.
	Warmup bool `yaml:"warmup"`
}

// Log configures logging.
type Log struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
	URLs   bool   `yaml:"urls"`
}

// Default returns the default configuration.
func Default() *Config {
	return &Config{
		Listen: Listen{Addr: ":9999"},
		MITM: MITM{
			LeafTTL:            48 * time.Hour,
			CacheSize:          10000,
			AutoPassthroughTTL: 6 * time.Hour,
			DefaultPassthrough: true,
		},
		Auth: Auth{
			Mode:          "none",
			BlockGlobal:   true,
			MaxFailures:   10,
			FailureWindow: time.Minute,
		},
		Upstream: Upstream{
			DialTimeout:           10 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConnsPerHost:   64,
			DNSCacheTTL:           30 * time.Second,
		},
		Server: Server{
			ReadHeaderTimeout:   10 * time.Second,
			IdleTimeout:         120 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
			ShutdownGrace:       30 * time.Second,
		},
		Pipeline: Pipeline{
			MinGain:       0.02,
			TranscodeWait: 250 * time.Millisecond,
			MemoryBudget:  0,
			Profile:       "balanced",
			NoTransform:   "profile",
			Text: Text{
				Minify:     Minify{HTML: "safe", CSS: "safe", JS: "off", SVG: "safe", JSON: "safe"},
				Recompress: true,
				BufferMax:  1 << 20,
				MinBytes:   256,
				MaxDecoded: 32 << 20,
			},
			Image: Image{
				JPEG:              true,
				PNG:               true,
				GIF:               true,
				Modern:            true,
				PageHints:         true,
				HintDPR:           2,
				MinBytes:          1024,
				MaxBytes:          8 << 20,
				MaxPixels:         16_000_000,
				Timeout:           3 * time.Second,
				Metric:            "dssim",
				Backend:           "wasm",
				Background:        true,
				BackgroundTimeout: 2 * time.Minute,
				Warmup:            true,
			},
		},
		Cache: Cache{
			Memory:     256 << 20,
			MaxSize:    5 << 30,
			MinTTL:     time.Minute,
			MaxTTL:     7 * 24 * time.Hour,
			DefaultTTL: time.Hour,
		},
		Profiles: map[string]img.ProfileOverride{},
		Rules:    []rules.Rule{},
		Metrics:  Metrics{Prometheus: true},
		Web: Web{
			Enabled:      true,
			Addr:         "127.0.0.1:9998",
			FlowBuffer:   500,
			CaptureBytes: 64 << 20,
			MagicHost:    "midopress.it",
		},
		Log: Log{Level: "info", Format: "text"},
	}
}

// Load builds the configuration from the defaults, the YAML file at path (if
// not empty) and the environment, in that order. Unknown keys in the file are
// an error, so that a typo does not silently leave a setting at its default.
func Load(path string, environ []string) (*Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading config: %w", err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
	}
	if err := ApplyEnv(cfg, environ); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks the values and returns every problem found.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if _, _, err := net.SplitHostPort(c.Listen.Addr); err != nil {
		add("listen.addr %q: %v", c.Listen.Addr, err)
	}
	if (c.Listen.TLS.Cert == "") != (c.Listen.TLS.Key == "") {
		add("listen.tls.cert and listen.tls.key must be set together")
	}
	if (c.MITM.CACert == "") != (c.MITM.CAKey == "") {
		add("mitm.ca_cert and mitm.ca_key must be set together")
	}
	if c.MITM.LeafTTL < time.Hour {
		add("mitm.leaf_ttl must be at least 1h")
	}
	if c.MITM.CacheSize < 1 {
		add("mitm.cache_size must be positive")
	}
	if c.MITM.AutoPassthroughTTL <= 0 {
		add("mitm.auto_passthrough_ttl must be positive")
	}

	switch c.Auth.Mode {
	case "none":
	case "basic":
		if c.Auth.User == "" || c.Auth.Pass == "" {
			add("auth.mode basic needs auth.user and auth.pass")
		}
	case "token":
		if c.Auth.DevicesFile == "" {
			add("auth.mode token needs auth.devices_file")
		}
	default:
		add("auth.mode %q must be none, basic or token", c.Auth.Mode)
	}
	if c.Auth.Mode == "none" && !c.Auth.BlockGlobal {
		add("refusing to run an open proxy: enable authentication or keep auth.block_global")
	}
	if c.Auth.MaxFailures < 1 {
		add("auth.max_failures must be positive")
	}
	if c.Auth.FailureWindow <= 0 {
		add("auth.failure_window must be positive")
	}

	if nt := c.Pipeline.NoTransform; nt != "profile" && !slices.Contains(NoTransformModes, nt) {
		add("pipeline.no_transform %q must be profile, respect, images or all", nt)
	}

	positive := map[string]time.Duration{
		"upstream.dial_timeout":             c.Upstream.DialTimeout,
		"upstream.tls_handshake_timeout":    c.Upstream.TLSHandshakeTimeout,
		"upstream.response_header_timeout":  c.Upstream.ResponseHeaderTimeout,
		"upstream.idle_conn_timeout":        c.Upstream.IdleConnTimeout,
		"server.read_header_timeout":        c.Server.ReadHeaderTimeout,
		"server.idle_timeout":               c.Server.IdleTimeout,
		"server.tls_handshake_timeout":      c.Server.TLSHandshakeTimeout,
		"server.shutdown_grace":             c.Server.ShutdownGrace,
		"pipeline.transcode_wait":           c.Pipeline.TranscodeWait,
		"pipeline.image.timeout":            c.Pipeline.Image.Timeout,
		"pipeline.image.background_timeout": c.Pipeline.Image.BackgroundTimeout,
	}
	for name, d := range positive {
		if d <= 0 {
			add("%s must be positive", name)
		}
	}
	if c.Upstream.MaxIdleConnsPerHost < 1 {
		add("upstream.max_idle_conns_per_host must be positive")
	}

	p := c.Pipeline
	if p.MinGain < 0 || p.MinGain > 0.9 {
		add("pipeline.min_gain must be between 0 and 0.9")
	}
	if p.MaxConcurrentTranscodes < 0 {
		add("pipeline.max_concurrent_transcodes must not be negative")
	}
	if p.MemoryBudget != 0 && p.MemoryBudget < 1<<20 {
		add("pipeline.memory_budget must be 0 (automatic) or at least 1MiB")
	}
	if p.Image.HintDPR < 1 || p.Image.HintDPR > 4 {
		add("pipeline.image.hint_dpr must be between 1 and 4")
	}
	if p.Text.Brotli < 0 || p.Text.Brotli > 11 {
		add("pipeline.text.brotli must be between 0 and 11")
	}
	if p.Text.Gzip < 0 || p.Text.Gzip > 9 {
		add("pipeline.text.gzip must be between 0 and 9")
	}
	if p.Text.Zstd < 0 || p.Text.Zstd > 22 {
		add("pipeline.text.zstd must be between 0 and 22")
	}
	for name, mode := range map[string]string{"html": p.Text.Minify.HTML, "css": p.Text.Minify.CSS, "js": p.Text.Minify.JS, "svg": p.Text.Minify.SVG, "json": p.Text.Minify.JSON} {
		switch mode {
		case "off", "safe", "aggressive":
		default:
			add("pipeline.text.minify.%s %q must be off, safe or aggressive", name, mode)
		}
	}
	if _, err := img.ParseMetric(p.Image.Metric); err != nil {
		add("pipeline.image.metric: %v", err)
	}
	if p.Image.Backend != "wasm" && p.Image.Backend != "vips" {
		add("pipeline.image.backend %q must be wasm or vips", p.Image.Backend)
	}
	k := c.Cache
	if k.Memory < 0 || k.MaxSize < 0 {
		add("cache sizes must not be negative")
	}
	if k.Dir != "" && k.MaxSize < 1<<20 {
		add("cache.max_size must be at least 1MiB when cache.dir is set")
	}
	if k.MinTTL <= 0 || k.MaxTTL < k.MinTTL || k.DefaultTTL < k.MinTTL || k.DefaultTTL > k.MaxTTL {
		add("cache TTLs must satisfy 0 < min_ttl <= default_ttl <= max_ttl")
	}
	profiles, err := img.BuildProfiles(c.Profiles)
	if err != nil {
		add("profiles: %v", err)
	}
	if _, ok := profiles[p.Profile]; profiles != nil && !ok {
		add("pipeline.profile %q is not a built-in or defined profile", p.Profile)
	}
	if profiles != nil {
		names := make([]string, 0, len(profiles))
		for n := range profiles {
			names = append(names, n)
		}
		if _, err := rules.Compile(c.Rules, rules.WithProfiles(names...)); err != nil {
			add("rules: %v", err)
		}
	}
	for i, r := range c.Rules {
		if err := ValidateSet(r.Set); err != nil {
			add("rules[%d].set: %v", i, err)
		}
	}
	if _, err := rules.NewHostMatcher(c.MITM.Passthrough); err != nil {
		add("mitm.passthrough: %v", err)
	}
	if p.Text.BufferMax < 1024 {
		add("pipeline.text.buffer_max must be at least 1KiB")
	}
	if p.Text.MaxDecoded < p.Text.BufferMax {
		add("pipeline.text.max_decoded must not be smaller than buffer_max")
	}
	if p.Image.JPEGQuality < 0 || p.Image.JPEGQuality > 100 {
		add("pipeline.image.jpeg_quality must be between 0 and 100")
	}
	if p.Image.MaxBytes < p.Image.MinBytes || p.Image.MaxBytes < 1024 {
		add("pipeline.image.max_bytes must be at least min_bytes and 1KiB")
	}
	if p.Image.MaxPixels < 1 {
		add("pipeline.image.max_pixels must be positive")
	}

	w := c.Web
	if w.Enabled && strings.TrimSpace(w.Addr) == "" {
		add("web.addr must be set when web.enabled is true")
	}
	if w.FlowBuffer < 1 || w.FlowBuffer > 10000 {
		add("web.flow_buffer must be between 1 and 10000")
	}
	if w.CaptureBytes < 0 {
		add("web.capture_bytes must not be negative")
	}
	if !validHostName(w.MagicHost) {
		add("web.magic_host %q is not a host name", w.MagicHost)
	}

	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		add("log.level %q must be debug, info, warn or error", c.Log.Level)
	}
	switch c.Log.Format {
	case "text", "json":
	default:
		add("log.format %q must be text or json", c.Log.Format)
	}
	return errors.Join(errs...)
}

// Redacted returns a copy that is safe to print.
func (c *Config) Redacted() *Config {
	cp := *c
	if cp.Auth.Pass != "" {
		cp.Auth.Pass = "<redacted>"
	}
	return &cp
}

// YAML renders the configuration.
func (c *Config) YAML() ([]byte, error) { return yaml.Marshal(c) }

// validHostName accepts what can be written after http://: letters, digits,
// hyphens and dots, with no port and no empty label. The empty name turns the
// magic host off.
func validHostName(h string) bool {
	if h == "" {
		return true
	}
	if len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}
