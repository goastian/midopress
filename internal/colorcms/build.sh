#!/bin/sh
set -eu
VERSION=2.19.1
SHA256=267705e278e2f7c2fb886c259dadcbaeb2be52748bcbc71c79f08aacacb7a709
SDK=${WASI_SDK_PATH:-/opt/wasi-sdk}
BUILD=$(mktemp -d)
trap 'rm -rf "$BUILD"' EXIT
HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
curl -fL "https://github.com/mm2/Little-CMS/archive/refs/tags/lcms${VERSION}.tar.gz" -o "$BUILD/source.tar.gz"
echo "$SHA256  $BUILD/source.tar.gz" | sha256sum -c -
tar -xzf "$BUILD/source.tar.gz" -C "$BUILD" --strip-components=1
"$SDK/bin/clang" -O2 -DCMS_NO_PTHREADS -DCMS_NO_HALF_SUPPORT \
    -I"$BUILD/include" "$HERE/wasm/bridge.c" "$BUILD"/src/*.c \
    -Wl,--no-entry -Wl,--export=malloc -Wl,--export=free -Wl,--export=cms_convert -Wl,--export=cms_rgb_profile \
    -Wl,--export-memory -Wl,-z,stack-size=1048576 -Wl,--max-memory=536870912 \
    -lm -o "$HERE/lcms2.wasm"
