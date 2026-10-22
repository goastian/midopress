package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// settleTime is how long the file has to be quiet before it is read again.
var settleTime = 300 * time.Millisecond

// watchConfigFile reloads the configuration when the file changes. It watches
// the directory, because editors replace the file instead of writing to it, and
// waits for the changes to settle so that a half written file is not read.
func watchConfigFile(ctx context.Context, path string, reload func() error, log *slog.Logger) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Warn("cannot watch the configuration file, use SIGHUP to reload it", "err", err)
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if err := w.Add(filepath.Dir(abs)); err != nil {
		_ = w.Close()
		log.Warn("cannot watch the configuration file, use SIGHUP to reload it", "err", err)
		return
	}
	go func() {
		defer func() { _ = w.Close() }()
		settle := settleTime
		timer := time.NewTimer(time.Hour)
		timer.Stop()
		for {
			select {
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if filepath.Clean(ev.Name) == abs && ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0 {
					timer.Reset(settle)
				}
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				log.Warn("configuration watcher", "err", err)
			case <-timer.C:
				if err := reload(); err != nil {
					log.Error("reload failed, keeping the previous state", "err", err)
				} else {
					log.Info("configuration reloaded")
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}
