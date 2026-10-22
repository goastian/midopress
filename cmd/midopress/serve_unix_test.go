//go:build !windows

package main

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncBuffer lets the test read the log while the server writes to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestServe starts the real command with compy's original flags, uses the
// proxy and stops it with a signal.
func TestServe(t *testing.T) {
	origin := httpServer(t)

	var stderr syncBuffer
	done := make(chan int, 1)
	state := t.TempDir()
	env := []string{"MIDOPRESS_WEB_ADDR=127.0.0.1:0", "MIDOPRESS_WEB_STATE_DIR=" + state}
	go func() {
		done <- run([]string{"-host", "127.0.0.1:0", "-user", "me", "-pass", "secret", "-jpeg", "0", "-log-format", "json"},
			io.Discard, &stderr, env)
	}()

	var addr string
	require.Eventually(t, func() bool {
		m := regexp.MustCompile(`"addr":"([^"]+)"`).FindStringSubmatch(stderr.String())
		if m == nil {
			return false
		}
		addr = m[1]
		return true
	}, 5*time.Second, 20*time.Millisecond, "the server must log its address: %s", stderr.String())
	assert.Contains(t, stderr.String(), `"auth":"basic"`)
	assert.Contains(t, stderr.String(), "credentials travel unencrypted", "basic auth without TLS must warn")

	// the administration interface: its address and token are announced once
	m := regexp.MustCompile(`http://(127\.0\.0\.1:\d+)/#/login\?token=([0-9a-f]{64})`).FindStringSubmatch(stderr.String())
	require.NotNil(t, m, "the admin address and token must be shown: %s", stderr.String())
	adminReq, _ := http.NewRequest(http.MethodGet, "http://"+m[1]+"/api/v1/about", nil)
	adminResp, err := http.DefaultClient.Do(adminReq)
	require.NoError(t, err)
	_ = adminResp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, adminResp.StatusCode, "no token, no way in")
	adminReq.Header.Set("Authorization", "Bearer "+m[2])
	adminResp, err = http.DefaultClient.Do(adminReq)
	require.NoError(t, err)
	_ = adminResp.Body.Close()
	assert.Equal(t, http.StatusOK, adminResp.StatusCode)
	assert.NotContains(t, stderr.String(), m[2]+`"`, "the token is not in the log")
	assert.FileExists(t, filepath.Join(state, "admin.token"))

	proxy := func(user *url.Userinfo) *http.Client {
		u := &url.URL{Scheme: "http", Host: addr, User: user}
		return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u), DisableCompression: true}, Timeout: 5 * time.Second}
	}
	resp, err := proxy(nil).Get(origin)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusProxyAuthRequired, resp.StatusCode, "-user and -pass turn on authentication")

	req, _ := http.NewRequest(http.MethodGet, origin, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err = proxy(url.UserPassword("me", "secret")).Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))
	assert.Less(t, len(body), 2000)

	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGINT))
	select {
	case code := <-done:
		assert.Equal(t, 0, code)
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not stop on SIGINT")
	}
	assert.Contains(t, stderr.String(), "midopress exiting")
}
