package imageapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

type CredentialScope struct {
	Hosts      []string `json:"hosts"`
	Profiles   []string `json:"profiles"`
	MaxWidth   int      `json:"max_width"`
	MaxDPR     float64  `json:"max_dpr"`
	TTLSeconds int      `json:"ttl_seconds"`
}

type Credential struct {
	CredentialScope
	ID      string `json:"id"`
	Expires int64  `json:"expires"`
}

type credentialWindow struct {
	start   int64
	expires int64
	count   int
}

func credentialMAC(key []byte, payload string) []byte {
	m := hmac.New(sha256.New, key)
	_, _ = m.Write([]byte("MidoPress image signing credential v1\n" + payload))
	return m.Sum(nil)
}

func scopeHost(raw string) (string, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	wildcard := strings.HasPrefix(raw, "*.")
	host := strings.TrimPrefix(raw, "*.")
	if len(host) == 0 || len(host) > 253 || strings.ContainsAny(host, "/:@?#\\ \t\r\n") || strings.HasSuffix(host, ".") {
		return "", errRequest
	}
	host, err := idna.Lookup.ToASCII(host)
	if err != nil || host == "localhost" || strings.HasSuffix(host, ".localhost") || !strings.Contains(host, ".") {
		return "", errRequest
	}
	if ip, err := netip.ParseAddr(host); err == nil && (!publicAddress(ip) || wildcard) {
		return "", errRequest
	}
	if wildcard {
		suffix, _ := publicsuffix.PublicSuffix(host)
		if suffix == host {
			return "", errRequest
		}
		host = "*." + host
	}
	return host, nil
}

func (s *Service) MintCredential(scope CredentialScope) (string, Credential, error) {
	if len(scope.Hosts) == 0 || len(scope.Hosts) > 32 || len(scope.Profiles) == 0 || len(scope.Profiles) > 16 ||
		scope.MaxWidth < 1 || scope.MaxWidth > s.o.MaxWidth || math.IsNaN(scope.MaxDPR) || math.IsInf(scope.MaxDPR, 0) ||
		scope.MaxDPR < 1 || scope.MaxDPR > s.o.MaxDPR || scope.TTLSeconds < 60 || scope.TTLSeconds > 3600 {
		return "", Credential{}, errRequest
	}
	scope.Hosts = append([]string(nil), scope.Hosts...)
	for i, host := range scope.Hosts {
		normal, err := scopeHost(host)
		if err != nil {
			return "", Credential{}, errRequest
		}
		scope.Hosts[i] = normal
	}
	for _, profile := range scope.Profiles {
		if _, ok := s.o.Profiles()[profile]; !ok {
			return "", Credential{}, errRequest
		}
	}
	scope.Profiles = append([]string(nil), scope.Profiles...)
	slices.Sort(scope.Hosts)
	scope.Hosts = slices.Compact(scope.Hosts)
	slices.Sort(scope.Profiles)
	scope.Profiles = slices.Compact(scope.Profiles)
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", Credential{}, err
	}
	c := Credential{CredentialScope: scope, ID: hex.EncodeToString(nonce[:]), Expires: s.now().Add(time.Duration(scope.TTLSeconds) * time.Second).Unix()}
	raw, err := json.Marshal(c)
	if err != nil || len(raw) > 4096 {
		return "", Credential{}, errRequest
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return payload + "." + base64.RawURLEncoding.EncodeToString(credentialMAC(s.o.Key, payload)), c, nil
}

func (s *Service) readCredential(token string) (Credential, error) {
	if len(token) == 0 || len(token) > 8192 {
		return Credential{}, errRequest
	}
	payload, encoded, ok := strings.Cut(token, ".")
	if !ok {
		return Credential{}, errRequest
	}
	mac, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || !hmac.Equal(mac, credentialMAC(s.o.Key, payload)) {
		return Credential{}, errRequest
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	var c Credential
	if err != nil || json.Unmarshal(raw, &c) != nil || c.Expires <= s.now().Unix() || c.Expires > s.now().Add(time.Hour).Unix() || len(c.ID) != 32 {
		return Credential{}, errRequest
	}
	return c, nil
}

func (c Credential) permits(r Request) bool {
	u, err := url.Parse(r.URL)
	if err != nil || r.Width < 1 || r.Width > c.MaxWidth || r.DPR > c.MaxDPR || !slices.Contains(c.Profiles, r.Profile) {
		return false
	}
	host, err := idna.Lookup.ToASCII(strings.ToLower(u.Hostname()))
	if err != nil {
		return false
	}
	for _, allowed := range c.Hosts {
		if host == allowed || strings.HasPrefix(allowed, "*.") && strings.HasSuffix(host, allowed[1:]) && host != allowed[2:] {
			return true
		}
	}
	return false
}

func (s *Service) credentialAllowed(c Credential) bool {
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	now := s.now().Unix()
	if s.credentials == nil {
		s.credentials = make(map[string]credentialWindow)
	}
	for id, state := range s.credentials {
		if state.expires <= now || state.start+60 <= now {
			delete(s.credentials, id)
		}
	}
	state, ok := s.credentials[c.ID]
	if !ok {
		if len(s.credentials) >= 1024 {
			return false
		}
		state = credentialWindow{start: now, expires: c.Expires}
	}
	if state.count >= 120 {
		return false
	}
	state.count++
	s.credentials[c.ID] = state
	return true
}

func (s *Service) serveSigner(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST, OPTIONS")
		s.fail(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	credential, err := s.readCredential(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if err != nil || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		s.fail(w, http.StatusForbidden, "invalid_credential")
		return
	}
	if r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" {
		s.fail(w, http.StatusConflict, "session_resource")
		return
	}
	var spec Request
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 12288))
	d.DisallowUnknownFields()
	if d.Decode(&spec) != nil || d.Decode(new(any)) != io.EOF {
		s.fail(w, http.StatusBadRequest, "invalid_request")
		return
	}
	spec.Mode = "optimized"
	spec, err = s.normalize(spec)
	if err != nil || !credential.permits(spec) {
		s.fail(w, http.StatusForbidden, "outside_scope")
		return
	}
	if !s.credentialAllowed(credential) {
		w.Header().Set("Retry-After", "60")
		s.fail(w, http.StatusTooManyRequests, "credential_rate_limit")
		return
	}
	expires := min(s.now().Add(s.o.SignatureTTL).Unix(), credential.Expires)
	q := spec.values()
	q.Set("exp", strconv.FormatInt(expires, 10))
	q.Set("sig", signature(s.o.Key, q))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"optimized_url": s.o.PublicURL + "/img?" + q.Encode(),
		"fallback_url": spec.URL, "expires": time.Unix(expires, 0)})
}
