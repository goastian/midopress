# Changelog

Format based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
The project uses [semantic versioning](https://semver.org/).

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
