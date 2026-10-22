package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	_ "image/gif" // the formats the proxy meets
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"strconv"
	"sync/atomic"

	"github.com/goastian/midopress/internal/flows"
	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/quality"
	"github.com/goastian/midopress/internal/rules"
)

type captureState struct{ on atomic.Bool }

// Settings returns the switches as they are.
func (s *Server) settings() Settings {
	flowsOn := s.d.Flows.Enabled()
	return Settings{Flows: flowsOn, Domains: s.d.Collector.Domains(), Capture: flowsOn && s.capture.on.Load()}
}

// SetInitialSettings sets the switches at start, from the configuration.
func (s *Server) SetInitialSettings(flowsOn, domains bool) {
	s.setFlows(flowsOn)
	s.setCapture(flowsOn)
	s.d.Collector.SetDomains(domains)
}

func (s *Server) setFlows(on bool) {
	s.d.Flows.SetEnabled(on)
	if !on {
		s.setCapture(false)
	}
}

func (s *Server) setCapture(on bool) {
	on = on && s.d.Flows.Enabled() && s.d.Config().Web.CaptureBytes > 0
	s.capture.on.Store(on)
	s.d.Pipeline.SetCapture(on)
}

func (s *Server) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.settings())
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Flows   *bool `json:"flows"`
		Domains *bool `json:"domains"`
		Capture *bool `json:"capture"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Flows != nil {
		was := s.d.Flows.Enabled()
		s.setFlows(*body.Flows)
		if *body.Flows && !was && body.Capture == nil {
			s.setCapture(true) // the comparison is what most people turn the list on for
		}
	}
	if body.Capture != nil {
		s.setCapture(*body.Capture)
	}
	if body.Domains != nil {
		s.d.Collector.SetDomains(*body.Domains)
	}
	cur := s.settings()
	s.events.broadcast("settings", cur)
	writeJSON(w, http.StatusOK, cur)
}

func (s *Server) handleFlows(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	opts := flows.ListOptions{Kind: q.Get("kind")}
	switch opts.Kind {
	case "", "image", "text", "font", "other":
	default:
		writeInvalid(w, "kind must be image, text, font or other", []Issue{{Index: -1, Field: "kind", Message: "image, text, font or other"}})
		return
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeInvalid(w, "limit must be a positive number", []Issue{{Index: -1, Field: "limit", Message: "a positive number"}})
			return
		}
		opts.Limit = n
	}
	if f := q.Get("filter"); f != "" {
		e, err := rules.Parse(f)
		if err != nil {
			iss := Issue{Index: -1, Field: "filter", Message: err.Error()}
			var pe *rules.ParseError
			if errors.As(err, &pe) {
				iss.Column, iss.Message = pe.Column, pe.Msg
			}
			writeInvalid(w, "the filter is not valid", []Issue{iss})
			return
		}
		opts.Filter = e
	}
	list, total := s.d.Flows.List(opts)
	if list == nil {
		list = []flows.Flow{}
	}
	writeJSON(w, http.StatusOK, flowList{Enabled: s.d.Flows.Enabled(), Capacity: s.d.Flows.Capacity(), Total: total, Flows: list})
}

func (s *Server) handleFlow(w http.ResponseWriter, r *http.Request) {
	f, ok := s.d.Flows.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "that flow is gone")
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) handleFlowBody(original bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		o, res, ok := s.d.Flows.Bodies(r.PathValue("id"))
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "the body of that flow was not kept")
			return
		}
		b := res
		if original {
			b = o
		}
		h := w.Header()
		h.Set("Content-Type", b.ContentType)
		h.Set("Content-Length", strconv.Itoa(len(b.Data)))
		// a picture from the Internet, shown by the interface: nothing in it runs
		h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
		_, _ = w.Write(b.Data)
	}
}

// decode turns a kept body into pixels, whatever its format.
func (s *Server) decode(b *flows.Body) (image.Image, error) {
	f := img.Sniff(b.Data)
	switch f {
	case img.WebP, img.AVIF, img.JXL:
		return s.d.Backend.Decode(f, b.Data)
	}
	m, _, err := image.Decode(bytes.NewReader(b.Data))
	return m, err
}

func (s *Server) handleFlowCompare(w http.ResponseWriter, r *http.Request) {
	o, res, ok := s.d.Flows.Bodies(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "the bodies of that flow were not kept")
		return
	}
	select {
	case s.compareMu <- struct{}{}:
		defer func() { <-s.compareMu }()
	case <-r.Context().Done():
		return
	}
	out := flowCompare{Metric: "dssim", OriginalBytes: len(o.Data), ResultBytes: len(res.Data)}
	a, err1 := s.decode(o)
	b, err2 := s.decode(res)
	if err1 != nil || err2 != nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.Width, out.Height = b.Bounds().Dx(), b.Bounds().Dy()
	if a.Bounds().Size() == b.Bounds().Size() {
		if d, err := quality.DSSIM(a, b); err == nil {
			out.Distance, out.Comparable = d, true
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleHAR(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="midopress.har"`)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(flows.ToHAR(s.d.Flows.All(), s.d.Version))
}

func (s *Server) handleClearFlows(w http.ResponseWriter, _ *http.Request) {
	s.d.Flows.Clear()
	w.WriteHeader(http.StatusNoContent)
}
