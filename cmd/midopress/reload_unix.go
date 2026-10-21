//go:build !windows

package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/goastian/midopress/internal/server"
)

// watchReload re-reads the certificate and the devices file on SIGHUP.
func watchReload(ctx context.Context, srv *server.Server, log *slog.Logger) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP)
	go func() {
		defer signal.Stop(c)
		for {
			select {
			case <-c:
				if err := srv.Reload(); err != nil {
					log.Error("reload failed, keeping the previous state", "err", err)
				} else {
					log.Info("reloaded certificate and devices")
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}
