# MidoPress

An HTTP/HTTPS proxy that recompresses whatever goes through it so pages weigh
less. We are building it for Midori (desktop and Android), but it is a regular
proxy and works with any browser that can use one.

It is a fork of [compy](https://github.com/barnacs/compy) by Barna Csorogi.
The original project had been idle for years, and that is where we started.
There is a section further down on what changed.

## Status

The proxy core has been rewritten and tested hard: it should now be safe to put
in front of real browsing, which compy was not. What it does not have yet is the
nicer part. There are no new image formats (AVIF, JPEG XL), no web interface and
no quality-driven compression. Those are the next phases, see

Things you should know before running it for other people:

- It decrypts HTTPS when you give it a CA, and then it sees everything. It logs
  no URLs and no host names unless you ask, but whoever runs it can read the
  traffic.
- Image recoding is lossy by default (JPEG quality 50). Text is only
  compressed, unless you turn on `-minify`, which can break pages.
- Images are decoded in memory, about 4 bytes per pixel for each job that runs
  at the same time. The defaults are safe for a small machine; see the
  `pipeline` section of the [example configuration](examples/midopress.yaml)
  before using a big one.
- There are no per-user quotas or accounting yet.

## What it does

- Compresses HTML, CSS, JavaScript, JSON, XML, SVG, fonts and other text-like
  files with brotli, zstd or gzip, whichever the browser prefers and accepts.
- Recompresses gzip, deflate and zstd responses as brotli when the browser
  supports it, the result is smaller and the size of the response is known and
  under 1 MiB. Larger ones, and ones of unknown length, keep the origin's
  compression.
- Recodes JPEG, PNG and static GIF images, as WebP when the browser accepts it.
  Animated GIFs are left alone.
- Does not make responses bigger: when it can see the whole body (every image,
  and text of known size under 1 MiB), the original is sent unless the result is
  at least 2% smaller. Text of unknown length is compressed as it streams, so it
  can not be checked, and incompressible text grows by under 1%.
- Leaves alone what it should not touch: `Cache-Control: no-transform`, range
  requests, `HEAD`, redirects and errors, event streams, anything it does not
  recognize, and anything that fails to convert.
- Tunnels WebSockets and any other `CONNECT` traffic, including HTTP/2 to the
  proxy.
- Can intercept HTTPS with a certificate authority of yours, to compress it
  too. Banks, government sites, password managers and the like are never
  intercepted, and a host whose client rejects our certificate (an app with a
  pinned certificate) is passed through from then on.
- Authenticates clients with a shared password or with one token per device.
- Reads its configuration from a YAML file, environment variables or flags.

On a corpus of 37 real pages and files recorded from the web, with HTTPS
interception on, nothing came out different except by being smaller. For a
Firefox client the text content was 38% lighter, and the images 33% lighter at
quality 50.

## Installation

You need Go 1.27, a C compiler and the libjpeg headers, because the image
codecs still use cgo. That goes away when we move to codecs without cgo
(phase 2).

```sh
# Fedora
sudo dnf install gcc libjpeg-turbo-devel
# Debian, Ubuntu
sudo apt install gcc libjpeg-dev
# macOS
brew install jpeg-turbo
export CGO_CFLAGS="-I$(brew --prefix jpeg-turbo)/include"
export CGO_LDFLAGS="-L$(brew --prefix jpeg-turbo)/lib"
```

Then:

```sh
go install github.com/goastian/midopress/cmd/midopress@latest
```

The binary ends up in `$(go env GOPATH)/bin/midopress`. From a clone of the
repository, `go install ./cmd/midopress` works too. `midopress version` tells
you which version you have.

## Usage

The simplest case, an HTTP proxy on port 9999 for your own machine or network:

```sh
midopress
```

Point the browser at `localhost:9999` and you are done. With the proxy turned
on, `http://localhost:9999/` shows the bytes saved, and `/health` answers `ok`
for load balancers (over HTTP/1.1, under any name or port).

By default the proxy only accepts clients on private, loopback and link-local
addresses (`block_global`). To serve clients on the internet you have to turn
that off, and the proxy will not start unless you also set a password or
tokens, because an open proxy gets abused within hours.

### Configuration

Everything is a setting with a default. They can come from, lowest priority
first, a YAML file, `MIDOPRESS_*` environment variables and command line flags:

```sh
midopress -config midopress.yaml
MIDOPRESS_PIPELINE_TEXT_BROTLI=9 midopress
midopress -brotli 9
```

[examples/midopress.yaml](examples/midopress.yaml) lists every setting with its
default and says what it does. The name of the environment variable of a setting
is its path in upper case, so `pipeline.text.brotli` is
`MIDOPRESS_PIPELINE_TEXT_BROTLI`. A key that does not exist is an error, not
something that is silently ignored.

```sh
midopress config check -config midopress.yaml    # validate, open the files, report every problem
midopress config check -config midopress.yaml -print   # show the effective settings
```

The flags that compy had still work: `-host`, `-cert`, `-key`, `-ca`, `-cakey`,
`-user`, `-pass`, `-brotli`, `-gzip`, `-jpeg`, `-png`, `-gif` and `-minify`.
`-log-level`, `-log-format`, `-log-urls` and `-block-global` are new. Run
`midopress -h` for the list.

### Authentication

With `-user` and `-pass` the proxy asks for that user name and password. For
more than one person, give each device its own token, so that you can cut one
off without changing the others:

```sh
midopress device add -file devices.json -id phone      # prints the token once
midopress device list -file devices.json
midopress device revoke -file devices.json -id phone

midopress -config midopress.yaml    # with auth.mode: token and auth.devices_file
```

The device id is the proxy user name and the token is the password; a
`Proxy-Authorization: Bearer <token>` header works too. Only a hash of each
token is stored. The file is re-read when it changes, or on `SIGHUP`. Someone
who guesses wrong too many times is locked out for a minute.

Without TLS, user names and passwords cross the network in the clear. The proxy
warns about it on startup.

### TLS to the proxy

To encrypt the traffic between the browser and the proxy you need a
certificate. If you do not have a real one (Let's Encrypt), a self-made one is
fine for trying things out:

```sh
openssl req -x509 -newkey rsa:2048 -nodes -keyout cert.key -out cert.crt \
  -days 3650 -subj '/CN=press.example.org' \
  -addext 'subjectAltName=DNS:press.example.org'

midopress -cert cert.crt -key cert.key
```

Browsers do not let you pick "HTTPS proxy" from their UI, so you have to give
them a PAC file:

```javascript
function FindProxyForURL(url, host) {
  if (url.substring(0, 5) == "http:" || url.substring(0, 6) == "https:") {
    return "HTTPS press.example.org:9999; DIRECT";
  }
  return "DIRECT";
}
```

The certificate is picked up again when it changes on disk, so renewing it
needs no restart.

### Intercepting HTTPS

To compress HTTPS pages as well, the proxy makes a certificate for each site and
signs it with a CA of yours:

```sh
midopress ca init                 # writes ~/.midopress/ca.crt and ca.key
midopress ca export -format der -out midopress-ca.der   # what Android asks for

midopress -cert cert.crt -key cert.key -ca ~/.midopress/ca.crt -cakey ~/.midopress/ca.key
```

After that you have to install `ca.crt` as a trusted authority in the browser
(with the proxy on, `http://localhost:9999/cacert` downloads it). Whoever has
`ca.key` can intercept the traffic of everyone who installed that CA, so treat
it like a password. A CA made with `openssl` works too.

Some sites must never be intercepted. A built-in list covers banks, government,
health, password managers and Mozilla's own services; add more in
`mitm.passthrough` as `example.com`, `*.example.com` or `~d <regex>`.

## Docker

```sh
docker build -t midopress .
docker run -d --name midopress -p 9999:9999 \
  -e CERTIFICATE_DOMAIN=press.example.org \
  -v midopress-data:/var/lib/midopress \
  midopress
```

The first run generates the proxy certificate and the CA and stores them in the
volume. Later starts reuse them, so you do not have to install the CA on the
clients again. To get the CA out of the container:

```sh
docker cp midopress:/var/lib/midopress/ca.crt .
```

Configure it with `MIDOPRESS_*` variables (add `-e MIDOPRESS_AUTH_MODE=basic
-e MIDOPRESS_AUTH_USER=me -e MIDOPRESS_AUTH_PASS=...`), with a file named by
`MIDOPRESS_CONFIG`, or with flags at the end of the command. `docker stop`
shuts the proxy down cleanly: it waits for the requests in flight.

If you publish the port to the internet, clients arrive from public addresses,
so also set `MIDOPRESS_AUTH_BLOCK_GLOBAL=false` and an authentication mode, and
consider `MIDOPRESS_UPSTREAM_DENY_PRIVATE=true` so that clients cannot reach the
network the proxy sits in.

## Development

```sh
go build ./...
go test -race ./...
golangci-lint run
```

The tests include a corpus of real responses, recorded as HAR with
`go run ./tools/record-corpus` (the list of URLs is in
`testdata/corpus/urls.txt`) and replayed through the proxy. They also include
benchmarks of the whole proxy:

```sh
go test -run '^$' -bench . -benchmem ./internal/server/
```

The tool versions we use in CI are in `.github/workflows/`. How the code is laid
out and where it is heading is explained in
[docs/04-proposed-architecture.md](docs/04-proposed-architecture.md).

## What changed from compy

- The module is `github.com/goastian/midopress` and the binary is `midopress`.
- Go 1.27 instead of 1.9 and 1.16, and brotli in pure Go.
- The proxy was rewritten: WebSockets work, responses that must not change are
  left alone, a failed conversion falls back to the original, there are
  timeouts and limits, authentication is not open to timing attacks, and the
  certificate for each HTTPS site is made properly. The list of what was wrong
  with compy, and what happened to each item, is in
  [docs/01-current-state.md](docs/01-current-state.md).
- Behavior you may notice: `block_global` is on, unknown paths on the proxy
  itself answer 404 instead of 501, HTTP/2 clients can ask for another host the
  way browsers do, and the quality header is `X-MidoPress-Quality` (the old
  `X-Compy-Quality` still works for now).
- The license went from ISC to Apache 2.0. The parts that come from compy are
  still the work of its author, and their copyright notice is in
  [NOTICE](NOTICE).

## Credits

Barna Csorogi wrote compy. The codecs come from
[go-libjpeg](https://github.com/pixiv/go-libjpeg),
[webp](https://github.com/chai2010/webp),
[brotli](https://github.com/andybalholm/brotli) and
[klauspost/compress](https://github.com/klauspost/compress), and minification
from [tdewolff/minify](https://github.com/tdewolff/minify).

## License

Apache 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
