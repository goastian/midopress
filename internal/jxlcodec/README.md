# Portable JPEG XL

`lib/codec.wasm.gz` embeds libjxl 0.13.0 (commit
`5a7da83447212fe4900fc29ce97430b881caa67e`), its JPEG coefficient transcoder,
Brotli 1.2.1, Highway and LittleCMS. It is WebAssembly executed by wazero; no shared
library, executable, cgo, Node runtime, filesystem mount or network mount is
needed by the deployed binary. Third-party licenses accompany the module in
`lib/licenses`.

`Recompress` uses `JxlEncoderAddJPEGFrame` and reconstruction metadata, rather
than decoding/re-encoding JPEG pixels. `Reconstruct` rebuilds the original JPEG.
The public image backend verifies every reconstructed byte before returning a
recompression. It may use the existing native reversible binding when present;
`img.JPEGRecompressionExecutionPath()` reports that selection. In a static or
`nodynamic` binary, the embedded WASM route remains available.

Pixel encoding and decoding always use this embedded module, even if a native
JPEG XL library is installed. The old dependency's native output cursor is never
used. Each invocation receives its own WASM linear memory and module instance;
compiled code is shared within the process. A first call compiles the module and
costs more than subsequent calls.

`Inspect` uses the authoritative libjxl decoder for ICC profiles, alpha,
orientation, integer/float precision, animation timing/loop counts and container
fragments. `Decode` applies orientation and explicitly configures the libjxl CMS
to return straight-alpha, big-endian RGBA16 in sRGB. Original color metadata is
reported separately. Its low-level HDR decoder requests 203-nit tone mapping.
The image engine keeps HDR originals even under its `srgb` option: this module's
low-level capability is not approval of HDR tone mapping in the engine.
The engine's default preservation policy also keeps wide-color originals; its
explicit `srgb` policy permits SDR color conversion as an intentional appearance
change, not a claim of universal perceptual equivalence.

Limits are 64 MiB input, 16 million total canvas/frame pixels, 256 frames,
4 MiB ICC metadata, 128 MiB output, 512 MiB WASM linear memory and a 20-second
invocation deadline. Worker CPU, process and wall-clock limits apply as well.
Malformed data, unsupported encodings or exhausted budgets return an error and
the callers preserve the original. Extra channels beyond RGB/gray/alpha are not
represented by the RGBA image API.

Rebuild with WASI SDK 25 (LLVM 19.1.5), CMake, Git, gzip and Python 3:

```sh
WASI_SDK_PATH=/opt/wasi-sdk bash internal/jxlcodec/lib/build.sh
```

The script checks the upstream commit, checks out its pinned submodules, removes
host threads, compiles static libraries, links `lib/codec.c`, and writes a
deterministically compressed artifact. `JXL_BUILD_DIR` selects a build directory
and `JXL_BUILD_JOBS` controls build concurrency. No tools are installed globally.

Verify portability independently of installed libraries:

```sh
env -u LD_LIBRARY_PATH CGO_ENABLED=0 go test -tags nodynamic ./internal/img \
  -run 'TestJPEGRecompressionWithoutDynamicLibraries|TestJXLPixelCodecUsesPortableModule|TestRecompressRoundTrip'
go test ./internal/jxlcodec ./internal/imgmeta
```

Tests cover bit-exact JPEG reconstruction (including EXIF, progressive,
subsampling and restart cases), lossless unassociated alpha, orientation, frame
delays/loop count, P3/PQ/HLG decoding, invalid input and the runtime route.
