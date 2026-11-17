package server_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/auth"
	"github.com/goastian/midopress/internal/config"
	"github.com/goastian/midopress/internal/quota"
)

func newQuotaHarness(t *testing.T, limits quota.Limits, options ...harnessOption) (*harness, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "devices.json")
	store, err := auth.OpenStore(path, true)
	require.NoError(t, err)
	token, err := store.AddWithQuotas("phone", "balanced", &limits)
	require.NoError(t, err)
	base := []harnessOption{withAdmin(), withConfig(func(c *config.Config) {
		c.Auth.Mode = "token"
		c.Auth.DevicesFile = path
		c.Auth.Quotas.Window = time.Hour
		c.Auth.Quotas.Defaults = quota.Limits{Requests: 50, Bytes: 1 << 20, Concurrency: 2}
	})}
	h := newHarness(t, append(base, options...)...)
	return h, token
}

func deviceUsage(t *testing.T, h *harness, id string) quota.Usage {
	t.Helper()
	var devices []struct {
		ID    string
		Quota quota.Usage
	}
	h.admin(t).into("GET", "/api/v1/devices", nil, 200, &devices)
	for _, device := range devices {
		if device.ID == id {
			return device.Quota
		}
	}
	t.Fatalf("missing fixture device %s", id)
	return quota.Usage{}
}

func quotaAuthorization(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("phone:"+token))
}

func TestQuotaHTTPRequestsPersistAcrossIsolatedServerRestart(t *testing.T) {
	h, token := newQuotaHarness(t, quota.Limits{Requests: 2, Concurrency: 1})
	for range 2 {
		resp, _ := h.get(h.viaUser("phone", token), h.plainURL("/status/204"))
		require.Equal(t, 204, resp.StatusCode)
	}
	assert.Equal(t, uint64(2), deviceUsage(t, h, "phone").Requests)
	h.stop()
	h.start(t.TempDir())
	resp, _ := h.get(h.viaUser("phone", token), h.plainURL("/status/204"))
	assert.Equal(t, 429, resp.StatusCode)
	assert.Equal(t, "requests", resp.Header.Get("X-MidoPress-Quota"))
	assert.NotEmpty(t, resp.Header.Get("Retry-After"))
	assert.Equal(t, uint64(2), deviceUsage(t, h, "phone").Requests)
	assert.Zero(t, deviceUsage(t, h, "phone").Active)
}

func TestQuotaMITMConcurrencyOneDoesNotDeadlock(t *testing.T) {
	h, token := newQuotaHarness(t, quota.Limits{Requests: 3, Concurrency: 1})
	c := h.viaUser("phone", token)
	c.Timeout = 5 * time.Second
	for range 2 {
		resp, _ := h.get(c, h.secureURL("/status/204"))
		require.Equal(t, 204, resp.StatusCode)
	}
	resp, _ := h.get(c, h.secureURL("/status/204"))
	assert.Equal(t, 429, resp.StatusCode)
	assert.Equal(t, "requests", resp.Header.Get("X-MidoPress-Quota"))
	usage := deviceUsage(t, h, "phone")
	assert.Equal(t, uint64(3), usage.Requests, "one CONNECT plus two HTTP requests inside it")
	assert.Equal(t, 1, usage.Tunnels)
	assert.Zero(t, usage.Active)
}

func TestQuotaConcurrentHTTPStreamRejectsAndReleasesSlot(t *testing.T) {
	h, token := newQuotaHarness(t, quota.Limits{Concurrency: 1})
	c := h.viaUser("phone", token)
	c.Timeout = 5 * time.Second
	resp, err := c.Get(h.plainURL("/stream"))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	_, err = io.ReadFull(resp.Body, make([]byte, 20))
	require.NoError(t, err)
	blocked, _ := h.get(h.viaUser("phone", token), h.plainURL("/status/204"))
	assert.Equal(t, 429, blocked.StatusCode)
	assert.Equal(t, "concurrency", blocked.Header.Get("X-MidoPress-Quota"))
	require.NoError(t, resp.Body.Close())
	require.Eventually(t, func() bool { return deviceUsage(t, h, "phone").Active == 0 }, 2*time.Second, 10*time.Millisecond)
	next, _ := h.get(h.viaUser("phone", token), h.plainURL("/status/204"))
	assert.Equal(t, 204, next.StatusCode)
}

