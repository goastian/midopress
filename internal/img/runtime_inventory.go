package img

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"runtime"
	"runtime/debug"
	"slices"

	"github.com/ebitengine/purego"
	"github.com/goastian/midopress/internal/colorcms"
	"github.com/goastian/midopress/internal/jxlcodec"
)

type RuntimeComponent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Execution string `json:"execution"`
	Source    string `json:"source"`
	Status    string `json:"status"`
	SHA256    string `json:"sha256,omitempty"`
}

//go:embed codec_sources.json
var codecSourceJSON []byte

type codecSource struct {
	ID, Name, Module, Version string
	ModuleVersion             string `json:"module_version"`
	Inputs                    []struct{ Path, SHA256 string }
}

func moduleVersion(path string) string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range bi.Deps {
			if dep.Path == path {
				if dep.Replace != nil {
					return dep.Replace.Version
				}
				return dep.Version
			}
		}
	}
	return "unreported"
}

func RuntimeInventory(be Backend) []RuntimeComponent {
	jxl := jxlcodec.BuildInfo()
	items := []RuntimeComponent{
		{ID: "go", Name: "Go runtime / standard library", Version: runtime.Version(), Execution: "go", Source: "runtime.Version", Status: "active"},
		{ID: "jxl-wasi", Name: "libjxl", Version: jxl.Version, Execution: "wasm", Source: "embedded build manifest; revision " + jxl.Revision, Status: "included", SHA256: jxl.SHA256},
		{ID: "lcms-wasi", Name: "LittleCMS", Version: colorcms.Version, Execution: "wasm", Source: "internal/colorcms/build.sh", Status: "included", SHA256: colorcms.ModuleSHA256()},
	}
	for _, f := range []Format{JPEG, WebP, AVIF} {
		pkg := map[Format]string{JPEG: "jpegli", WebP: "webp", AVIF: "avif"}[f]
		path := "github.com/gen2brain/" + pkg
		items = append(items, RuntimeComponent{ID: pkg + "-package", Name: path, Version: moduleVersion(path), Execution: gen2brainExecutionPath(f), Source: "Go build info; embedded fallback package version", Status: "included"})
	}
	var provenance struct{ Components []codecSource }
	_ = json.Unmarshal(codecSourceJSON, &provenance)
	for _, codec := range provenance.Components {
		f := map[string]Format{"jpegli": JPEG, "webp": WebP, "avif": AVIF}[codec.Name]
		path := gen2brainExecutionPath(f)
		input := codec.Inputs[0]
		if path == "go-wasm2go" {
			input = codec.Inputs[1]
		}
		version, status := codec.Version, "included-fallback"
		if actual := moduleVersion(codec.Module); actual != "unreported" && actual != codec.ModuleVersion {
			version, status = "unreported", "source-version-mismatch"
		}
		items = append(items, RuntimeComponent{ID: codec.ID, Name: codec.Name + " codec fallback", Version: version, Execution: path, Source: codec.Module + "@" + codec.ModuleVersion + " build recipe; SHA256 of " + input.Path, Status: status, SHA256: input.SHA256})
	}
	for name, revision := range jxl.Sources {
		items = append(items, RuntimeComponent{ID: "jxl-" + name, Name: name + " (libjxl dependency)", Version: revision, Execution: "wasm", Source: "libjxl pinned submodule commit", Status: "included"})
	}
	if be.Native(WebP) {
		items = append(items, nativeComponent("webp-native", "libwebp", "libwebp", "WebPGetEncoderVersion", true))
		items = append(items, nativeComponent("webp-native-decoder", "libwebp decoder", "libwebp", "WebPGetDecoderVersion", true))
	}
	if be.Native(AVIF) {
		items = append(items, nativeComponent("avif-native", "libavif", "libavif", "avifVersion", false))
		if address := nativeSymbol("libavif", "avifCodecVersions"); address != 0 {
			var versions func(*byte)
			purego.RegisterFunc(&versions, address)
			var buffer [256]byte
			versions(&buffer[0])
			n := bytes.IndexByte(buffer[:], 0)
			if n > 0 {
				items = append(items, RuntimeComponent{ID: "avif-native-codecs", Name: "libavif codec implementations", Version: string(buffer[:n]), Execution: "native", Source: "avifCodecVersions on an already loaded library", Status: "active"})
			}
		}
		if address := nativeSymbol("libavif", "avifLibYUVVersion"); address != 0 {
			var version func() uint32
			purego.RegisterFunc(&version, address)
			if v := version(); v != 0 {
				items = append(items, RuntimeComponent{ID: "avif-native-libyuv", Name: "libavif libyuv build revision", Version: fmt.Sprint(v), Execution: "native", Source: "avifLibYUVVersion on an already loaded library", Status: "active"})
			}
		}
	}
	if JPEGRecompressionExecutionPath() == "native" {
		v := jxlNativeVersion()
		status := "active"
		if v == "" {
			v, status = "unreported", "version-unavailable"
		}
		items = append(items, RuntimeComponent{ID: "jxl-native", Name: "libjxl reversible JPEG binding", Version: v, Execution: "native", Source: "JxlEncoderVersion on the selected library handle", Status: status})
	}
	if be.Name() == "vips" {
		items = append(items, nativeComponent("vips-native", "libvips", "libvips", "vips_version_string", false))
	}
	slices.SortFunc(items, func(a, b RuntimeComponent) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	return items
}

func nativeComponent(id, name, library, symbol string, packed bool) RuntimeComponent {
	c := RuntimeComponent{ID: id, Name: name, Execution: "native", Source: symbol + " on an already loaded library", Status: "active"}
	address := nativeSymbol(library, symbol)
	if address != 0 {
		if packed {
			var version func() uint32
			purego.RegisterFunc(&version, address)
			v := version()
			c.Version = fmt.Sprintf("%d.%d.%d", v>>16, v>>8&255, v&255)
		} else {
			var version func() string
			purego.RegisterFunc(&version, address)
			c.Version = version()
		}
	}
	if c.Version == "" {
		c.Version, c.Status = "unreported", "version-unavailable"
	}
	return c
}
