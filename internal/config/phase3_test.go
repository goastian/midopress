package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goastian/midopress/internal/img"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/rules"
)

func TestLoadRulesProfilesAndCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
pipeline:
  profile: mobile
  image: {metric: ssimulacra2}
cache:
  dir: /var/cache/midopress
  max_size: 2GiB
  min_ttl: 30s
  default_ttl: 10m
  max_ttl: 24h
profiles:
  mobile:
    base: saver
    target_dssim: 0.01
    formats: [webp]
rules:
  - match: '~d (bank|paypal)\.'
    action: passthrough_tls
  - match: '~d cdn\.example\.com & ~t image/'
    profile: mobile
  - match: '~u /app\.js$'
    set: {minify_js: true, quality: 70}
metrics: {addr: "127.0.0.1:9100"}
`), 0o600))
	cfg, err := Load(path, nil)
	require.NoError(t, err)
	require.NoError(t, cfg.Validate())
	assert.Equal(t, "mobile", cfg.Pipeline.Profile)
	assert.Equal(t, ByteSize(2<<30), cfg.Cache.MaxSize)
	assert.Equal(t, 30*time.Second, cfg.Cache.MinTTL)
	assert.Len(t, cfg.Rules, 3)
	assert.Equal(t, rules.ActionPassthroughTLS, cfg.Rules[0].Action)
	assert.Equal(t, 70, cfg.Rules[2].Set["quality"])
	assert.Equal(t, "saver", cfg.Profiles["mobile"].Base)
	assert.Equal(t, "127.0.0.1:9100", cfg.Metrics.Addr)
}

func TestValidatePhase3(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"bad rule", func(c *Config) { c.Rules = []rules.Rule{{Match: "~z foo", Action: rules.ActionPassthrough}} }, "rules"},
		{"rule with an unknown profile", func(c *Config) { c.Rules = []rules.Rule{{Match: "~all", Profile: "ghost"}} }, "rules"},
		{"passthrough_tls needs a host rule", func(c *Config) {
			c.Rules = []rules.Rule{{Match: "~u /x", Action: rules.ActionPassthroughTLS}}
		}, "rules"},
		{"unknown set key", func(c *Config) { c.Rules = []rules.Rule{{Match: "~all", Set: map[string]any{"colour": 1}}} }, "unknown key"},
		{"set quality range", func(c *Config) { c.Rules = []rules.Rule{{Match: "~all", Set: map[string]any{"quality": 500}}} }, "quality"},
		{"set mode", func(c *Config) { c.Rules = []rules.Rule{{Match: "~all", Set: map[string]any{"minify_js": "maybe"}}} }, "minify_js"},
		{"unknown default profile", func(c *Config) { c.Pipeline.Profile = "ghost" }, "pipeline.profile"},
		{"profile base", func(c *Config) { c.Profiles["x"] = profileBase("ghost") }, "unknown base"},
		{"metric", func(c *Config) { c.Pipeline.Image.Metric = "psnr" }, "metric"},
		{"backend", func(c *Config) { c.Pipeline.Image.Backend = "gimp" }, "backend"},
		{"ttl order", func(c *Config) { c.Cache.MinTTL = 2 * time.Hour }, "TTL"},
		{"disk without size", func(c *Config) { c.Cache.Dir = "/tmp/x"; c.Cache.MaxSize = 0 }, "cache.max_size"},
		{"mitm pattern", func(c *Config) { c.MITM.Passthrough = []string{"~d ("} }, "mitm.passthrough"},
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

func TestEveryProblemIsReportedTogether(t *testing.T) {
	cfg := Default()
	cfg.Rules = []rules.Rule{{Match: "~z", Action: rules.ActionPassthrough}, {Match: "~d (", Action: rules.ActionPassthrough}}
	cfg.Pipeline.Image.Metric = "psnr"
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rules[0]")
	assert.Contains(t, err.Error(), "rules[1]")
	assert.Contains(t, err.Error(), "metric")
}

func TestMinifyEnvAcceptsABool(t *testing.T) {
	cfg, err := Load("", []string{"MIDOPRESS_PIPELINE_TEXT_MINIFY=true", "MIDOPRESS_PIPELINE_TEXT_MINIFY_JS=aggressive"})
	require.NoError(t, err)
	assert.Equal(t, "safe", cfg.Pipeline.Text.Minify.HTML)
	assert.Equal(t, "aggressive", cfg.Pipeline.Text.Minify.JS, "a specific setting applies after the whole switch")
}

func profileBase(base string) img.ProfileOverride { return img.ProfileOverride{Base: base} }
