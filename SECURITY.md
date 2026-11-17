# MidoPress Security

This policy covers the proxy, local administration, `/img`, its workers, and
the artifacts built from this repository. Operational procedures and
TLS/MITM limitations are detailed in
[Security and Operation](docs/19-seguridad-operacion.md).

## Reporting an Issue

Please use the private channel provided via the repository's
[Security tab](https://github.com/goastian/midopress/security).
If no private mechanism is available, open an issue requesting a contact channel;
do not include exploitable details, credentials, CA keys, private URLs,
session captures, or personal data. This repository does not document a
dedicated security mailbox or a guaranteed response timeframe.

When reporting, include the revision or version, platform, build tags,
backend, affected components, a redacted minimal configuration, and a
local reproduction using synthetic data. Specify which trust boundary is
crossed and the observed effect. Coordinate disclosure with the maintainers
before publishing any details that could impact third-party installations.

## Versions and Updates

Acceptance of this release applies to the code and platforms specified in
its test evidence. The project does not guarantee security maintenance for
all previous versions, nor does it claim to have undergone an exhaustive
audit. To maintain an installation, monitor project and Go security advisories,
as well as those for dependencies and native libraries; rebuild with the
necessary fixes, test a release candidate, and verify its hash/SBOM before
replacing the existing binary. Modifying `go.mod` does not update an
executable that is already running.

Go versions are pinned via `go.mod`/`go.sum`, frontend versions via
`web/package-lock.json`, and custom WASM sources via their respective
scripts and manifests. `scripts/sbom.py` generates a deterministic CycloneDX 1.6 SBOM for each binary; the `--verify-codecs` option detects an outdated codec inventory. The Service screen distinguishes between versions detected in loaded libraries and those declared in the recipes for embedded modules. An `unreported` value indicates a lack of provenance, not a secure version.

Optional system libraries should also be updated via the distribution's package manager. Ensure a system restart is performed during a scheduled maintenance window following an update, as a running process may keep the previous version of a library in memory. Updates are neither automatically downloaded nor installed during system startup.

## Configuration and Sensitive Data

Limit administrative access to the loopback interface, and use TLS and authentication for any authorized remote access. The `auth: none` mode is not intended for exposing a proxy to the Internet. For shared installations, restrict destinations and set usage quotas. Capturing request/response bodies, data streams, or URL logs requires an explicit decision by the operator; these may contain browsing information, even if certain headers are masked.

Installing a MITM CA means placing trust in whoever holds its private key. The `/img` API allows for the optimization of public images without requiring the installation of this CA. While the public certificate may be distributed, the `ca.key` file, administrative tokens, and the HMAC key used for the `/img` API must remain private. Neither the SBOM nor any generated report should include these files.

If a CA compromise is suspected, follow the trust-revocation and key-rotation procedures outlined in the operational documentation. Simply restoring a key that may have been leaked does not restore trust. There is no provision for the remote revocation of an installed root certificate.