# Changelog

Format based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
The project uses [semantic versioning](https://semver.org/).

## 0.3.0 (not tagged yet)

A web interface and an administration API: operate MidoPress without touching
files, and install the CA without reading documentation.

### Added

- Administration server (`web.addr`, `127.0.0.1:9998` by default) with the
  interface embedded in the binary (`go:embed`) and a REST API with SSE. It is
  documented in [docs/09-admin-api.md](docs/09-admin-api.md).
- Pages: dashboard (savings in total, by type, codec and profile, hit rate,
  latencies, connections, top domains), traffic (live flows with a filter in the
  rules language, detail, original against compressed comparator with DSSIM,
  HAR export), devices (connected clients, tokens, profile, QR for the Midori
  extension), rules and profiles editor with live validation and "test a URL",
  certificate, logs with a level that changes while running, and service.
- The magic host `midopress.it`: the proxy answers it by itself with the page
  that installs the CA (PEM, DER, PKCS#12, QR, steps per platform, trust check).
- `midopress admin token` shows the admin token; `midopress ca export -format
  p12` writes a PKCS#12 trust store (password `midopress`).
- `web:` section in the configuration. What the interface saves goes to
  `ui.yaml` in `web.state_dir` (`~/.midopress`) and wins over the file.

### Changed (behavior you may notice)

- The administration server is on by default, on the loopback. The first start
  makes a token in `~/.midopress/admin.token` (permissions 600) and prints the
  sign-in link once on stderr. Set `web.enabled: false` to turn it off.
- Failed tokens are rate limited per address (8 wrong ones, one minute).
- The live list of flows and the aggregation by domain are off until switched on;
  they live in memory only and secret headers are never kept.
- Building needs the interface: `cd web && npm ci && npm run build` before
  `go build` (a plain `go build` works, and says the interface is not built).
- The Docker image publishes 9998 as well and binds the interface to all
  addresses inside the container.

## 0.2.0 (not tagged yet)

The proxy core is rewritten. The findings of
[docs/01-current-state.md](docs/01-current-state.md) with high severity are
closed, and the status of every one is listed there.

### Changed (behavior you may notice)

- `auth.block_global` is on by default: only clients on private, loopback or
  link-local addresses are accepted. The proxy refuses to start with it off
  unless authentication is on. If you served clients on the internet with
  `-user` and `-pass`, add `-block-global=false`.
- `-user` and `-pass` now turn on `basic` authentication; the credentials are
  compared in constant time and wrong guesses are rate limited per address.
- Unknown paths on the proxy itself answer 404 instead of 501.
- `/cacert` serves the CA certificate. It used to serve the proxy's own.
- A request in origin form (`GET /path`) over HTTP/1.1 is a visit to the proxy
  itself, whatever the `Host` says, so health checks work through a NAT or a
  published port. Over HTTP/2 it is how a client asks for another host, and it
  is forwarded. Requests that would loop back to the proxy answer 508.
- The `-gif` flag now means "recode static GIFs". Animated ones are left alone;
  compy turned them into a single still image.
- A request to the proxy with `CONNECT` and no CA configured now opens a plain
  tunnel. It used to fail.
- Logging uses `log/slog`. Request URLs and host names are not logged unless
  `log.urls` is set.
- The text of the stats page and the exit message changed.

### Added (perceptual quality, profiles and cache)

- **DSSIM is the measure** that decides when a lossy image is close enough:
  `balanced` accepts a distance of 0.002, `saver` 0.006 and `extreme` 0.015, and
  every result is decoded again and measured before it is used. SSIM and
  SSIMULACRA2 are available with `pipeline.image.metric`. The first guess of the
  search, the iteration limit, the analysis size, the formats that are skipped
  (AVIF under 10 KB, those that never reach the target) and the measured cost of
  each format are all there to keep the number of encodes low.
- **Rules**: `rules:` with the mitmproxy filter language and the actions
  `passthrough`, `passthrough_tls`, `profile` and `set`; invalid rules are
  reported with their position and stop the proxy from starting.
- **Profiles** can be changed and defined in `profiles:`; the profile of a
  request comes from the device token, the `X-MidoPress-Profile` header, a rule,
  `Save-Data` or the default.
- **Cache with a disk tier** (`cache.dir`), TTLs derived from `Cache-Control` and
  `Expires`, size cleanup, and hit rate metrics. What the background jobs finish
  is stored in it.
- **Hot reload** of rules, profiles, the default profile, minification, TTLs,
  `mitm.passthrough` and the log level, on `SIGHUP` and when the configuration
  file changes.
- **Prometheus `/metrics`** on the proxy port or on `metrics.addr`.
- `midopress bench -passes N` runs the whole pipeline with its cache; `-metric`,
  `-backend` and the measured dssim of every result in `-verify`.

Fixed in this phase:

- The WebP decoder was measured with the wrong colour range and chroma
  upsampling, so every WebP looked worse than it was and never reached a strict
  target. It now matches `dwebp` byte for byte.
- A result found after the time ran out was remembered as "the original is
  better" and the background work never replaced it.

Behavior changes of this phase:

- `balanced` is stricter: a dssim of 0.002 instead of an SSIM of 0.975. Images
  that were at 0.975 but not at 0.002 are now sent at a higher quality.
- `pipeline.image.cache_size` is gone: use `cache.memory`.
- Intercepted HTTPS connections get the profile of the device that opened them.

### Added (: modern codecs)

- **Images are recoded against a measured quality.** `internal/img` picks the
  best format the browser accepts (JPEG XL, AVIF, WebP, jpegli JPEG, optimized
  PNG), searches the lowest quality that keeps a similarity (SSIM) of 0.975 with
  the original, decodes the result again to check it, and sends the original
  unless it is at least 2% smaller. No cgo: the codecs are WebAssembly, and use
  the native libwebp, libavif and libjxl when they are installed.
- **Profiles**: `lossless`, `balanced` (default), `saver`, `extreme`, with
  `pipeline.profile`, `-profile`, the `X-MidoPress-Profile` header, and
  `Save-Data: on` raising balanced to saver. Saver and extreme shrink big
  images to the screen using `Sec-CH-Viewport-Width`, `Sec-CH-DPR` or
  `X-MidoPress-Viewport`.
- **PNG**: lossless optimization in pure Go (palette, bit depth, filters,
  deflate), conversion to lossless WebP or JPEG XL when smaller, 256-colour
  quantization in `extreme`, and alpha dropped when opaque.
- **Animated GIF becomes animated WebP**; animated PNG is left alone.
- **Metadata**: EXIF orientation is applied to the pixels, ICC profiles that are
  not sRGB are kept (and the image is not recoded), the rest is removed.
- **Fonts**: TTF, OTF and WOFF become WOFF2 for browsers loading a font.
- **Minification** by kind and mode (`pipeline.text.minify`), safe by default;
  `-minify` turns on HTML, CSS and JavaScript in safe mode.
- **Cache and background work**: results are kept by content, and what does not
  fit in `pipeline.image.timeout` is finished in the background, so the next
  request gets the best one. Slower brotli levels for responses that look
  cacheable.
- **`midopress bench`** and `tools/fetch-images`.
- CI, release and Docker build without C libraries; Windows is a normal target.
  `docker build --build-arg NATIVE_CODECS=1` adds the native codec libraries.

Behavior changes of this phase:

- The default image quality is no longer a fixed JPEG 50: it is searched for each
  image against the profile. `-jpeg N` fixes it; `-jpeg 0` still disables JPEG.
- `pipeline.text.minify` is a map (`html`, `css`, `js`, `svg`, `json`); a plain
  `true` or `false` still works. CSS, HTML, SVG and JSON are minified by default
  in safe mode, JavaScript is not. Minifying breaks Subresource Integrity.
- `pipeline.text.brotli`, `gzip` and `zstd` default to 0, the level of the
  profile.
- Animated GIFs are no longer left alone for clients that accept WebP.
- Images get `Vary: Accept, Save-Data`.

- **Reversible JPEG to JPEG XL** recompression through libjxl (found at run
  time, no cgo), verified by rebuilding the JPEG before it is used.
- **Optional libvips backend** (`-tags vips`, `pipeline.image.backend: vips`).

### Added

- **WebSockets and tunnels**: `Upgrade` requests are relayed, and every
  `CONNECT` that is not intercepted is a bidirectional tunnel with half-close.
  A tunnel to an intercepted host that does not start with TLS is relayed as is.
- **Configuration**: YAML file, `MIDOPRESS_*` environment variables and flags;
  `midopress config check` validates the file and opens everything it
  references; unknown keys are errors. `examples/midopress.yaml` documents every
  setting.
- **Subcommands**: `midopress ca init` and `ca export`, `midopress device add`,
  `list` and `revoke`, `midopress config check`, `midopress version`.
- **Authentication**: one token per device, stored hashed in a JSON file that is
  reloaded when it changes or on `SIGHUP`; `Bearer` tokens; per-address
  lockout.
- **HTTPS interception**: a certificate per site, made from the SNI of the
  ClientHello, with its own key and a random serial, cached; the connection to
  the origin is reused. A built-in list of sensitive hosts and `mitm.passthrough`
  patterns are never intercepted, and a host whose client rejects our certificate
  is passed through for `mitm.auto_passthrough_ttl`.
- **Brotli, gzip, zstd and deflate** for text; the response is encoded with what
  the client accepts, understood with q-values. gzip, deflate and zstd responses
  of known size are recompressed as brotli.
- **Response handling**: `no-transform`, `Range`, 206, 304, `HEAD`, redirects,
  errors and event streams are left alone; `Content-Length`, weak `ETag` and
  `Vary` are set on what is recoded; `Content-MD5` and `Repr-Digest` are dropped.
- **Safety**: an original is sent when the result is not at least `min_gain`
  smaller or anything fails; limits on image bytes, pixels and time; a CPU
  limiter and a memory budget, beyond which responses are passed through;
  timeouts on every stage; decompression bombs are not expanded.
- **Upstream**: a connection pool with HTTP/2, a DNS cache, and optional refusal
  of private addresses (`upstream.deny_private`).
- **Graceful shutdown**: requests in flight finish and tunnels are drained for
  `server.shutdown_grace`.
- The proxy certificate is re-read when it changes, and on `SIGHUP`.
- `/health` answers `ok` without credentials.
- A corpus of 37 real responses recorded as HAR (`go run ./tools/record-corpus`),
  replayed through the proxy by the tests, and benchmarks of the whole proxy.
- A CI job that builds the Docker image.

### Fixed

- WebSockets did not work with HTTPS interception.
- `Cache-Control: no-transform`, `Range` requests and 206 and 304 responses
  were transcoded and broken.
- A conversion that failed half way truncated the response.
- Responses could become larger than the original.
- The JPEG quality header was a data race between concurrent requests.
- `Accept-Encoding: gzip,deflate,br`, without spaces, was not understood.
- Content types were matched exactly, so `image/jpg`, `text/plain`, JSON and
  SVG were missed.
- Hop-by-hop headers were forwarded.
- The `User-Agent` of the client was echoed as a response header.
- Detection of requests meant for the proxy only worked for one exact host
  name, and anything else made the proxy connect to itself and hang.
- Idle connections to origins stayed open after shutdown.

### Security

- No timeouts on the listeners: slow clients could hold connections forever.
- No limits on image size, so a large image could exhaust memory.
- Certificates made for intercepted sites reused the CA key and copied fields of
  the real certificate.
- Credentials were compared with `!=`.
- Every URL was logged.

### Removed

- The cgo dependencies `go-libjpeg` and `chai2010/webp`.
- The `compy` packages `proxy` and `transcoder`, replaced by the ones above.
- The `datacounter` dependency.

## 0.1.0 (not tagged yet)

First version of the fork. The proxy behaves the same as compy at its last
commit (`fe1404d`, March 2022).

### Changed

- The compy fork is renamed to MidoPress. Module
  `github.com/goastian/midopress`, binary `midopress`, code in `cmd/midopress`
  and `internal/`.
- License: from ISC to Apache 2.0. The compy copyright notice is kept in
  `NOTICE`.
- Go 1.27 (it was 1.16 in `go.mod` and 1.9 in the Dockerfile).
- Brotli moves from `kothar/brotli-go` (cgo) to `andybalholm/brotli` (pure Go).
- Updated dependencies: `chai2010/webp` 1.4.0, `tdewolff/minify` 2.24.
- The JPEG quality header is now `X-MidoPress-Quality`.
- `ResponseWriter.ReadFrom` is renamed to `CopyFrom`: it had the name of
  `io.ReaderFrom` with a different signature and `go vet` flagged it.
- The admin page and the authentication realm now say MidoPress.
- If the MITM listener stops, or writing the admin page fails, it is now
  logged instead of ignored.
- The Dockerfile is now multi-stage on Debian trixie, runs as an unprivileged
  user and keeps the certificate and the CA in a volume. Before, they were
  regenerated on every start.
- The proxy certificate generated by the Docker image includes
  `subjectAltName`.
- Tests use `testing` and `testify`, with a test server of our own and
  ephemeral ports. `check.v1` and `go-httpbin` are removed.

### Added

- The `-version` option.
- CI on GitHub Actions: lint, `govulncheck`, build and tests on Linux and
  macOS, and Windows as experimental. Release workflow with GoReleaser.
- `.golangci.yml` and `.goreleaser.yaml`.
- New README, `NOTICE`, and the `docs/` folder with the project analysis and
  the work plan.

### Deprecated

- `X-Compy-Quality` still works as an alias of `X-MidoPress-Quality`, but it
  will be removed in a later version.

### Removed

- `.travis.yml` and `docker.sh`.
