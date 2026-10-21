package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultsAreValid(t *testing.T) {
	assert.NoError(t, Default().Validate())
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "midopress.yaml")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func TestLoadFile(t *testing.T) {
	p := writeFile(t, `
listen:
  addr: "127.0.0.1:8080"
  tls: {cert: c.pem, key: k.pem}
mitm:
  ca_cert: ca.pem
  ca_key: ca.key
  leaf_ttl: 12h
  passthrough: ["~d \\.bank$", "*.example.org"]
auth:
  mode: token
  devices_file: devices.json
pipeline:
  memory_budget: 512MiB
  text: {brotli: 9, minify: true, buffer_max: 2MiB}
  image: {jpeg_quality: 70, max_pixels: 8000000, timeout: 5s}
log: {level: debug, format: json, urls: true}
`)
	cfg, err := Load(p, nil)
	require.NoError(t, err)

	assert.Equal(t, "127.0.0.1:8080", cfg.Listen.Addr)
	assert.Equal(t, TLS{Cert: "c.pem", Key: "k.pem"}, cfg.Listen.TLS)
	assert.True(t, cfg.MITM.Enabled())
	assert.Equal(t, 12*time.Hour, cfg.MITM.LeafTTL)
	assert.Equal(t, []string{`~d \.bank$`, "*.example.org"}, cfg.MITM.Passthrough)
	assert.True(t, cfg.MITM.DefaultPassthrough, "untouched defaults survive")
	assert.Equal(t, "token", cfg.Auth.Mode)
	assert.Equal(t, ByteSize(512<<20), cfg.Pipeline.MemoryBudget)
	assert.Equal(t, 9, cfg.Pipeline.Text.Brotli)
	assert.Equal(t, 6, cfg.Pipeline.Text.Gzip)
	assert.Equal(t, ByteSize(2<<20), cfg.Pipeline.Text.BufferMax)
	assert.Equal(t, 70, cfg.Pipeline.Image.JPEGQuality)
	assert.Equal(t, 5*time.Second, cfg.Pipeline.Image.Timeout)
	assert.True(t, cfg.Log.URLs)
	assert.NoError(t, cfg.Validate())
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	_, err := Load(writeFile(t, "pipeline:\n  text:\n    brotlli: 9\n"), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "brotlli")
}

func TestLoadErrors(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"), nil)
	assert.Error(t, err)
	_, err = Load(writeFile(t, "listen: [unclosed"), nil)
	assert.Error(t, err)
	_, err = Load(writeFile(t, "pipeline:\n  memory_budget: lots\n"), nil)
	assert.Error(t, err)
	_, err = Load(writeFile(t, "upstream:\n  dial_timeout: soon\n"), nil)
	assert.Error(t, err)
}

func TestLoadEmptyFileKeepsDefaults(t *testing.T) {
	cfg, err := Load(writeFile(t, ""), nil)
	require.NoError(t, err)
	assert.Equal(t, Default(), cfg)
}

func TestEnvOverridesFile(t *testing.T) {
	p := writeFile(t, "listen:\n  addr: \":1111\"\npipeline:\n  text:\n    brotli: 3\n")
	cfg, err := Load(p, []string{
		"MIDOPRESS_LISTEN_ADDR=:2222",
		"MIDOPRESS_PIPELINE_TEXT_BROTLI=11",
		"MIDOPRESS_PIPELINE_TEXT_MINIFY=true",
		"MIDOPRESS_PIPELINE_MEMORY_BUDGET=1GiB",
		"MIDOPRESS_PIPELINE_MIN_GAIN=0.1",
		"MIDOPRESS_AUTH_BLOCK_GLOBAL=false",
		"MIDOPRESS_UPSTREAM_DIAL_TIMEOUT=3s",
		"MIDOPRESS_MITM_PASSTHROUGH=a.example, ~d b\\.example$ ,",
		"MIDOPRESS_PIPELINE_IMAGE_MAX_PIXELS=1000",
		"MIDOPRESS_DATA=/not/a/setting",
		"HOME=/root",
	})
	require.NoError(t, err)
	assert.Equal(t, ":2222", cfg.Listen.Addr)
	assert.Equal(t, 11, cfg.Pipeline.Text.Brotli)
	assert.True(t, cfg.Pipeline.Text.Minify)
	assert.Equal(t, ByteSize(1<<30), cfg.Pipeline.MemoryBudget)
	assert.InDelta(t, 0.1, cfg.Pipeline.MinGain, 1e-9)
	assert.False(t, cfg.Auth.BlockGlobal)
	assert.Equal(t, 3*time.Second, cfg.Upstream.DialTimeout)
	assert.Equal(t, []string{"a.example", `~d b\.example$`}, cfg.MITM.Passthrough)
	assert.Equal(t, int64(1000), cfg.Pipeline.Image.MaxPixels)
}

