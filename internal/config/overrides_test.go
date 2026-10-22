package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/rules"
)

func TestOverridesRoundTripAndApply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "ui.yaml")
	dt := 0.004
	profile := "saver"
	profs := map[string]img.ProfileOverride{"mobile": {Base: "saver", TargetDSSIM: &dt}}
	rs := []rules.Rule{{Match: `~d bank\.`, Action: rules.ActionPassthroughTLS}, {Match: "~t image/", Set: map[string]any{"quality": 60}}}
	o := &Overrides{DefaultProfile: &profile, Profiles: &profs, Rules: &rs}
	require.NoError(t, SaveOverrides(path, o))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	got, err := LoadOverrides(path)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "saver", *got.DefaultProfile)
	assert.Equal(t, "saver", (*got.Profiles)["mobile"].Base)
	assert.Equal(t, 0.004, *(*got.Profiles)["mobile"].TargetDSSIM)
	assert.Equal(t, rules.ActionPassthroughTLS, (*got.Rules)[0].Action)
	assert.Equal(t, 60, (*got.Rules)[1].Set["quality"])

	cfg := Default()
	cfg.Pipeline.Profile = "extreme"
	cfg.Rules = []rules.Rule{{Match: "~all", Action: rules.ActionPassthrough}}
	cfg.ApplyOverrides(got)
	assert.Equal(t, "saver", cfg.Pipeline.Profile)
	assert.Len(t, cfg.Rules, 2)
	assert.Contains(t, cfg.Profiles, "mobile")
	assert.NoError(t, cfg.Validate())
}

func TestAbsentFieldsLeaveTheFileInCharge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ui.yaml")
	empty := []rules.Rule{}
	require.NoError(t, SaveOverrides(path, &Overrides{Rules: &empty}))
	got, err := LoadOverrides(path)
	require.NoError(t, err)
	require.NotNil(t, got.Rules, "an empty list is a choice: no rules at all")
	assert.Empty(t, *got.Rules)
	assert.Nil(t, got.DefaultProfile)
	assert.Nil(t, got.Profiles)

	cfg := Default()
	cfg.Pipeline.Profile = "extreme"
	cfg.Rules = []rules.Rule{{Match: "~all", Action: rules.ActionPassthrough}}
	cfg.ApplyOverrides(got)
	assert.Equal(t, "extreme", cfg.Pipeline.Profile)
	assert.Empty(t, cfg.Rules)
}

func TestNothingToKeepRemovesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ui.yaml")
	profile := "saver"
	require.NoError(t, SaveOverrides(path, &Overrides{DefaultProfile: &profile}))
	require.FileExists(t, path)
	require.NoError(t, SaveOverrides(path, &Overrides{}))
	assert.NoFileExists(t, path)
	require.NoError(t, SaveOverrides(path, nil), "removing what is not there is fine")

	got, err := LoadOverrides(path)
	require.NoError(t, err)
	assert.Nil(t, got)
	var nilOverrides *Overrides
	assert.True(t, nilOverrides.Empty())
	Default().ApplyOverrides(nil)
}

func TestBadOverridesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ui.yaml")
	require.NoError(t, os.WriteFile(path, []byte("colour: green\n"), 0o600))
	_, err := LoadOverrides(path)
	assert.ErrorContains(t, err, "colour")
	require.NoError(t, os.WriteFile(path, []byte("rules: [unclosed\n"), 0o600))
	_, err = LoadOverrides(path)
	assert.Error(t, err)
}

func TestWebSettings(t *testing.T) {
	w := Default().Web
	assert.True(t, w.Enabled)
	assert.Equal(t, "127.0.0.1:9998", w.Addr)
	assert.Equal(t, "midopress.it", w.MagicHost)
	assert.Equal(t, filepath.Join(w.StateDirPath(), "admin.token"), w.TokenPath())
	assert.Equal(t, filepath.Join(w.StateDirPath(), "ui.yaml"), w.UIPath())
	w.StateDir = "/var/lib/x"
	assert.Equal(t, "/var/lib/x/admin.token", w.TokenPath())
	w.AdminTokenFile = "/run/secret"
	assert.Equal(t, "/run/secret", w.TokenPath())

	for name, mutate := range map[string]func(*Config){
		"addr":    func(c *Config) { c.Web.Addr = " " },
		"buffer":  func(c *Config) { c.Web.FlowBuffer = 0 },
		"capture": func(c *Config) { c.Web.CaptureBytes = -1 },
		"host":    func(c *Config) { c.Web.MagicHost = "not a host" },
		"host2":   func(c *Config) { c.Web.MagicHost = "a..b" },
		"host3":   func(c *Config) { c.Web.MagicHost = "midopress.it:80" },
	} {
		cfg := Default()
		mutate(cfg)
		assert.Error(t, cfg.Validate(), name)
	}
	cfg := Default()
	cfg.Web.MagicHost = ""
	cfg.Web.Enabled = false
	cfg.Web.Addr = ""
	assert.NoError(t, cfg.Validate(), "off is off")
}