func TestQuotaHTTPBytesCapUploadDownloadAndUpgradeBodies(t *testing.T) {
	for _, mode := range []string{"download", "upload", "upgrade-upload"} {
		t.Run(mode, func(t *testing.T) {
			h, token := newQuotaHarness(t, quota.Limits{Bytes: 64, Concurrency: 1})
			observed := make(chan int, 1)
			if mode != "download" {
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					data, _ := io.ReadAll(io.LimitReader(r.Body, 1024))
					observed <- len(data)
					w.Header().Set("Content-Type", "application/octet-stream")
					_, _ = io.WriteString(w, "ok")
				}))
				defer origin.Close()
				h.routes.Store("example.com:80", origin.Listener.Addr().String())
			}
			c := h.viaUser("phone", token)
			c.Timeout = 5 * time.Second
			method, target := http.MethodGet, h.plainURL("/binary")
			var body io.Reader
			if mode != "download" {
				method, target = http.MethodPost, h.plainURL("/echo")
				body = bytes.NewReader(bytes.Repeat([]byte("x"), 512))
			}
			req, err := http.NewRequest(method, target, body)
			require.NoError(t, err)
			if mode == "upgrade-upload" {
				req.Header.Set("Connection", "Upgrade")
				req.Header.Set("Upgrade", "fixture")
			}
			resp, err := c.Do(req)
			if err == nil {
				data, readErr := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if mode == "download" {
					assert.LessOrEqual(t, len(data), 64)
					assert.Error(t, readErr)
				} else {
					assert.Equal(t, 429, resp.StatusCode)
					assert.Equal(t, "bytes", resp.Header.Get("X-MidoPress-Quota"))
				}
			}
			if mode != "download" {
				select {
				case n := <-observed:
					assert.LessOrEqual(t, n, 64, "upgrade cannot bypass upload quota even if its response is tiny")
				case <-time.After(100 * time.Millisecond):
				}
			}
			require.Eventually(t, func() bool { return deviceUsage(t, h, "phone").Bytes == 64 }, 2*time.Second, 10*time.Millisecond)
			assert.Zero(t, deviceUsage(t, h, "phone").Active)
			next, _ := h.get(c, h.plainURL("/status/204"))
			assert.Equal(t, 429, next.StatusCode)
			assert.Equal(t, "bytes", next.Header.Get("X-MidoPress-Quota"))
		})
	}
}

func quotaConnect(t *testing.T, h *harness, token, target string) (net.Conn, *bufio.Reader, *http.Response) {
	t.Helper()
	conn := h.rawConn()
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	_, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n", target, target, quotaAuthorization(token))
	require.NoError(t, err)
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	require.NoError(t, err)
	return conn, reader, resp
}

func TestQuotaOpaqueConnectGreetingConcurrencyAndByteLimit(t *testing.T) {
	h, token := newQuotaHarness(t, quota.Limits{Bytes: 16, Concurrency: 1}, withoutMITM())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = conn.Write([]byte("hello"))
				data, _ := io.ReadAll(conn)
				_, _ = conn.Write(bytes.ToUpper(data))
			}()
		}
	}()
	h.routes.Store("quota.test:7000", ln.Addr().String())
	conn, reader, resp := quotaConnect(t, h, token, "quota.test:7000")
	defer func() { _ = conn.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	greeting := make([]byte, 5)
	_, err = io.ReadFull(reader, greeting)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(greeting))
	second, _, blocked := quotaConnect(t, h, token, "quota.test:7000")
	_ = second.Close()
	assert.Equal(t, 429, blocked.StatusCode)
	assert.Equal(t, "concurrency", blocked.Header.Get("X-MidoPress-Quota"))
	_, err = conn.Write(bytes.Repeat([]byte("x"), 32))
	require.NoError(t, err)
	_, _ = io.ReadAll(reader)
	require.Eventually(t, func() bool { return deviceUsage(t, h, "phone").Tunnels == 0 }, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, uint64(16), deviceUsage(t, h, "phone").Bytes)
}

