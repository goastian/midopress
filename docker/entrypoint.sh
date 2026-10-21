#!/bin/sh
# Creates the proxy certificate and the MITM CA the first time the container
# runs and keeps them in the data volume, so clients only trust the CA once.
set -eu

umask 077
cd "${MIDOPRESS_DATA:-/var/lib/midopress}"

# browsers ignore the CN and look at subjectAltName
case "$CERTIFICATE_DOMAIN" in
    *[!0-9.]*) san="DNS:${CERTIFICATE_DOMAIN}" ;;
    *) san="IP:${CERTIFICATE_DOMAIN}" ;;
esac

gen() {
    # $1 = base name (cert or ca), $2 = extra openssl arguments
    if [ -f "$1.crt" ] && [ -f "$1.key" ]; then
        return
    fi
    # shellcheck disable=SC2086
    openssl req -x509 -newkey rsa:2048 -nodes \
        -keyout "$1.key" -out "$1.crt" -days 3650 \
        -subj "/CN=${CERTIFICATE_DOMAIN}" $2
    echo "generated $1.crt for CN=${CERTIFICATE_DOMAIN}"
}

gen cert "-addext subjectAltName=${san}"
gen ca ""

exec midopress -host :9999 \
    -cert cert.crt -key cert.key \
    -ca ca.crt -cakey ca.key \
    "$@"
