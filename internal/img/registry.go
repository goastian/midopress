package img

import (
	"fmt"
	"slices"
	"sort"
	"sync"
)

var (
	registryMu sync.Mutex
	registry   = map[string]func() (Backend, error){
		"wasm": func() (Backend, error) { return NewWASM(), nil },
	}
)

// RegisterBackend makes a backend available by name. Backends that need build
// tags or C libraries call it from an init function.
func RegisterBackend(name string, build func() (Backend, error)) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = build
}

// Backends lists the names of the backends in this build.
func Backends() []string {
	registryMu.Lock()
	defer registryMu.Unlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// NewBackend returns the backend called name: "wasm" always, "vips" when the
// binary was built with the vips tag.
func NewBackend(name string) (Backend, error) {
	registryMu.Lock()
	build, ok := registry[name]
	registryMu.Unlock()
	if !ok {
		hint := ""
		if name == "vips" && !slices.Contains(Backends(), "vips") {
			hint = " (build with -tags vips and libvips installed)"
		}
		return nil, fmt.Errorf("img: no backend called %q%s, this build has %v", name, hint, Backends())
	}
	return build()
}
