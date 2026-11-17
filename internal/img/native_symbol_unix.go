//go:build (linux || darwin || freebsd) && (amd64 || arm64) && !nodynamic

package img

import "github.com/ebitengine/purego"

func nativeSymbol(_ string, symbol string) uintptr {
	address, _ := purego.Dlsym(purego.RTLD_DEFAULT, symbol)
	return address
}
