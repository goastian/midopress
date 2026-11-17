package imageapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLimitedCredentialCanOnlySignItsScope(t *testing.T) {
	s := service(t)
	now := time.Now().Truncate(time.Second)
	s.now = func() time.Time { return now }
	token, grant, err := s.MintCredential(CredentialScope{Hosts: []string{"images.example.com", "*.cdn.example.com"},
		Profiles: []string{"balanced"}, MaxWidth: 800, MaxDPR: 2, TTLSeconds: 60})
	require.NoError(t, err)
	call := func(token string, spec Request, cookie bool) *httptest.ResponseRecorder {
		body, err := json.Marshal(spec)
		require.NoError(t, err)
		r := httptest.NewRequest(http.MethodPost, "https://images.example.com/img/sign", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		if cookie {
			r.Header.Set("Cookie", "session=private")
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	good := Request{URL: "https://images.example.com/photo.png", Profile: "balanced", Width: 600, DPR: 2}
	w := call(token, good, false)
	require.Equal(t, http.StatusOK, w.Code)
	var result struct {
		URL     string    `json:"optimized_url"`
		Expires time.Time `json:"expires"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	spec, err := s.authenticate(strings.SplitN(result.URL, "?", 2)[1])
	require.NoError(t, err)
	assert.Equal(t, good.Width, spec.Width)
	assert.Equal(t, good.DPR, spec.DPR)
	assert.LessOrEqual(t, result.Expires.Unix(), grant.Expires)
	for _, change := range []func(*Request){
		func(r *Request) { r.URL = "https://other.example.com/photo.png" },
		func(r *Request) { r.URL = "https://images.example.com.attacker.test/photo.png" },
		func(r *Request) { r.URL = "https://cdn.example.com/photo.png" },
		func(r *Request) { r.URL = "http://127.0.0.1/photo.png" },
		func(r *Request) { r.Width = 801 },
		func(r *Request) { r.Width = 0 },
		func(r *Request) { r.DPR = 3 },
		func(r *Request) { r.Profile = "extreme" },
	} {
		bad := good
		change(&bad)
		assert.Equal(t, http.StatusForbidden, call(token, bad, false).Code)
	}
	subdomain := good
	subdomain.URL = "https://a.cdn.example.com/photo.png"
	assert.Equal(t, http.StatusOK, call(token, subdomain, false).Code)
	assert.Equal(t, http.StatusConflict, call(token, good, true).Code)
	assert.Equal(t, http.StatusForbidden, call("", good, false).Code)
	parts := strings.Split(token, ".")
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	parts[0] = base64.RawURLEncoding.EncodeToString(bytes.Replace(raw, []byte("800"), []byte("999"), 1))
	assert.Equal(t, http.StatusForbidden, call(strings.Join(parts, "."), good, false).Code)
	assert.Empty(t, s.cache.Stats().MemoryItems)
	now = now.Add(61 * time.Second)
	assert.Equal(t, http.StatusForbidden, call(token, good, false).Code)
}

func TestCredentialScopesAndRateAreBounded(t *testing.T) {
	s := service(t)
	scope := CredentialScope{Hosts: []string{"images.example.com"}, Profiles: []string{"balanced"}, MaxWidth: 800, MaxDPR: 2, TTLSeconds: 300}
	for _, host := range []string{"localhost", "127.0.0.1", "169.254.169.254", "*.com", "*.co.uk", "https://images.example.com", "foo\r\nbar.com"} {
		bad := scope
		bad.Hosts = []string{host}
		_, _, err := s.MintCredential(bad)
		assert.Error(t, err, host)
	}
	_, grant, err := s.MintCredential(scope)
	require.NoError(t, err)
	for range 120 {
		require.True(t, s.credentialAllowed(grant))
	}
	assert.False(t, s.credentialAllowed(grant))
	request := httptest.NewRequest(http.MethodOptions, "https://images.example.com/img/sign", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, request)
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Contains(t, w.Header().Get("Access-Control-Allow-Headers"), "Authorization")
}

func TestConcurrentCredentialRequestsRespectSigningQuota(t *testing.T) {
	s := service(t)
	now := time.Now().Truncate(time.Second)
	s.now = func() time.Time { return now }
	token, _, err := s.MintCredential(CredentialScope{Hosts: []string{"images.example.com"}, Profiles: []string{"balanced"},
		MaxWidth: 800, MaxDPR: 2, TTLSeconds: 300})
	require.NoError(t, err)
	body := []byte(`{"url":"https://images.example.com/photo.png","profile":"balanced","width":600,"dpr":1}`)
	start := make(chan struct{})
	statuses := make(chan int, 200)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			<-start
			for range 25 {
				r := httptest.NewRequest(http.MethodPost, "https://api.example.com/img/sign", bytes.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				statuses <- w.Code
			}
		})
	}
	close(start)
	workers.Wait()
	close(statuses)
	accepted, refused := 0, 0
	for status := range statuses {
		switch status {
		case http.StatusOK:
			accepted++
		case http.StatusTooManyRequests:
			refused++
		default:
			t.Errorf("unexpected signing status %d", status)
		}
	}
	assert.Equal(t, 120, accepted)
	assert.Equal(t, 80, refused)
	assert.Zero(t, s.cache.Stats().MemoryItems)
}
