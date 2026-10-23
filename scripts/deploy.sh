#!/usr/bin/env bash
# Builds MidoPress and restarts the user service with the new binary.
# The web interface is rebuilt when its sources are newer than the built copy
# (or with WEB=1). Set NORESTART=1 to only build.
set -euo pipefail
cd "$(dirname "$0")/.."

dist=internal/webui/dist/index.html
if [ "${WEB:-0}" = 1 ] || [ ! -f "$dist" ] || [ -n "$(find web/src web/index.html web/package.json -newer "$dist" -print -quit)" ]; then
	echo "building the web interface"
	(cd web && npm run build >/dev/null)
fi

echo "building midopress"
go build -o bin/midopress.new ./cmd/midopress
mv -f bin/midopress.new bin/midopress

[ "${NORESTART:-0}" = 1 ] && exit 0

systemctl --user restart midopress
for _ in $(seq 1 50); do
	if curl -fsS -m 1 http://127.0.0.1:9999/health >/dev/null 2>&1; then
		echo "midopress is up: $(bin/midopress version 2>&1 | head -1)"
		exit 0
	fi
	sleep 0.2
done
echo "midopress did not answer on :9999" >&2
systemctl --user status midopress --no-pager | tail -15 >&2
exit 1
