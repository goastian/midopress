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
			switch path {
			case "native":
				info.Reason = "codec package loaded a shared library; executes native code in the calling process"
			case "wasm":
				info.Reason = "embedded WebAssembly codec; no native codec selected"
			case "go-wasm2go":
				info.Reason = "codec WebAssembly was translated to Go at build time; executes generated Go code, not a WASM sandbox"
			default:
				info.Reason = "Go implementation"
			}
			if f == JPEG {
				info.Decoder = "go"
			}
			if f == JXL {
				info.Encoder, info.Decoder = "wasm", "wasm"
				info.Reason = "embedded libjxl WASI module, with bounded linear memory and no filesystem or network mounts; reversible JPEG recompression selects native when available and otherwise the embedded WASM implementation"
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
	case JXL:
		return "wasm"
	case JPEG, WebP, AVIF:
		return gen2brainExecutionPath(f)
	default:
		return "go"
	}
}
