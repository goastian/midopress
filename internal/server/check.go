package server

import (
	"crypto/tls"
	"errors"
	"fmt"

	"github.com/goastian/midopress/internal/auth"
	"github.com/goastian/midopress/internal/config"
	"github.com/goastian/midopress/internal/mitm"
)

// Check validates the configuration and also opens the files it points to, so
// that problems show up before the server starts. It returns every problem.
func Check(cfg *config.Config) error {
	errs := []error{cfg.Validate()}

	if cfg.Listen.TLS.Cert != "" {
		if _, err := tls.LoadX509KeyPair(cfg.Listen.TLS.Cert, cfg.Listen.TLS.Key); err != nil {
			errs = append(errs, fmt.Errorf("listen.tls: %w", err))
		}
	}
	if cfg.MITM.Enabled() {
		if _, err := mitm.LoadCA(cfg.MITM.CACert, cfg.MITM.CAKey); err != nil {
			errs = append(errs, fmt.Errorf("mitm: %w", err))
		}
	}
	if _, err := mitm.NewPolicy(mitm.PolicyOptions{
		Patterns:    cfg.MITM.Passthrough,
		UseDefaults: cfg.MITM.DefaultPassthrough,
		AutoTTL:     cfg.MITM.AutoPassthroughTTL,
	}); err != nil {
		errs = append(errs, fmt.Errorf("mitm.passthrough: %w", err))
	}
	if cfg.Auth.Mode == "token" && cfg.Auth.DevicesFile != "" {
		a, err := auth.New(auth.Options{Mode: auth.ModeToken, DevicesFile: cfg.Auth.DevicesFile, ReloadInterval: -1})
		if err != nil {
			errs = append(errs, fmt.Errorf("auth: %w", err))
		} else {
			a.Close()
		}
	}
	return errors.Join(errs...)
}
