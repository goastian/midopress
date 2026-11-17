package server

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/goastian/midopress/internal/imageapi"
	"github.com/goastian/midopress/internal/img"
)

func (s *Server) newImageAPI() error {
	a := s.cfg.ImageAPI
	if !a.Enabled {
		return nil
	}
	keyFile := a.KeyFile
	if keyFile == "" {
		keyFile = filepath.Join(s.cfg.Web.StateDirPath(), "image-api.key")
	}
	key, err := imageapi.LoadKey(keyFile)
	if err != nil {
		return fmt.Errorf("image_api.key_file: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	s.images, err = imageapi.New(imageapi.Options{
		Key: key, PublicURL: a.PublicURL, MaxBytes: int64(a.MaxBytes), MaxPixels: a.MaxPixels,
		MaxWidth: a.MaxWidth, MaxDPR: a.MaxDPR, Concurrency: a.Concurrency, Timeout: a.Timeout,
		SignatureTTL: a.SignatureTTL, CacheTTL: a.CacheTTL, Backend: s.cfg.Pipeline.Image.Backend,
		Profiles:  func() map[string]*img.Profile { profiles, _ := s.pipe.Profiles(); return profiles },
		Transform: imageapi.Process(executable),
	})
	if err != nil {
		return err
	}
	s.imageSrv = &http.Server{Handler: s.images, ReadHeaderTimeout: s.cfg.Server.ReadHeaderTimeout,
		IdleTimeout: s.cfg.Server.IdleTimeout, WriteTimeout: a.Timeout + s.cfg.Server.ReadHeaderTimeout,
		MaxHeaderBytes: 16 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	return nil
}

func (s *Server) listenImages() error {
	if s.imageSrv == nil || s.imageLn != nil {
		return nil
	}
	ln, err := net.Listen("tcp", s.cfg.ImageAPI.Addr)
	if err != nil {
		return fmt.Errorf("image_api.addr: %w", err)
	}
	s.imageLn = ln
	return nil
}

func (s *Server) ImageAddr() net.Addr {
	if s.imageLn == nil {
		return nil
	}
	return s.imageLn.Addr()
}
