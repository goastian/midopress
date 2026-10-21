# syntax=docker/dockerfile:1

FROM golang:1.27-trixie AS build
RUN apt-get update \
    && apt-get install -y --no-install-recommends libjpeg-dev \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=1 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/midopress ./cmd/midopress

FROM debian:trixie-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends libjpeg62-turbo openssl ca-certificates \
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
