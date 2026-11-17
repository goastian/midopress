package config

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImageAPIConfig(t *testing.T) {
	c := Default()
	c.ImageAPI.Enabled = true
	require.NoError(t, c.Validate())
	for _, change := range []func(*Config){
		func(c *Config) { c.ImageAPI.Addr = ":9997" },
		func(c *Config) { c.ImageAPI.Addr = c.Web.Addr },
		func(c *Config) { c.ImageAPI.PublicURL = "http://images.example.com" },
		func(c *Config) { c.ImageAPI.PublicURL = "https://images.example.com/img" },
		func(c *Config) { c.ImageAPI.MaxDPR = math.NaN() },
		func(c *Config) { c.ImageAPI.MaxPixels = 16_000_001 },
		func(c *Config) { c.ImageAPI.Timeout = 0 },
		func(c *Config) { c.ImageAPI.SignatureTTL = 2 * time.Hour },
		func(c *Config) { c.ImageAPI.TLS.Cert = "missing" },
	} {
		copy := *c
		change(&copy)
		assert.Error(t, copy.Validate())
	}
	c.ImageAPI.PublicURL = "https://images.example.com"
	require.NoError(t, c.Validate(), "TLS termination may sit in front of a loopback-only listener")
}
