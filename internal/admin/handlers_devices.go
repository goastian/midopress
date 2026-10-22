package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goastian/midopress/internal/auth"
	"github.com/goastian/midopress/internal/magic"
	"github.com/goastian/midopress/internal/qr"
)

var deviceID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// devices opens the store of tokens, which is a file that the command line can
// change too, so it is read each time. It answers for the caller when the proxy
// does not use tokens.
func (s *Server) devices(w http.ResponseWriter) (*auth.Store, bool) {
	if s.d.OpenDevices == nil {
		writeError(w, http.StatusConflict, "no_tokens", "the proxy does not use device tokens: set auth.mode to token and auth.devices_file")
		return nil, false
	}
	st, err := s.d.OpenDevices()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return nil, false
	}
	return st, true
}

func (s *Server) deviceView(d auth.Device, st map[string]deviceStat, active map[string]bool) Device {
	out := Device{ID: d.ID, Profile: d.Profile, Created: d.Created, Active: active[d.ID]}
	if x, ok := st[d.ID]; ok {
		out.Requests, out.BytesIn, out.BytesOut, out.Saved = x.Requests, x.BytesIn, x.BytesOut, x.Saved
		if !x.LastSeen.IsZero() {
			t := x.LastSeen
			out.LastSeen = &t
		}
	}
	return out
}

type deviceStat = struct {
	Requests uint64
	BytesIn  uint64
	BytesOut uint64
	Saved    int64
	LastSeen time.Time
}

func (s *Server) liveDeviceData() (map[string]deviceStat, map[string]bool) {
	st := map[string]deviceStat{}
	for id, d := range s.d.Collector.Devices() {
		st[id] = deviceStat{Requests: d.Requests, BytesIn: d.BytesIn, BytesOut: d.BytesOut, Saved: d.Saved, LastSeen: d.LastSeen}
	}
	active := map[string]bool{}
	for _, c := range s.d.Collector.Clients(activeWindow) {
		if c.Device != "" && c.Active {
			active[c.Device] = true
		}
	}
	return st, active
}

func (s *Server) handleDevices(w http.ResponseWriter, _ *http.Request) {
	store, ok := s.devices(w)
	if !ok {
		return
	}
	stt, active := s.liveDeviceData()
	list := store.List()
	out := make([]Device, 0, len(list))
	for _, d := range list {
		out = append(out, s.deviceView(d, stt, active))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) checkProfile(w http.ResponseWriter, name string) bool {
	if name == "" {
		return true
	}
	if _, ok := s.liveProfiles()[name]; !ok {
		writeInvalid(w, "there is no profile called "+name, []Issue{{Index: -1, Field: "profile", Message: "unknown profile"}})
		return false
	}
	return true
}

func (s *Server) handleAddDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID      string `json:"id"`
		Profile string `json:"profile"`
		// Host is how the extension reaches the proxy; it defaults to the name the
		// administration interface was reached with
		Host string `json:"host"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !deviceID.MatchString(body.ID) {
		writeInvalid(w, "the id is letters, digits, dots, - and _, up to 64", []Issue{{Index: -1, Field: "id", Message: "letters, digits, ., - and _"}})
		return
	}
	if !s.checkProfile(w, body.Profile) {
		return
	}
	store, ok := s.devices(w)
	if !ok {
		return
	}
	for _, d := range store.List() {
		if d.ID == body.ID {
			writeError(w, http.StatusConflict, "exists", "there is a device called "+body.ID)
			return
		}
	}
	token, err := store.Add(body.ID, body.Profile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.reloadAuth()

	cfg := s.d.Config()
	host := strings.TrimSpace(body.Host)
	if host == "" {
		host = r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
	}
	_, port, err := net.SplitHostPort(cfg.Listen.Addr)
	if err != nil {
		port = "9999"
	}
	tls := "0"
	if cfg.Listen.TLS.Cert != "" {
		tls = "1"
	}
	q := url.Values{"host": {host}, "port": {port}, "user": {body.ID}, "token": {token}, "tls": {tls}}
	cfgURL := "midopress://config?" + q.Encode()
	svg, err := qr.SVG(cfgURL)
	if err != nil {
		svg = ""
	}
	writeJSON(w, http.StatusCreated, newDevice{ID: body.ID, Profile: body.Profile, Token: token, ConfigURL: cfgURL, ConfigQRSVG: svg})
}

func (s *Server) reloadAuth() {
	if s.d.Auth != nil {
		_ = s.d.Auth.Reload()
	}
}

func (s *Server) handlePatchDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Profile string `json:"profile"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !s.checkProfile(w, body.Profile) {
		return
	}
	store, ok := s.devices(w)
	if !ok {
		return
	}
	found := false
	for _, d := range store.List() {
		found = found || d.ID == id
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "there is no device called "+id)
		return
	}
	if err := store.SetProfile(id, body.Profile); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.reloadAuth()
	stt, active := s.liveDeviceData()
	for _, d := range store.List() {
		if d.ID == id {
			writeJSON(w, http.StatusOK, s.deviceView(d, stt, active))
			return
		}
	}
}

func (s *Server) handleRevokeDevice(w http.ResponseWriter, r *http.Request) {
	store, ok := s.devices(w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := store.Revoke(id); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "there is no device called "+id)
		return
	}
	s.reloadAuth()
	w.WriteHeader(http.StatusNoContent)
}

// ---- the certificate

func (s *Server) handleCA(w http.ResponseWriter, _ *http.Request) {
	cfg := s.d.Config()
	out := caInfo{MagicHost: cfg.Web.MagicHost}
	if s.d.CA != nil {
		c := s.d.CA.Cert
		sum := sha256.Sum256(c.Raw)
		out.Available = true
		out.Subject = c.Subject.String()
		out.NotBefore = c.NotBefore.UTC().Format(time.RFC3339)
		out.NotAfter = c.NotAfter.UTC().Format(time.RFC3339)
		out.SHA256 = strings.ToUpper(hex.EncodeToString(sum[:]))
		if cfg.Web.MagicHost != "" {
			out.PageURL = "http://" + cfg.Web.MagicHost + "/"
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCAFile(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if s.d.CA == nil {
			writeError(w, http.StatusNotFound, "not_found", "HTTPS interception is not configured: there is no certificate")
			return
		}
		h := w.Header()
		var data []byte
		switch kind {
		case "pem":
			data = s.d.CA.CertPEM()
			h.Set("Content-Type", "application/x-x509-ca-cert")
			h.Set("Content-Disposition", `attachment; filename="midopress-ca.crt"`)
		case "der":
			data = s.d.CA.Cert.Raw
			h.Set("Content-Type", "application/x-x509-ca-cert")
			h.Set("Content-Disposition", `attachment; filename="midopress-ca.der"`)
		default:
			p, err := magic.P12(s.d.CA.CertPEM(), magic.P12Password)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", err.Error())
				return
			}
			data = p
			h.Set("Content-Type", "application/x-pkcs12")
			h.Set("Content-Disposition", `attachment; filename="midopress-ca.p12"`)
		}
		h.Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
	}
}