func TestQuotaWebSocketMetersBothDirectionsAndClosesAtLimit(t *testing.T) {
	h, token := newQuotaHarness(t, quota.Limits{Bytes: 32, Concurrency: 1})
	conn := h.rawConn()
	defer func() { _ = conn.Close() }()
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	_, err := fmt.Fprintf(conn, "GET http://example.com/ws HTTP/1.1\r\nHost: example.com\r\nProxy-Authorization: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: fixture-key\r\nSec-WebSocket-Version: 13\r\n\r\n", quotaAuthorization(token))
	require.NoError(t, err)
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	require.NoError(t, err)
	require.Equal(t, 101, resp.StatusCode)
	payload := bytes.Repeat([]byte("a"), 20)
	_, err = conn.Write(payload)
	require.NoError(t, err)
	got, _ := io.ReadAll(reader)
	assert.Equal(t, bytes.Repeat([]byte("A"), 12), got)
	require.Eventually(t, func() bool { return deviceUsage(t, h, "phone").Active == 0 }, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, uint64(32), deviceUsage(t, h, "phone").Bytes)
}

type quotaWireCounter struct {
	net.Conn
	mu                      sync.Mutex
	readHeader, writeHeader bool
	readTail, writeTail     uint32
	accepted                atomic.Uint64
}

func (c *quotaWireCounter) count(b []byte, reading bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	done, tail := &c.writeHeader, &c.writeTail
	if reading {
		done, tail = &c.readHeader, &c.readTail
	}
	for i, value := range b {
		if *done {
			c.accepted.Add(uint64(len(b) - i))
			break
		}
		*tail = *tail<<8 | uint32(value)
		if *tail == 0x0d0a0d0a {
			*done = true
		}
	}
}
func (c *quotaWireCounter) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.count(b[:n], true)
	return n, err
}
func (c *quotaWireCounter) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.count(b[:n], false)
	return n, err
}

func TestQuotaMITMCountsEncryptedClientEdgeOnce(t *testing.T) {
	h, token := newQuotaHarness(t, quota.Limits{Bytes: 1 << 20, Concurrency: 1})
	var counter atomic.Pointer[quotaWireCounter]
	c := h.client(func(tr *http.Transport) {
		tr.Proxy = http.ProxyURL(&url.URL{Scheme: "http", Host: h.proxyAddr, User: url.UserPassword("phone", token)})
		tr.ForceAttemptHTTP2 = false
		tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := new(net.Dialer).DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			wire := &quotaWireCounter{Conn: conn}
			counter.Store(wire)
			return wire, nil
		}
	})
	resp, body := h.get(c, h.secureURL("/binary"))
	require.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, fix().noise, body)
	wire := counter.Load()
	require.NotNil(t, wire)
	require.Eventually(t, func() bool { return deviceUsage(t, h, "phone").Bytes == wire.accepted.Load() }, 2*time.Second, 10*time.Millisecond, "TLS edge bytes must not be charged a second time as plaintext response bytes")
	assert.Greater(t, wire.accepted.Load(), uint64(len(body)), "TLS handshake and records are part of the edge budget")
}

func assertRegistrationLimited(t *testing.T, a *adminAPI, path, id, mode string) {
	t.Helper()
	window := func() int64 {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		var file struct{ Registration auth.Registration }
		require.NoError(t, json.Unmarshal(data, &file))
		return file.Registration.Window
	}
	before := window()
	response, _ := a.call("POST", "/api/v1/devices", obj{"id": id, "profile": "balanced"})
	if mode == "rate" && response.StatusCode == 201 {
		assert.Greater(t, window(), before, "registration is only allowed after the actual rate window rolls over")
		response, _ = a.call("POST", "/api/v1/devices", obj{"id": id + "-blocked", "profile": "balanced"})
	}
	assert.Equal(t, 429, response.StatusCode)
	assert.NotEmpty(t, response.Header.Get("Retry-After"))
}

