// Package webui holds the web interface, built by `npm run build` in web/ and
// embedded in the binary. A build without it still works: the admin server
// then says the interface was not built.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built interface, and whether there is one.
func FS() (fs.FS, bool) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return sub, false
	}
	return sub, true
}
