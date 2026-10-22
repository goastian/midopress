package server_test

import (
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/auth"
	"github.com/goastian/midopress/internal/config"
	"github.com/goastian/midopress/internal/rules"
	"github.com/goastian/midopress/internal/server"
)

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestMetricsOnTheProxyPort(t *testing.T) {
	h := newHarness(t)
	h.get(h.client(), h.plainURL("/html"), "Accept-Encoding", "gzip")
	h.get(h.client(), h.plainURL("/image/jpeg"))

	resp, body := get(t, "http://"+h.proxyAddr+"/metrics")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "version=0.0.4")
	assert.Contains(t, body, "midopress_requests_total 2")
	assert.Contains(t, body, `midopress_responses_total{reason="transformed"}`)
	assert.Contains(t, body, "midopress_cache_hit_ratio")
	assert.Contains(t, body, "go_goroutines")
	assert.Contains(t, body, "midopress_build_info")
}

func TestMetricsCanBeOff(t *testing.T) {
	h := newHarness(t, withConfig(func(c *config.Config) { c.Metrics.Prometheus = false }))
	resp, _ := get(t, "http://"+h.proxyAddr+"/metrics")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestMetricsOnAListenerOfItsOwn(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	h := newHarness(t, withConfig(func(c *config.Config) { c.Metrics.Addr = addr }))
	h.get(h.client(), h.plainURL("/html"), "Accept-Encoding", "gzip")
	var body string
	require.Eventually(t, func() bool {
		resp, err := http.Get("http://" + addr + "/metrics")
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		body = string(b)
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 50*time.Millisecond)
	assert.Contains(t, body, "midopress_requests_total 1")

	resp, _ := get(t, "http://"+h.proxyAddr+"/metrics")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "not on the proxy port any more")
}

func TestRulesLeaveImagesAlone(t *testing.T) {
	f := fix()
	h := newHarness(t, withConfig(func(c *config.Config) {
		c.Rules = []rules.Rule{{Match: `~u /image/jpeg`, Action: rules.ActionPassthrough}}
	}))
	_, body := h.get(h.client(), h.plainURL("/image/jpeg"))
	assert.Equal(t, f.jpeg, body)
	_, body = h.get(h.client(), h.plainURL("/image/png"), "Accept", "image/webp")
	assert.Less(t, len(body), len(f.png), "other images are still recoded")
}

func TestRulesPassthroughTLSStopsInterception(t *testing.T) {
	h := newHarness(t, withConfig(func(c *config.Config) {
		c.Rules = []rules.Rule{{Match: `~d example\.com`, Action: rules.ActionPassthroughTLS}}
	}))
	origin := h.client(func(tr *http.Transport) { tr.TLSClientConfig.RootCAs = h.originPool })
	resp, body := h.get(origin, h.secureURL("/html"), "Accept-Encoding", "gzip")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, fix().html, body, "the origin's bytes: nobody touched them")
	st := h.srv.Proxy().Stats()
	assert.Zero(t, st.TLSHandshakes, "no connection was intercepted")
	assert.Positive(t, st.Tunnels)
}

func TestReloadAppliesRulesAndKeepsTheOldOnesOnError(t *testing.T) {
	f := fix()
	next := config.Default()
	loader := func() (*config.Config, error) { return next, nil }
	h := newHarness(t, withServerOption(server.WithConfigLoader(loader)))
	next.Pipeline.Text.Minify = h.cfg.Pipeline.Text.Minify
	next.MITM = h.cfg.MITM
	next.Pipeline.Image = h.cfg.Pipeline.Image

	_, body := h.get(h.client(), h.plainURL("/image/jpeg"))
	assert.Less(t, len(body), len(f.jpeg), "recoded before the reload")

	next.Rules = []rules.Rule{{Match: `~t image/`, Action: rules.ActionPassthrough}}
	require.NoError(t, h.srv.Reload())
	_, body = h.get(h.client(), h.plainURL("/image/jpeg"))
	assert.Equal(t, f.jpeg, body, "the new rule applies without a restart")

	next.Rules = []rules.Rule{{Match: `~bogus`, Action: rules.ActionPassthrough}}
	require.Error(t, h.srv.Reload())
	_, body = h.get(h.client(), h.plainURL("/image/jpeg"))
	assert.Equal(t, f.jpeg, body, "a configuration that does not validate changes nothing")

	next.Rules = nil
	require.NoError(t, h.srv.Reload())
	_, body = h.get(h.client(), h.plainURL("/image/jpeg"))
	assert.Less(t, len(body), len(f.jpeg))
}

func TestDeviceProfilesReachTheImagePipeline(t *testing.T) {
	devices := filepath.Join(t.TempDir(), "devices.json")
	store, err := auth.OpenStore(devices, true)
	require.NoError(t, err)
	phone, err := store.Add("phone", "extreme")
	require.NoError(t, err)
	laptop, err := store.Add("laptop", "balanced")
	require.NoError(t, err)

	h := newHarness(t, withConfig(func(c *config.Config) { c.Auth.Mode, c.Auth.DevicesFile = "token", devices }))
	hints := []string{"Sec-CH-Viewport-Width", "100", "Sec-CH-DPR", "1"}
	for _, url := range []string{h.plainURL("/image/jpeg"), h.secureURL("/image/jpeg")} {
		_, small := h.get(h.viaUser("phone", phone), url, hints...)
		_, big := h.get(h.viaUser("laptop", laptop), url, hints...)
		assert.Less(t, len(small), len(big)/2, "the phone is on the extreme profile, the laptop on balanced: %s", url)
	}
}