func TestQuotaOverridesAdminSeparationAndRegistrationLimits(t *testing.T) {
	t.Run("per-device patch and null reset apply immediately", func(t *testing.T) {
		h, token := newQuotaHarness(t, quota.Limits{Requests: 1, Concurrency: 1})
		a := h.admin(t)
		created := a.obj("POST", "/api/v1/devices", obj{"id": "tablet", "profile": "balanced", "quotas": obj{"requests": 1, "bytes": 0, "concurrency": 1}}, 201)
		tabletToken, ok := created["token"].(string)
		require.True(t, ok)
		resp, _ := h.get(h.viaUser("tablet", tabletToken), h.plainURL("/status/204"))
		require.Equal(t, 204, resp.StatusCode)
		resp, _ = h.get(h.viaUser("tablet", tabletToken), h.plainURL("/status/204"))
		require.Equal(t, 429, resp.StatusCode)
		c := h.viaUser("phone", token)
		h.get(c, h.plainURL("/status/204"))
		resp, _ = h.get(c, h.plainURL("/status/204"))
		require.Equal(t, 429, resp.StatusCode)
		a.obj("PATCH", "/api/v1/devices/phone", obj{"quotas": obj{"requests": 2, "bytes": 0, "concurrency": 1}}, 200)
		resp, _ = h.get(c, h.plainURL("/status/204"))
		assert.Equal(t, 204, resp.StatusCode)
		a.obj("PATCH", "/api/v1/devices/phone", obj{"quotas": nil}, 200)
		resp, _ = h.get(c, h.plainURL("/status/204"))
		assert.Equal(t, 204, resp.StatusCode)
		assert.Equal(t, uint64(50), deviceUsage(t, h, "phone").Limits.Requests)
		response, _ := a.do("GET", "/api/v1/about", nil, token)
		assert.Equal(t, 401, response.StatusCode)
	})
	for _, mode := range []string{"capacity", "rate"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "devices.json")
			h := newHarness(t, withAdmin(), withConfig(func(c *config.Config) {
				c.Auth.Mode = "token"
				c.Auth.DevicesFile = path
				c.Auth.MaxDevices = 1
				c.Auth.RegistrationsPerMinute = 100
				if mode == "rate" {
					c.Auth.MaxDevices = 10
					c.Auth.RegistrationsPerMinute = 1
				}
			}))
			a := h.admin(t)
			a.obj("POST", "/api/v1/devices", obj{"id": "one", "profile": "balanced"}, 201)
			assertRegistrationLimited(t, a, path, "two", mode)
			h.stop()
			h.start(t.TempDir())
			a = h.admin(t)
			assertRegistrationLimited(t, a, path, "three", mode)
		})
	}
	t.Run("manually aliased administrator token never authenticates to proxy", func(t *testing.T) {
		path, state := filepath.Join(t.TempDir(), "devices.json"), t.TempDir()
		adminToken := strings.Repeat("a", 64)
		digest := sha256.Sum256([]byte(adminToken))
		data, err := json.Marshal(map[string]any{"devices": []auth.Device{{ID: "phone", TokenSHA256: hex.EncodeToString(digest[:])}}})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, data, 0600))
		require.NoError(t, os.WriteFile(filepath.Join(state, "admin.token"), []byte(adminToken+"\n"), 0600))
		h := newHarness(t, withAdmin(), withConfig(func(c *config.Config) { c.Auth.Mode = "token"; c.Auth.DevicesFile = path; c.Web.StateDir = state }))
		response, _ := h.get(h.viaUser("phone", adminToken), h.plainURL("/status/204"))
		assert.Equal(t, 407, response.StatusCode)
		req, err := http.NewRequest(http.MethodGet, h.plainURL("/status/204"), nil)
		require.NoError(t, err)
		req.Header.Set("Proxy-Authorization", "Bearer "+adminToken)
		response, err = h.client().Do(req)
		require.NoError(t, err)
		_ = response.Body.Close()
		assert.Equal(t, 407, response.StatusCode)
		response, _ = h.admin(t).call("GET", "/api/v1/about", nil)
		assert.Equal(t, 200, response.StatusCode)
	})
}
