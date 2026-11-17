//go:build windows && !nodynamic

package img

import (
	"syscall"

	"golang.org/x/sys/windows"
)

func nativeSymbol(library, symbol string) uintptr {
	name, err := windows.UTF16PtrFromString(library + ".dll")
	if err != nil {
		return 0
	}
	var handle windows.Handle
	if windows.GetModuleHandleEx(windows.GET_MODULE_HANDLE_EX_FLAG_UNCHANGED_REFCOUNT, name, &handle) != nil {
		return 0
	}
	address, _ := syscall.GetProcAddress(syscall.Handle(handle), symbol)
	return address
}
