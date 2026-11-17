package server_test

import (
	"crypto/tls"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImageAPITLSAndAdministrationAreSeparateWithoutMITM(t *testing.T) {
	h := newHarness(t, withoutMITM(), withAdmin(), func(h *harness) {
		h.cfg.ImageAPI.Enabled = true
		h.cfg.ImageAPI.Addr = "127.0.0.1:0"
		h.cfg.ImageAPI.PublicURL = "https://images.example.com"
		h.cfg.ImageAPI.TLS.Cert, h.cfg.ImageAPI.TLS.Key = h.makeProxyCert(t.TempDir())
	})
	require.NotNil(t, h.srv.ImageAddr())
	assert.NotEqual(t, h.srv.AdminAddr().String(), h.srv.ImageAddr().String())
	a := h.admin(t)
	resp, _ := a.do("POST", "/api/v1/images/sign", obj{"url": "https://example.com/a.png"}, "")
	assert.Equal(t, 401, resp.StatusCode)
	signed := a.obj("POST", "/api/v1/images/sign", obj{"url": "https://example.com/a.png", "width": 300, "dpr": 2}, 200)
	assert.Contains(t, signed["optimized_url"], "https://images.example.com/img?")
	assert.Contains(t, signed["original_url"], "mode=original")
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: h.proxyCertPool, MinVersion: tls.VersionTLS12}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport}
	for path, status := range map[string]int{"/img": 403, "/api/v1/about": 404, "/api/v1/images/sign": 404} {
		response, err := client.Get("https://" + h.srv.ImageAddr().String() + path)
		require.NoError(t, err)
		data, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		assert.Equal(t, status, response.StatusCode, string(data))
		require.NotNil(t, response.TLS)
	}
	assert.False(t, h.cfg.MITM.Enabled())
}
