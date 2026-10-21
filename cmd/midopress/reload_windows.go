//go:build windows

package main

import (
	"context"
	"log/slog"

	"github.com/goastian/midopress/internal/server"
)

// watchReload does nothing on Windows, which has no SIGHUP.
func watchReload(context.Context, *server.Server, *slog.Logger) {}
