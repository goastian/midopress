package admin

import (
	"net/http"

	"github.com/goastian/midopress/internal/imageapi"
)

func (s *Server) handleImages(w http.ResponseWriter, _ *http.Request) {
	cfg := s.d.Config().ImageAPI
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": s.d.Images != nil, "public_url": cfg.PublicURL,
		"max_bytes": cfg.MaxBytes, "max_pixels": cfg.MaxPixels, "max_width": cfg.MaxWidth,
		"max_dpr": cfg.MaxDPR, "timeout_ms": cfg.Timeout.Milliseconds(),
	})
}

func (s *Server) handleSignImage(w http.ResponseWriter, r *http.Request) {
	if s.d.Images == nil {
		writeError(w, http.StatusConflict, "images_disabled", "enable image_api and restart the service")
		return
	}
	var body imageapi.Request
	if !readJSON(w, r, &body) {
		return
	}
	body.Mode = "optimized"
	optimized, expires, err := s.d.Images.Sign(body)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_image_request", "use a public HTTP(S) image, a known profile and width/DPR within the configured limits")
		return
	}
	body.Mode = "original"
	original, _, err := s.d.Images.Sign(body)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_image_request", "invalid image parameters")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"optimized_url": optimized, "original_url": original,
		"fallback_url": body.URL, "expires": expires})
}

func (s *Server) handleImageCredential(w http.ResponseWriter, r *http.Request) {
	if s.d.Images == nil {
		writeError(w, http.StatusConflict, "images_disabled", "enable the image API first")
		return
	}
	var scope imageapi.CredentialScope
	if !readJSON(w, r, &scope) {
		return
	}
	token, grant, err := s.d.Images.MintCredential(scope)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_scope", "select public image hosts, known profiles, valid size limits and an expiry between 60 and 3600 seconds")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "scope": grant, "public_url": s.d.Config().ImageAPI.PublicURL})
}
