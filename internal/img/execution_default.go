//go:build !wasm2go

package img

import "runtime"

func gen2brainExecutionPath(f Format) string {
	if f == WebP || f == JPEG && runtime.GOARCH == "arm64" {
		return "go-wasm2go"
	}
	return "wasm"
}
