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
	"time"

	"go.yaml.in/yaml/v3"
)

// Config is the whole configuration.
type Config struct {
	Listen   Listen   `yaml:"listen"`
	MITM     MITM     `yaml:"mitm"`
	Auth     Auth     `yaml:"auth"`
	Upstream Upstream `yaml:"upstream"`
	Server   Server   `yaml:"server"`
	Pipeline Pipeline `yaml:"pipeline"`
	Log      Log      `yaml:"log"`
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
	MemoryBudget            ByteSize      `yaml:"memory_budget"`
	Text                    Text          `yaml:"text"`
	Image                   Image         `yaml:"image"`
}

// Text configures the compression of text like content.
type Text struct {
	Brotli     int      `yaml:"brotli"`
	Gzip       int      `yaml:"gzip"`
	Zstd       int      `yaml:"zstd"`
	Minify     bool     `yaml:"minify"`
	Recompress bool     `yaml:"recompress"`
	BufferMax  ByteSize `yaml:"buffer_max"`
	MinBytes   ByteSize `yaml:"min_bytes"`
	MaxDecoded ByteSize `yaml:"max_decoded"`
}

// Image configures image recoding.
type Image struct {
	JPEGQuality int           `yaml:"jpeg_quality"`
	PNG         bool          `yaml:"png"`
	GIF         bool          `yaml:"gif"`
	MinBytes    ByteSize      `yaml:"min_bytes"`
	MaxBytes    ByteSize      `yaml:"max_bytes"`
	MaxPixels   int64         `yaml:"max_pixels"`
	Timeout     time.Duration `yaml:"timeout"`
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
			MemoryBudget:  256 << 20,
			Text: Text{
				Brotli:     6,
				Gzip:       6,
				Zstd:       3,
				Recompress: true,
				BufferMax:  1 << 20,
				MinBytes:   256,
				MaxDecoded: 32 << 20,
			},
			Image: Image{
				JPEGQuality: 50,
				PNG:         true,
				GIF:         true,
				MinBytes:    1024,
				MaxBytes:    8 << 20,
				MaxPixels:   16_000_000,
				Timeout:     3 * time.Second,
			},
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

	positive := map[string]time.Duration{
		"upstream.dial_timeout":            c.Upstream.DialTimeout,
		"upstream.tls_handshake_timeout":   c.Upstream.TLSHandshakeTimeout,
		"upstream.response_header_timeout": c.Upstream.ResponseHeaderTimeout,
		"upstream.idle_conn_timeout":       c.Upstream.IdleConnTimeout,
		"server.read_header_timeout":       c.Server.ReadHeaderTimeout,
		"server.idle_timeout":              c.Server.IdleTimeout,
		"server.tls_handshake_timeout":     c.Server.TLSHandshakeTimeout,
		"server.shutdown_grace":            c.Server.ShutdownGrace,
		"pipeline.transcode_wait":          c.Pipeline.TranscodeWait,
		"pipeline.image.timeout":           c.Pipeline.Image.Timeout,
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
	if p.MemoryBudget < 1<<20 {
		add("pipeline.memory_budget must be at least 1MiB")
	}
	if p.Text.Brotli < 0 || p.Text.Brotli > 11 {
		add("pipeline.text.brotli must be between 0 and 11")
	}
	if p.Text.Gzip < 0 || p.Text.Gzip > 9 {
		add("pipeline.text.gzip must be between 0 and 9")
	}
	if p.Text.Zstd < 1 || p.Text.Zstd > 22 {
		add("pipeline.text.zstd must be between 1 and 22")
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
