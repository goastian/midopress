# syntax=docker/dockerfile:1

FROM golang:1.27-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/midopress ./cmd/midopress

FROM debian:trixie-slim
# Optional native image codecs: build with --build-arg NATIVE_CODECS=1. When the
# shared libraries of libwebp, libavif and libjxl are found, the image codecs
# run native code, about ten times faster than the WebAssembly ones inside the
# binary. It is off by default because in testing a stress run of the native
# path crashed once in about ten runs (see docs/05-work-plan.md, phase 2).
ARG NATIVE_CODECS=0
RUN apt-get update \
    && apt-get install -y --no-install-recommends openssl ca-certificates \
    && if [ "$NATIVE_CODECS" = "1" ]; then \
         apt-get install -y --no-install-recommends libwebp7 libwebpdemux2 libwebpmux3 libavif16 libjxl0.11 \
         && for l in webp webpdemux webpmux avif jxl; do \
              f=$(ls /usr/lib/*/lib$l.so.* | head -1); ln -sf "$f" "$(dirname "$f")/lib$l.so"; \
            done; \
       fi \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 --home-dir /var/lib/midopress --create-home midopress
COPY --from=build /out/midopress /usr/local/bin/midopress
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh

ENV CERTIFICATE_DOMAIN=localhost
USER midopress
WORKDIR /var/lib/midopress
VOLUME /var/lib/midopress
EXPOSE 9999
ENTRYPOINT ["entrypoint.sh"]
