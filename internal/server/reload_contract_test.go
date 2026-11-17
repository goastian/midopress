package server_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/config"
	"github.com/goastian/midopress/internal/server"
)

func TestE1ReloadRejectsUnappliedStaticOptions(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*config.Config)
	}{
		{"gain", func(c *config.Config) { c.Pipeline.MinGain = 0.15 }},
		{"metric", func(c *config.Config) { c.Pipeline.Image.Metric = "ssim" }},
		{"backend", func(c *config.Config) { c.Pipeline.Image.Backend = "vips" }},
		{"quality", func(c *config.Config) { c.Pipeline.Image.JPEGQuality = 80 }},
		{"cache-capacity", func(c *config.Config) { c.Cache.Memory += 1024 }},
		{"quota", func(c *config.Config) { c.Auth.Quotas.Defaults.Requests++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var next *config.Config
			h := newHarness(t, withAdmin(), withServerOption(server.WithConfigLoader(func() (*config.Config, error) { return next, nil })))
			copy := *h.cfg
			next = &copy
			a := h.admin(t)
			before := a.obj("GET", "/api/v1/config", nil, 200)
			test.change(next)
			err := h.srv.Reload()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "restart")
			assert.Equal(t, before, a.obj("GET", "/api/v1/config", nil, 200))
			original := *h.cfg
			next = &original
			require.NoError(t, h.srv.Reload())
		})
	}
}
