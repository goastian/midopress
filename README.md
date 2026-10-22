# MidoPress

An HTTP/HTTPS proxy that recompresses whatever goes through it so pages weigh
less. We are building it for Midori (desktop and Android), but it is a regular
proxy and works with any browser that can use one.

It is a fork of [compy](https://github.com/barnacs/compy) by Barna Csorogi.
The original project had been idle for years, and that is where we started.
There is a section further down on what changed.

## Status

The proxy core has been rewritten and tested hard: it should now be safe to put
in front of real browsing, which compy was not. Images are recoded with modern
formats (WebP, AVIF, JPEG XL) by a quality search that measures what it does, and
the binary needs no C libraries. It has a web interface to watch and run it (see
[Administration interface](#administration-interface)). What it does not have yet
is per-user accounting.

Things you should know before running it for other people:

- It decrypts HTTPS when you give it a CA, and then it sees everything. It logs
  no URLs and no host names unless you ask, but whoever runs it can read the
  traffic.
- Image recoding is lossy by default, within the `balanced` profile: every
  result is measured against the original and kept only if it stays above a
  similarity of 0.975 (SSIM), which is what you can not tell apart by eye. Other
  profiles are stricter (`lossless`) or push further (`saver`, `extreme`), see
  [Image profiles](#image-profiles). Text is compressed, and HTML, CSS, SVG and
  JSON are minified in a safe mode that never changes how a page renders;
  JavaScript is not minified unless you ask.
- Minifying a file breaks its Subresource Integrity (`integrity="..."`) hash, if
  the page has one. The proxy reads the pages that go through it and never
  minifies a script or style sheet that a page it has seen loads with an
  `integrity` attribute. It can not know about one that a script adds later, so
  turn the kinds you do not want off in `pipeline.text.minify`.
- Images are decoded in memory, and that is expensive: measured, a recode holds
  80 to 190 bytes per pixel at its peak (a 7.7 megapixel photo takes over a
  gigabyte). `pipeline.memory_budget` bounds it; 0, the default, takes a quarter
  of the memory of the machine or container (256 MiB to 8 GiB). A response that
  finds no room waits up to `pipeline.transcode_wait` and then goes through as it
  came, and its image is recoded in the background when room frees up, so the
  next request gets it. A picture that would need more than the whole budget is
  never recoded: with the smallest budget, 256 MiB, that is anything over about
  1.3 megapixels in the profiles that try AVIF.
- There are no per-user quotas or accounting yet.

## What it does

- Compresses HTML, CSS, JavaScript, JSON, XML, SVG, fonts and other text-like
  files with brotli, zstd or gzip, whichever the browser prefers and accepts.
- Recompresses gzip, deflate and zstd responses as brotli when the browser
  supports it, the result is smaller and the size of the response is known and
  under 1 MiB. Larger ones, and ones of unknown length, keep the origin's
  compression. A response that the origin already sends as brotli is recompressed
  at the slowest levels (10 or 11, which give about a tenth less) when the
  profile allows it (`balanced` at 10, `saver` and `extreme` at 11) and the
  response can be cached; bodies too big to make a request wait for that are
  finished in the background.
- Recodes JPEG, PNG and GIF images with the best format the browser says it
  accepts (JPEG XL, AVIF, WebP, else a better JPEG made with jpegli or an
  optimized PNG), at the lowest quality that keeps the picture looking the same.
  The WebP and AVIF pictures that sites already send are recoded too, measured
  against what the site sent, and the original goes when nothing smaller is as
  good (`pipeline.image.modern`). Animated ones, and any that carry a colour
  profile other than sRGB, an orientation or a crop, are left alone; so is JPEG
  XL unless its header says plain sRGB with no alpha plane.
  Animated GIFs become animated WebP, and animated PNG is left alone. Colour
  profiles other than sRGB are never dropped, and the EXIF orientation is applied
  to the pixels before the metadata is removed.
- Turns TTF, OTF and WOFF fonts into WOFF2 when the browser is loading a font
  and accepts it, which saves about a third and does not change a glyph.
- Keeps what it computed: a popular image or font is recoded once, and the formats
  that are slow in WebAssembly (AVIF, JPEG XL) are finished in the background for
  the next request, so nobody waits for them.
- Does not touch video: it is already compressed with codecs that need hours of
  CPU to improve a little, and recoding it would break seeking (range requests).
- Does not make responses bigger: when it can see the whole body (every image,
  and text of known size under 1 MiB), the original is sent unless the result is
  at least 2% smaller. Text of unknown length is compressed as it streams, so it
  can not be checked, and incompressible text grows by under 1%.
- Leaves alone what it should not touch: `Cache-Control: no-transform` (but see
  [no-transform](#no-transform)), range requests, `HEAD`, redirects and errors,
  event streams, anything it does not recognize, and anything that fails to
  convert.
- Tunnels WebSockets and any other `CONNECT` traffic, including HTTP/2 to the
  proxy.
- Can intercept HTTPS with a certificate authority of yours, to compress it
  too. Banks, government sites, password managers and the like are never
  intercepted, and a host whose client rejects our certificate (an app with a
  pinned certificate) is passed through from then on.
- Authenticates clients with a shared password or with one token per device.
- Reads its configuration from a YAML file, environment variables or flags.

On a corpus of 37 real pages and files recorded from the web, with HTTPS
interception on, nothing came out different except by being smaller. The image
numbers are in [Benchmark](#benchmark).

## Installation

You need Go 1.27, and Node 22 to build the web interface (a build without it
still works, and the administration server then says so). There is no C compiler, no cgo and no C library to install:
the image codecs run as WebAssembly inside the binary, so it is built with
`CGO_ENABLED=0` for Linux, macOS and Windows.

Then:

```sh
go install github.com/goastian/midopress/cmd/midopress@latest
```

`go install` builds without the web interface, which is built from a clone:

```sh
git clone https://github.com/goastian/midopress && cd midopress
(cd web && npm ci && npm run build)
go build ./cmd/midopress
```

The releases and the Docker image include it.

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
`-jpeg N` now fixes the quality (and `-jpeg 0` still turns JPEG off); without it
the quality is found for each image. `-profile` is new, with `-log-level`,
`-log-format`, `-log-urls` and `-block-global`. Run
`midopress -h` for the list.

### Image profiles

A profile says how far an image may be pushed. All of them keep the picture
looking the same to a person; they differ in how strict the measurement is:

| profile | similarity (SSIM) | what else |
|---|---|---|
| `lossless` | exact | only metadata, PNG optimization and lossless recoding |
| `balanced` (default) | 0.975 | dimensions never change |
| `saver` | 0.95 | big images shrink to the screen when the client says its size; lossy animations |
| `extreme` | 0.90 | PNG graphics reduced to 256 colours, animations become their first frame |

Set the default with `pipeline.profile` or `-profile`. A client can ask for
another one per request with `X-MidoPress-Profile`, and `Save-Data: on` raises
`balanced` to `saver`. For the screen size the proxy reads `Sec-CH-Viewport-Width`
and `Sec-CH-DPR` (the origin has to ask for them with `Accept-CH`), or the header
`X-MidoPress-Viewport: WIDTHxDPR` that the MidoPress extension will send.
`X-MidoPress-Quality` fixes the quality of one request and skips the search.

Pictures with few colours (logos, screenshots, icons) are graphics and stay
lossless in every profile except `extreme`. Photographs, including PNG
photographs, are recoded against the similarity target.

A profile that resizes (`saver`, `extreme`) needs to know how big the picture is
shown. When the client does not say it, the proxy uses what the page said: it
reads the `width` of the `<img>` tags of the HTML that goes by, remembers it for
the address of each image, and assumes `pipeline.image.hint_dpr` (2) pixels per
CSS pixel (`pipeline.image.page_hints` turns it off). It works for pages that
reach the proxy, which is not the case for a page the browser has in its cache.

`saver` and `extreme` also minify JavaScript (`balanced` and `lossless` do not),
and `saver` and `extreme` wait longer for the slow brotli levels.
Every one of these is a field of the profile (`effort`, `no_transform`,
`minify_js`, `text.static_wait_ms`) that can be changed in the interface or in
`pipeline.profiles`.

#### no-transform

`Cache-Control: no-transform` asks proxies not to change a response. RFC 9111 made
it for operators that recoded content on the way to a person. Here the proxy is the
one the person chose, and an image is only replaced by a result that was measured
against it, so `pipeline.no_transform` says what to do:

| value | what it does |
|---|---|
| `profile` (default) | what the profile says: `lossless` and `balanced` respect it, `saver` and `extreme` recode the images that carry it |
| `respect` | always keep it |
| `images` | recode the images that carry it, in every profile |
| `all` | recode text too, which can break files that are signed or hashed |

A rule can change it for a site (`set: {no_transform: images}`). A request that
says no-transform is always respected, and so are range requests, SVG and fonts
unless the value is `all`. The responses that were recoded anyway are counted in
`midopress_pipeline_no_transform_overridden_total`. The risk is a URL that is
signed for exactly those bytes; if a site breaks, add a rule for it.

### Speed

WebAssembly is about ten times slower than native code for AVIF and JPEG XL, a
second or more per megapixel. The proxy copes in two ways. An image that does not
fit in `pipeline.image.timeout` is sent as it is (or as a fast jpegli/WebP
recoding) and finished in the background, and the next request gets the best
result from the cache. And if the shared libraries of libwebp, libavif and libjxl
are installed, the codecs find and use them without cgo, which is 3 to 10 times
faster (on three WebP and AVIF files, 1.7 s per image in `balanced` against 6.6 s
with WebAssembly, and 0.77 s in `extreme` against 2.4 s). The Docker image can
include them with `--build-arg NATIVE_CODECS=1`; it is off by default because one
stress run of that path (about ten runs in total) once crashed inside the C
library bindings. The same test was repeated on a current system with ten runs of
40 to 60 s (`TestNativeCodecsUnderGCStress`, and did not crash, but that is not a
proof, so the service should restart by itself if it dies.
The libjxl library also lets the proxy recompress JPEG files into JPEG XL without
losing a bit (15.9% smaller on the test corpus), which nothing else here does.

The libraries are found by the name `libavif.so`, `libwebp.so`, `libwebpdemux.so`
and `libjxl.so`. A system that only has the versioned files (`libavif.so.16`) and
no development packages can still use them without root, with a directory of
links:

```sh
mkdir -p ~/.midopress/native && cd ~/.midopress/native
ln -sf /usr/lib64/libavif.so.16 libavif.so
ln -sf /usr/lib64/libwebp.so.7 libwebp.so
ln -sf /usr/lib64/libwebpdemux.so.2 libwebpdemux.so
ln -sf /usr/lib64/libjxl.so.0.12 libjxl.so
LD_LIBRARY_PATH=~/.midopress/native midopress
```

`/api/v1/about` says which formats use them (`native`). AVIF takes the effort of
the profile (`avif_effort`: 4 in `balanced`, 5 in `saver` and `extreme`) when it
runs natively, and never more than 2 with WebAssembly. On 26 photographs, against
the old default of 3, 4 saved 0.6 to 1.2 points more of the bytes at the same
time in `balanced`, and 5 saved 1.4 to 2.7 more in about twice the time; 6 was
slower again and, in `saver`, saved less, so it is not offered as a default.

### libvips backend

A binary built with `-tags vips` (it needs cgo and libvips with its development
files) can encode and decode with libvips, which is 2 to 12 times faster than the
WebAssembly codecs; choose it with `pipeline.image.backend: vips`.
`go build -tags vips ./cmd/midopress`. The default build has no C dependency and
does not include it.

### Rules

Rules say what happens to a request or a response, in order, with the filter
language of mitmproxy (`~d` domain, `~u` URL, `~t` content type, `~c` status,
`~m` method, `~h` headers, `~b` body, `~src` and `~dst` addresses, with `&`, `|`,
`!` and parentheses):

```yaml
rules:
  - match: '~d (bank|banco|paypal|health)\.'
    action: passthrough_tls          # do not decrypt it at all
  - match: '~u \.(mp4|webm|m3u8|ts)$ | ~t video/'
    action: passthrough              # send it as it is
  - match: '~d cdn\.example\.com & ~t image/'
    profile: extreme
  - match: '~u /app\.js$'
    set: {minify_js: true}
```

`profile` and `set` add up as the rules go by; the first `action` ends the
evaluation. An invalid rule is reported with its position, and the proxy does not
start with one. The profile of a request comes from, in this order, the profile of
the device token (`midopress device add -profile`), the `X-MidoPress-Profile`
header, a rule, `Save-Data: on` and the default. You can also change the built-in
profiles or define new ones under `profiles:`; see the example configuration.

Rules, profiles, `mitm.passthrough`, the default profile, minification and the log
level are applied without a restart, when the configuration file changes or on
`SIGHUP`. A new configuration that does not validate changes nothing.

### Cache

Recoded images, fonts and slowly compressed text are kept by the content of the
original, in memory and, if `cache.dir` is set, on disk, where they survive
restarts. The origin's `Cache-Control` and `Expires` choose how long, between
`cache.min_ttl` and `cache.max_ttl`, and the disk is kept under `cache.max_size`.

### Metrics

`/metrics` has the Prometheus text format: requests and bytes, what the pipeline did
with each response and why (`midopress_pipeline_reason_responses_total` and
`midopress_pipeline_reason_bytes_total` by reason and kind of content, and the
`cause` versions that say why it was busy, which media types it does not handle
and who forbade a transformation), the bytes of the HTTPS tunnels that are not
decrypted, the memory budget and what is reserved, the time of each step, cache
hits and hit rate, the background queue, and the Go and process collectors. It is served on the proxy
port to the clients the proxy accepts, or on its own listener with
`metrics.addr`.

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

## Administration interface

MidoPress serves a web interface on `127.0.0.1:9998` (`web.addr`): a dashboard
that separates what it can shrink (images, text, fonts) from video and audio,
which it passes as they come, and from the HTTPS tunnels it never opens, with
the reasons and causes of the bytes that were not saved and, if you turn the
tracking of domains on, the hosts they came from; the devices
that are connected and what they ask for; the live traffic with a comparison of
each image against what it became; an editor of rules and profiles that applies
without a restart; the certificate; the log, whose level changes while running;
and the service controls. The first start makes a token and prints a sign-in link
once on stderr. Later, `midopress admin token` shows it again. What you change
in the interface is saved to `~/.midopress/ui.yaml` and wins over the
configuration file. The API is in [docs/09-admin-api.md](docs/09-admin-api.md).

Anyone with the token can change the configuration, so keep `web.addr` on the
loopback, or put TLS in front of it. The live list of flows shows URLs: it is off
until you turn it on, it only lives in memory, and cookies and authorization
headers are never kept.

When the proxy has a CA, a device that uses it can open `http://midopress.it`:
the proxy answers that name by itself with a page that downloads the certificate
(PEM, DER, PKCS#12), shows a QR, explains the steps for Android, iOS, Windows,
macOS, Linux and Firefox, and checks whether the certificate is trusted.

To work on the interface, run `npm run dev` in `web/`: Vite serves it on 5173
with live reload and forwards `/api` to the running MidoPress (`MIDOPRESS_ADMIN`
changes where).

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

`scripts/deploy.sh` builds the interface (when its sources changed) and the
binary, and restarts the `midopress` user service. `web/scenarios/` measures what
the proxy does with real sites and with a fixed set of files, against any binary:

```sh
cd web
node scenarios/run.mjs --bin ../bin/midopress --label after      # news, social feed, shop, GitHub, YouTube, HLS
node scenarios/replay.mjs --bin ../bin/midopress --dir ../artifacts/corpus-images --label images   # the same files, cold and warm
node scenarios/table.mjs ../artifacts/scenarios/after.json       # the results as Markdown
```

Real sites change from one run to the next: compare two binaries in the same
hour (the runs alternate them), and use `replay.mjs`, which serves the same bytes
every time, for what the code did. It needs Chromium from Playwright
(`npx playwright install chromium`) for `run.mjs`.

## What changed from compy

- The module is `github.com/goastian/midopress` and the binary is `midopress`.
- Go 1.27 instead of 1.9 and 1.16, and brotli in pure Go.
- The proxy was rewritten: WebSockets work, responses that must not change are
  left alone, a failed conversion falls back to the original, there are
  timeouts and limits, authentication is not open to timing attacks, and the
  certificate for each HTTPS site is made properly. The list of what was wrong
  with compy, and what happened to each item. 

- Behavior you may notice: `block_global` is on, unknown paths on the proxy
  itself answer 404 instead of 501, HTTP/2 clients can ask for another host the
  way browsers do, and the quality header is `X-MidoPress-Quality` (the old
  `X-Compy-Quality` still works for now).
- The license went from ISC to Apache 2.0. The parts that come from compy are
  still the work of its author, and their copyright notice is in
  [NOTICE](NOTICE).

## Benchmark

`midopress bench DIR` recodes every image in a directory for each client and
profile, then decodes every result again and measures it with the dssim
implementation, apart from the engine. `go run ./tools/fetch-images` downloads the
corpus used here (62 images: 25 JPEG photographs as web servers deliver them, 29
PNG photographs, 6 graphics and 2 animated GIFs; the images are not part of the
repository). Balanced profile (dssim at most 0.002), WebAssembly codecs:

| client accepts | whole corpus | web JPEG | PNG photos | results above 0.002 |
|---|---:|---:|---:|---:|
| JPEG only | 65.2% | 38.5% | 87.1% | 0 of 47 |
| WebP | 69.3% | 39.5% | 89.9% | 0 of 53 |
| JPEG XL, AVIF, WebP | 71.9% | 45.1% | 91.4% | 0 of 53 |

Saver saves 74% to 81% and extreme 83% to 90% over the whole corpus, see
[CHANGELOG](CHANGELOG.md). Recoding a JPEG that is already well compressed saves
less than recoding a PNG photograph, which is why the web JPEG column is lower:
the honest number for web JPEGs at the balanced profile is 38% to 45%. Two JPEG
photographs carry a wide-gamut colour profile and are left as they are.

With the cache, going over the same images again (`midopress bench -passes 2`)
answers every request from it: a hit rate of 100% and a 95th percentile of 2.3 ms.
The first pass saves 52% within the 3 seconds a request waits, and 71% once the
background work is done.

## Credits

Barna Csorogi wrote compy. The image codecs come from
[gen2brain](https://github.com/gen2brain) (libwebp, libavif, libjxl and jpegli
compiled to WebAssembly) run with [wazero](https://wazero.io),
fonts are repackaged with [tdewolff/font](https://github.com/tdewolff/font),
[brotli](https://github.com/andybalholm/brotli) and
[klauspost/compress](https://github.com/klauspost/compress), and minification
from [tdewolff/minify](https://github.com/tdewolff/minify).

## License

Apache 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
