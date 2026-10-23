// Package admin is the administration server: the web interface that is
// embedded in the binary and the API behind it. It listens apart from the
// proxy and needs a token for everything but the interface itself and a
// liveness probe.
package admin

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goastian/midopress/internal/auth"
	"github.com/goastian/midopress/internal/config"
	"github.com/goastian/midopress/internal/flows"
	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/metrics"
	"github.com/goastian/midopress/internal/mitm"
	"github.com/goastian/midopress/internal/pipeline"
	"github.com/goastian/midopress/internal/proxy"
	"github.com/goastian/midopress/internal/stats"
	"github.com/goastian/midopress/internal/webui"
)

// Deps is what the administration server works with. The server package
// builds it; tests can build a smaller one.
type Deps struct {
	Version   string
	Started   time.Time
	Token     string
	Pipeline  *pipeline.Pipeline
	Proxy     *proxy.Proxy
	Collector *stats.Collector
	Flows     *flows.Store
	Logs      *LogHub
	Level     *slog.LevelVar
	Registry  *metrics.Registry // nil when the metrics are off
	Backend   img.Backend
	Auth      *auth.Authenticator
	CA        *mitm.CA // nil without HTTPS interception

	// Config returns the configuration in use.
	Config func() *config.Config
	// Overrides returns what the interface saved, nil for nothing.
	Overrides func() (*config.Overrides, error)
	// Save checks the overrides as a whole, writes them and applies them. If
	// anything is wrong nothing is written or applied.
	Save func(*config.Overrides) error
	// Reload reads the configuration again.
	Reload func() error
	// Shutdown stops the proxy.
	Shutdown func()
	// OpenDevices returns the store of the devices, nil when the proxy does not
	// use tokens.
	OpenDevices func() (*auth.Store, error)

	Pprof bool
}

// Server is the administration server.
type Server struct {
	d     Deps
	guard *guard
	mux   *http.ServeMux
	ui    fs.FS
	built bool

	events *eventHub

	saveMu    sync.Mutex // one change of the configuration at a time
	compareMu chan struct{}
	capture   captureState
}

// New builds the server.
func New(d Deps) (*Server, error) {
	if len(d.Token) < 32 {
		return nil, errors.New("admin: the token is too short")
	}
	s := &Server{d: d, guard: newGuard(d.Token), mux: http.NewServeMux(), compareMu: make(chan struct{}, 2)}
	s.ui, s.built = webui.FS()
	s.events = newEventHub(s)
	s.routes()
	return s, nil
}

// Handler returns the handler to serve.
func (s *Server) Handler() http.Handler { return s.secure(s.mux) }

// Close stops what runs in the background.
func (s *Server) Close() { s.events.close() }

func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/debug/") {
			h.Set("Cache-Control", "no-store")
		} else {
			h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		}
		next.ServeHTTP(w, r)
	})
}

// authed wraps a handler with the check of the token.
func (s *Server) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch status, retry := s.guard.check(r); status {
		case 0:
			h(w, r)
		case http.StatusTooManyRequests:
			w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
			writeError(w, status, "too_many_attempts", "too many wrong tokens, wait a minute")
		default:
			w.Header().Set("WWW-Authenticate", `Bearer realm="MidoPress"`)
			writeError(w, status, "unauthorized", "the admin token is missing or wrong")
		}
	}
}

