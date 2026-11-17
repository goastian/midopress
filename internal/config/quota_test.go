package config

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestQuotaConfigurationAndEnvironment(t *testing.T) {
	c := Default()
	require.NoError(t, ApplyEnv(c, []string{"MIDOPRESS_AUTH_QUOTAS_DEFAULTS_REQUESTS=10", "MIDOPRESS_AUTH_QUOTAS_DEFAULTS_BYTES=1024", "MIDOPRESS_AUTH_QUOTAS_DEFAULTS_CONCURRENCY=2", "MIDOPRESS_AUTH_MAX_DEVICES=8"}))
	assert.Equal(t, uint64(10), c.Auth.Quotas.Defaults.Requests)
	assert.Equal(t, uint64(1024), c.Auth.Quotas.Defaults.Bytes)
	assert.Equal(t, 2, c.Auth.Quotas.Defaults.Concurrency)
	assert.Equal(t, 8, c.Auth.MaxDevices)
	require.NoError(t, c.Validate())
	c.Auth.Quotas.Defaults.Concurrency = -1
	assert.Error(t, c.Validate())
	c = Default()
	c.Auth.Mode = "token"
	c.Auth.DevicesFile = "/tmp/device-fixture.json"
	c.Auth.Quotas.File = c.Auth.DevicesFile
	assert.Error(t, c.Validate())
}
