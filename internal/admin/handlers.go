package admin

import (
	"net/http"
	"runtime"
	"runtime/debug"
	"slices"
	"time"

	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/stats"
)

type flowEvent struct{ Flow any }

const activeWindow = 2 * time.Minute

func (s *Server) snapshot() StatsSnapshot {
	now := time.Now()
	ps := s.d.Proxy.Stats()
	cs := s.d.Pipeline.CacheStats()
	slots, busy, queued := s.d.Pipeline.CPU()
	topN := 15
	snap := StatsSnapshot{
		Time:          now,
		UptimeSeconds: now.Sub(s.d.Started).Seconds(),
		Version:       s.d.Version,
		Snapshot:      s.d.Collector.Snapshot(topN),
		Cache: cacheView{
			Hits: cs.Hits(), Misses: cs.Misses, HitRate: cs.HitRate(),
			MemoryBytes: cs.MemoryBytes, MemoryItems: cs.MemoryItems, DiskBytes: cs.DiskBytes, DiskItems: cs.DiskItems,
			Evictions: cs.Evictions, Expired: cs.Expired, DiskErrors: cs.DiskErrors,
		},
		Connections: connectionsView{
			ClientsActive: s.d.Collector.ActiveClients(activeWindow), RequestsInFlight: ps.InFlight,
			TunnelsActive: ps.TunnelsActive, TunnelsTotal: ps.Tunnels, TunnelBytesUp: ps.TunnelBytesUp, TunnelBytesDown: ps.TunnelBytesDown, TLSHandshakes: ps.TLSHandshakes,
			TLSHandshakeErrors: ps.TLSHandshakeErrs, UpstreamErrors: ps.UpstreamErrors, Denied: ps.Denied,
		},
		CPU: cpuView{Slots: slots, Busy: busy, BackgroundQueue: queued},
	}
	return snap
}

func (s *Server) handleStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.snapshot())
}

func (s *Server) handleAbout(w http.ResponseWriter, _ *http.Request) {
	cfg := s.d.Config()
	a := about{
		Version: s.d.Version, GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
		Started: s.d.Started, UptimeSeconds: time.Since(s.d.Started).Seconds(),
		Backend: s.d.Backend.Name(), Backends: img.Backends(), Native: map[string]bool{},
		Modules: []module{}, StateDir: cfg.Web.StateDirPath(), ProxyAddr: cfg.Listen.Addr,
		TLS: cfg.Listen.TLS.Cert != "", MITM: cfg.MITM.Enabled(), AuthMode: cfg.Auth.Mode,
	}
	for _, f := range []img.Format{img.WebP, img.AVIF, img.JXL} {
		a.Native[f.String()] = s.d.Backend.Native(f)
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, m := range bi.Deps {
			v := m.Version
			if m.Replace != nil {
				v = m.Replace.Version
			}
			a.Modules = append(a.Modules, module{Path: m.Path, Version: v})
		}
		slices.SortFunc(a.Modules, func(x, y module) int {
			switch {
			case x.Path < y.Path:
				return -1
			case x.Path > y.Path:
				return 1
			}
			return 0
		})
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleClients(w http.ResponseWriter, _ *http.Request) {
	cl := s.d.Collector.Clients(10 * time.Minute)
	for i := range cl {
		cl[i].Profile = s.profileOf(cl[i])
	}
	if cl == nil {
		cl = []stats.Client{}
	}
	writeJSON(w, http.StatusOK, cl)
}

// profileOf is the profile a client gets by default: the one of its device, or
// the default of the proxy.
func (s *Server) profileOf(c stats.Client) string {
	if c.Device != "" {
		if p := s.d.Auth.DeviceProfile(c.Device); p != "" {
			return p
		}
	}
	_, def := s.d.Pipeline.Profiles()
	return def
}

func (s *Server) handleGetLog(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.logState())
}

type logState struct {
	Level string    `json:"level"`
	Lines []LogLine `json:"lines"`
}

func (s *Server) logState() logState {
	st := logState{Level: "info", Lines: []LogLine{}}
	if s.d.Level != nil {
		st.Level = levelName(s.d.Level.Level())
	}
	if s.d.Logs != nil {
		if l := s.d.Logs.Lines(); l != nil {
			st.Lines = l
		}
	}
	return st
}

func (s *Server) handlePutLog(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Level string `json:"level"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	lv, ok := parseLevel(body.Level)
	if !ok || s.d.Level == nil {
		writeInvalid(w, "the level must be debug, info, warn or error", []Issue{{Index: -1, Field: "level", Message: "debug, info, warn or error"}})
		return
	}
	s.d.Level.Set(lv)
	writeJSON(w, http.StatusOK, s.logState())
}

func (s *Server) handleReload(w http.ResponseWriter, _ *http.Request) {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if s.d.Reload == nil {
		writeError(w, http.StatusConflict, "unavailable", "this server can not read its configuration again")
		return
	}
	if err := s.d.Reload(); err != nil {
		writeInvalid(w, err.Error(), []Issue{{Index: -1, Field: "", Message: err.Error()}})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleClearCache(w http.ResponseWriter, _ *http.Request) {
	s.d.Pipeline.ClearCache()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleResetStats(w http.ResponseWriter, _ *http.Request) {
	s.d.Collector.Reset()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleShutdown(w http.ResponseWriter, _ *http.Request) {
	if s.d.Shutdown == nil {
		writeError(w, http.StatusConflict, "unavailable", "this server can not stop the proxy")
		return
	}
	w.WriteHeader(http.StatusAccepted)
	go func() {
		time.Sleep(300 * time.Millisecond)
		s.d.Shutdown()
	}()
}