func TestEnvErrors(t *testing.T) {
	_, err := Load("", []string{"MIDOPRESS_PIPELINE_TEXT_BROTLI=high", "MIDOPRESS_LOG_URLS=maybe"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MIDOPRESS_PIPELINE_TEXT_BROTLI")
	assert.Contains(t, err.Error(), "MIDOPRESS_LOG_URLS")
}

func TestEveryLeafHasAnEnvName(t *testing.T) {
	seen := map[string]bool{}
	walk(reflectValue(Default()), "MIDOPRESS", func(name string, _ reflectV) {
		assert.False(t, seen[name], "duplicate env name %s", name)
		seen[name] = true
	})
	for _, want := range []string{"MIDOPRESS_LISTEN_ADDR", "MIDOPRESS_LISTEN_TLS_CERT", "MIDOPRESS_PIPELINE_TEXT_BROTLI", "MIDOPRESS_AUTH_DEVICES_FILE"} {
		assert.True(t, seen[want], want)
	}
}

func TestParseByteSize(t *testing.T) {
	tests := map[string]ByteSize{
		"0": 0, "1024": 1024, "8MiB": 8 << 20, "8 mib": 8 << 20, "1.5KiB": 1536,
		"2KB": 2000, "3MB": 3_000_000, "1GiB": 1 << 30, "10b": 10,
	}
	for in, want := range tests {
		got, err := ParseByteSize(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "MiB", "-1", "1TB", "1 2", "abc"} {
		_, err := ParseByteSize(bad)
		assert.Error(t, err, bad)
	}
}

func TestByteSizeString(t *testing.T) {
	assert.Equal(t, "8MiB", ByteSize(8<<20).String())
	assert.Equal(t, "256MiB", ByteSize(256<<20).String())
	assert.Equal(t, "1GiB", ByteSize(1<<30).String())
	assert.Equal(t, "1536", ByteSize(1536).String()[:4])
	assert.Equal(t, "0", ByteSize(0).String())
}

func TestYAMLRoundTrip(t *testing.T) {
	out, err := Default().YAML()
	require.NoError(t, err)
	cfg, err := Load(writeFile(t, string(out)), nil)
	require.NoError(t, err)
	assert.Empty(t, cfg.MITM.Passthrough)
	cfg.MITM.Passthrough = nil
	assert.Equal(t, Default(), cfg)
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"bad listen", func(c *Config) { c.Listen.Addr = "nope" }, "listen.addr"},
		{"tls half set", func(c *Config) { c.Listen.TLS.Cert = "c" }, "set together"},
		{"ca half set", func(c *Config) { c.MITM.CACert = "c" }, "ca_cert and mitm.ca_key"},
		{"short leaf ttl", func(c *Config) { c.MITM.LeafTTL = time.Minute }, "leaf_ttl"},
		{"unknown auth mode", func(c *Config) { c.Auth.Mode = "oauth" }, "auth.mode"},
		{"basic without creds", func(c *Config) { c.Auth.Mode = "basic" }, "needs auth.user"},
		{"token without file", func(c *Config) { c.Auth.Mode = "token" }, "devices_file"},
		{"open proxy", func(c *Config) { c.Auth.BlockGlobal = false }, "open proxy"},
		{"brotli range", func(c *Config) { c.Pipeline.Text.Brotli = 12 }, "brotli"},
		{"gzip range", func(c *Config) { c.Pipeline.Text.Gzip = 10 }, "gzip"},
		{"zstd range", func(c *Config) { c.Pipeline.Text.Zstd = 0 }, "zstd"},
		{"jpeg range", func(c *Config) { c.Pipeline.Image.JPEGQuality = 101 }, "jpeg_quality"},
		{"min gain", func(c *Config) { c.Pipeline.MinGain = 0.95 }, "min_gain"},
		{"budget", func(c *Config) { c.Pipeline.MemoryBudget = 1000 }, "memory_budget"},
		{"decoded smaller than buffer", func(c *Config) { c.Pipeline.Text.MaxDecoded = 1024 }, "max_decoded"},
		{"zero timeout", func(c *Config) { c.Server.IdleTimeout = 0 }, "server.idle_timeout"},
		{"log level", func(c *Config) { c.Log.Level = "loud" }, "log.level"},
		{"log format", func(c *Config) { c.Log.Format = "xml" }, "log.format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(cfg)
			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	cfg := Default()
	cfg.Log.Level = "loud"
	cfg.Pipeline.Text.Brotli = 99
	cfg.Auth.Mode = "oauth"
	err := cfg.Validate()
	require.Error(t, err)
	for _, want := range []string{"log.level", "brotli", "auth.mode"} {
		assert.Contains(t, err.Error(), want)
	}
}

func TestRedacted(t *testing.T) {
	cfg := Default()
	cfg.Auth.Pass = "hunter2"
	out, err := cfg.Redacted().YAML()
	require.NoError(t, err)
	assert.NotContains(t, string(out), "hunter2")
	assert.Equal(t, "hunter2", cfg.Auth.Pass, "the original is untouched")
}

// The annotated example is documentation, so it must say what the defaults are.
func TestExampleFileMatchesDefaults(t *testing.T) {
	cfg, err := Load("../../examples/midopress.yaml", nil)
	require.NoError(t, err)
	assert.NoError(t, cfg.Validate())
	cfg.MITM.Passthrough = nil
	assert.Equal(t, Default(), cfg)
}
