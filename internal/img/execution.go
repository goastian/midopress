package img

type CodecRuntime struct {
	Encoder string `json:"encoder"`
	Decoder string `json:"decoder"`
	Reason  string `json:"reason"`
}

func RuntimeInfo(be Backend) map[string]CodecRuntime {
	out := map[string]CodecRuntime{}
	for _, f := range []Format{JPEG, PNG, GIF, WebP, AVIF, JXL} {
		path := ExecutionPath(be, f)
		info := CodecRuntime{Encoder: path, Decoder: path}
		if be.Name() == "wasm" {
			switch {
			case path == "native":
				info.Reason = "codec package loaded a shared library; executes native code in the calling process"
			case path == "wasm":
				info.Reason = "embedded WebAssembly codec; no native codec selected"
			default:
				info.Reason = "Go implementation"
			}
			if f == JPEG {
				info.Decoder = "go"
			}
			if f == JXL && !be.Supports(JXL) {
				info.Encoder = "disabled"
				info.Reason = "shared-library JPEG XL pixel encoding is disabled because its output cursor is unsafe under Go garbage collection; reversible JPEG recompression uses a separate binding"
			}
		}
		out[f.String()] = info
	}
	return out
}

func ExecutionPath(be Backend, f Format) string {
	if f == PNG || f == GIF {
		return "go"
	}
	if be.Name() != "wasm" {
		return be.Name()
	}
	if be.Native(f) {
		return "native"
	}
	switch f {
	case JPEG, WebP, AVIF, JXL:
		return "wasm"
	default:
		return "go"
	}
}
