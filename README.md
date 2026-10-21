# MidoPress

An HTTP/HTTPS proxy that recompresses whatever goes through it so pages weigh
less. We are getting it ready for Midori (desktop and Android), but it is a
regular proxy and works with any browser that can use one.

It is a fork of [compy](https://github.com/barnacs/compy) by Barna Csorogi.
The original project had been idle for years, and that is where we started.
There is a section further down on what changed and what did not.

## Status

For now it is compy with the house tidied up: same behavior, Go 1.27, brotli
without cgo, new tests, CI, and a Dockerfile that does not make a new CA on
every start. What we want to build on top (new formats, measured quality, a
web interface, Midori integration) is in
[docs/05-work-plan.md](docs/05-work-plan.md).

Do not put it in front of people you do not know yet. It has known problems,
the most annoying ones being:

- With MITM enabled, WebSockets (`wss://`) do not work.
- It ignores `Cache-Control: no-transform` and `Range` requests.
- If converting an image fails halfway through a response, the client gets a
  truncated body.
- It does not check that the result is smaller than the original.
- It has no timeouts.
- A request to the proxy itself, using an address it does not recognize (see
  below), loops instead of returning an error.

The full list, with where each item lives in the code, is in
[docs/01-current-state.md](docs/01-current-state.md). All of that is phase 1
work.

## What it does

- Compresses HTML, CSS and JavaScript with brotli or gzip, depending on what
  the browser accepts.
- Recompresses JPEG at whatever quality you ask for (50 by default).
- Converts PNG, GIF and JPEG to WebP when the browser accepts it. Careful with
  GIFs: they end up as a still image.
- Minifies HTML, CSS and JS if you turn on `-minify`. It breaks pages, which is
  why it is off.
- Can be used over TLS between the browser and the proxy, and with
  man-in-the-middle to compress HTTPS traffic too.
- Optional Basic authentication.

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
repository, `go install ./cmd/midopress` works too. `midopress -version` tells
you which version you have.

## Usage

The simplest case, an HTTP proxy on port 9999:

```sh
midopress
```

Point the browser at `localhost:9999` and you are done. With the proxy turned
on, the page `http://MACHINE-NAME:9999/` shows the bytes saved. It has to be
the name that `hostname` returns: with `localhost` or `127.0.0.1` the proxy
forwards the request to itself and it hangs. If you start with
`-host 127.0.0.1:9999`, then that exact address does work.

The options you will use most:

| Option | Default | What it does |
|--------|---------|--------------|
| `-host` | `:9999` | address to listen on |
| `-brotli` | `6` | brotli level, 0 to 11 |
| `-gzip` | `6` | gzip level, 0 to 9 |
| `-jpeg` | `50` | JPEG quality from 1 to 100, `0` turns it off |
| `-png`, `-gif` | on | convert PNG and GIF |
| `-minify` | off | minify HTML, CSS and JS |
| `-user`, `-pass` | empty | user and password (go together) |
| `-cert`, `-key` | empty | proxy certificate, to speak TLS |
| `-ca`, `-cakey` | empty | CA for MITM mode |

JPEG quality can be changed per request with the header
`X-MidoPress-Quality: 30`. The old `X-Compy-Quality` still works, but it will
go away.

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
    return "HTTPS press.example.org:9999";
  }
  return "DIRECT";
}
```

### MITM

To compress HTTPS pages as well, the proxy impersonates each site and signs
the certificates with a CA of your own:

```sh
openssl req -x509 -newkey rsa:2048 -nodes -keyout ca.key -out ca.crt \
  -days 3650 -subj '/CN=MidoPress CA'

midopress -cert cert.crt -key cert.key -ca ca.crt -cakey ca.key
```

After that you have to install `ca.crt` as a trusted authority in the browser.
Whoever has `ca.key` can intercept the traffic of everyone who installed that
CA, so treat it like a password.

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

Any extra option for the binary goes at the end of `docker run`, for example
`... midopress -jpeg 40 -brotli 9`.

## Development

```sh
go build ./...
go test -race ./...
golangci-lint run
```

The tool versions we use in CI are in `.github/workflows/`. How the code is
laid out and where it is heading is explained in
[docs/04-proposed-architecture.md](docs/04-proposed-architecture.md).

## What changed from compy

- The module is `github.com/goastian/midopress` and the binary is `midopress`.
  The code now lives in `cmd/` and `internal/`.
- Go 1.27 instead of 1.9 and 1.16. Brotli comes from `andybalholm/brotli`, in
  pure Go, and `kothar/brotli-go` is gone.
- Tests use `testing` and `testify`, with ports picked by the system.
- The license went from ISC to Apache 2.0. The parts that come from compy are
  still the work of its author, and their copyright notice is in
  [NOTICE](NOTICE).
- The proxy behaves the same, except for the name of the quality header.

## Credits

Barna Csorogi wrote compy. The codecs come from
[go-libjpeg](https://github.com/pixiv/go-libjpeg),
[webp](https://github.com/chai2010/webp) and
[brotli](https://github.com/andybalholm/brotli), and minification from
[tdewolff/minify](https://github.com/tdewolff/minify).

## License

Apache 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
