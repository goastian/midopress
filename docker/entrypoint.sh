#!/bin/sh
# Creates the proxy certificate and the MITM CA the first time the container
# runs and keeps them in the data volume, so clients only trust the CA once.
# Everything else is configured with MIDOPRESS_* environment variables, or with
# a YAML file named by MIDOPRESS_CONFIG.
set -eu

umask 077
data="${MIDOPRESS_DATA:-/var/lib/midopress}"
cd "$data"

# browsers ignore the CN and look at subjectAltName
case "$CERTIFICATE_DOMAIN" in
    *[!0-9.]*) san="DNS:${CERTIFICATE_DOMAIN}" ;;
    *) san="IP:${CERTIFICATE_DOMAIN}" ;;
esac

if [ ! -f cert.crt ] || [ ! -f cert.key ]; then
    openssl req -x509 -newkey rsa:2048 -nodes \
        -keyout cert.key -out cert.crt -days 3650 \
        -subj "/CN=${CERTIFICATE_DOMAIN}" -addext "subjectAltName=${san}"
    echo "generated cert.crt for ${CERTIFICATE_DOMAIN}"
fi

if [ ! -f ca.crt ] || [ ! -f ca.key ]; then
    midopress ca init -dir "$data" -name "MidoPress CA (${CERTIFICATE_DOMAIN})"
fi

if [ -n "${MIDOPRESS_CONFIG:-}" ]; then
    set -- -config "$MIDOPRESS_CONFIG" "$@"
fi

# the administration interface must be reachable from outside the container;
# its token is in $data/admin.token (docker exec ... midopress admin token)
export MIDOPRESS_WEB_ADDR="${MIDOPRESS_WEB_ADDR:-0.0.0.0:9998}"
export MIDOPRESS_WEB_STATE_DIR="${MIDOPRESS_WEB_STATE_DIR:-$data}"

exec midopress -host :9999 \
    -cert cert.crt -key cert.key \
    -ca ca.crt -cakey ca.key \
    "$@"
