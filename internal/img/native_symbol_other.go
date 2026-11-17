//go:build nodynamic || !(linux || darwin || freebsd || windows) || ((linux || darwin || freebsd) && !(amd64 || arm64))

package img

func nativeSymbol(string, string) uintptr { return 0 }