func (s *Server) routes() {
	m := s.mux
	get := func(pattern string, h http.HandlerFunc) { m.HandleFunc("GET "+pattern, s.authed(h)) }
	send := func(method, pattern string, h http.HandlerFunc) { m.HandleFunc(method+" "+pattern, s.authed(h)) }

	m.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	get("/api/v1/about", s.handleAbout)
	get("/api/v1/stats", s.handleStats)
	get("/api/v1/stats/missed", s.handleMissed)
	get("/api/v1/events", s.handleEvents)
	get("/api/v1/stats/stream", s.handleEvents)

	get("/api/v1/flows", s.handleFlows)
	get("/api/v1/flows.har", s.handleHAR)
	get("/api/v1/flows/{id}", s.handleFlow)
	get("/api/v1/flows/{id}/original", s.handleFlowBody(true))
	get("/api/v1/flows/{id}/transformed", s.handleFlowBody(false))
	get("/api/v1/flows/{id}/compare", s.handleFlowCompare)
	send("DELETE", "/api/v1/flows", s.handleClearFlows)

	get("/api/v1/settings", s.handleGetSettings)
	send("PUT", "/api/v1/settings", s.handlePutSettings)

	get("/api/v1/config", s.handleConfig)
	send("PUT", "/api/v1/config/rules", s.handlePutRules)
	send("PUT", "/api/v1/config/profiles/{name}", s.handlePutProfile)
	send("DELETE", "/api/v1/config/profiles/{name}", s.handleDeleteProfile)
	send("PUT", "/api/v1/config/default-profile", s.handlePutDefaultProfile)
	send("DELETE", "/api/v1/config/ui", s.handleForgetUI)
	send("POST", "/api/v1/config/validate", s.handleValidate)
	send("POST", "/api/v1/config/test", s.handleTest)

	get("/api/v1/devices", s.handleDevices)
	send("POST", "/api/v1/devices", s.handleAddDevice)
	send("PATCH", "/api/v1/devices/{id}", s.handlePatchDevice)
	send("DELETE", "/api/v1/devices/{id}", s.handleRevokeDevice)
	get("/api/v1/clients", s.handleClients)

	get("/api/v1/ca", s.handleCA)
	get("/api/v1/ca.pem", s.handleCAFile("pem"))
	get("/api/v1/ca.der", s.handleCAFile("der"))
	get("/api/v1/ca.p12", s.handleCAFile("p12"))

	get("/api/v1/log", s.handleGetLog)
	send("PUT", "/api/v1/log", s.handlePutLog)

	send("POST", "/api/v1/service/reload", s.handleReload)
	send("POST", "/api/v1/service/cache/clear", s.handleClearCache)
	send("POST", "/api/v1/service/stats/reset", s.handleResetStats)
	send("POST", "/api/v1/service/shutdown", s.handleShutdown)

	if s.d.Registry != nil {
		get("/api/v1/metrics", s.d.Registry.Handler().ServeHTTP)
	}
	if s.d.Pprof {
		get("/debug/pprof/", pprof.Index)
		get("/debug/pprof/cmdline", pprof.Cmdline)
		get("/debug/pprof/profile", pprof.Profile)
		get("/debug/pprof/symbol", pprof.Symbol)
		get("/debug/pprof/trace", pprof.Trace)
		get("/debug/pprof/{name}", func(w http.ResponseWriter, r *http.Request) { pprof.Handler(r.PathValue("name")).ServeHTTP(w, r) })
	}

	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		// not an endpoint, or not with this method: say so in JSON, whether or
		// not a token came
		var allow []string
		for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
			probe := r.Clone(r.Context())
			probe.Method = method
			if _, pattern := m.Handler(probe); pattern != "" && pattern != "/api/" {
				allow = append(allow, method)
			}
		}
		if len(allow) > 0 {
			w.Header().Set("Allow", strings.Join(allow, ", "))
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "that endpoint does not take "+r.Method)
			return
		}
		writeError(w, http.StatusNotFound, "not_found", "there is no such endpoint")
	})
	m.HandleFunc("/", s.serveUI)
}

// serveUI serves the embedded interface. The routes of the interface live in
// the fragment of the address, so any other path is a file or nothing.
func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.built {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, notBuiltPage)
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" || name == "." {
		name = "index.html"
	}
	f, err := s.ui.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		data, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, name, st.ModTime(), bytesReader(data))
		return
	}
	http.ServeContent(w, r, name, st.ModTime(), rs)
}

const notBuiltPage = `<!doctype html><meta charset="utf-8"><title>MidoPress</title>
<body style="font-family:system-ui;max-width:40rem;margin:3rem auto;padding:0 1rem">
<h1>MidoPress</h1>
<p>The administration API is running, but this binary was built without the web interface.</p>
<p>Build it with <code>cd web &amp;&amp; npm ci &amp;&amp; npm run build</code> and then <code>go build ./cmd/midopress</code>,
or use a release.</p></body>`

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Error: msg, Code: code})
}

func writeInvalid(w http.ResponseWriter, msg string, issues []Issue) {
	writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: msg, Code: "invalid", Issues: issues})
}

// readJSON decodes the body, refusing what is not a single JSON value of a
// reasonable size.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "the body is not valid JSON: "+err.Error())
		return false
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, "bad_request", "the body has more than one JSON value")
		return false
	}
	return true
}
