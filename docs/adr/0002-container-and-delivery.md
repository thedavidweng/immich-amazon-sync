# 0002: Single nonroot container and explicit OCI publication

Status: accepted. Date: 2026-10-20.

Build one static Go executable with `CGO_ENABLED=0` for linux/amd64 and linux/arm64.
Use a small Alpine runtime for CA certificates, BusyBox `wget`, and operational
archive commands. Run as numeric UID/GID 10001:10001. Initialize `/data` with
that ownership and mode 0700 so a new named volume works without a root init
container. Existing volumes and bind mounts require operator-owned permissions.

Use JSON configuration plus separate read-only credential files. Compose
file-backed secrets retain host ownership; do not claim its uid/gid/mode settings
rewrite bind-mount permissions. A read-only root filesystem, writable `/data`,
small `/tmp` tmpfs and dropped capabilities are sufficient for normal operation.
There is one writer, not a replica set.

Native defaults bind loopback. Container `LISTEN=0.0.0.0:8080` makes the service
reachable through Docker networking; Compose publishes only host 127.0.0.1.
Probe both `/healthz` and `/readyz` with local wget. Remote failures remain
diagnostic states, not healthcheck credential leaks or forced restarts.

Use stock Go format/vet/build/test/race/tidy checks and the existing project's
golangci-lint convention. One shell gate and mise task are enough. Pin workflow
actions by full upstream commit SHA; jactionlint checks safety and syntax.
The adapter owns its pinned Immich contract test; delivery CI just runs it.

Only an explicitly pushed semantic-version tag starts OCI publication. Run
quality and native platform smoke jobs first, then push exact version and full
commit image tags after checking they do not exist. Do not create Git tags,
GitHub releases, mutable latest tags or automatic production deployments.
GHCR tags are not intrinsically immutable; digests are the deployment identity.
Publish no image or release as part of implementing this ticket.

**2026-10-28 update:** Static OpenAPI snapshot assertions were removed; adapter
tests exercise the supported request and response behavior instead. Upstream
contract changes still require review against the documented source pins.

**2026-10-30 update:** `mise.toml` is the single Go compiler pin. The CI check
job installs that toolchain through mise and sets `GOTOOLCHAIN=local`, so it
fails rather than fetching a different compiler. The Docker builder reads
`mise.toml`, downloads the matching official Go distribution from
`dl.google.com`, verifies the published SHA-256, and sets `GOTOOLCHAIN=local`;
there is no second Go image pin. The archive and its `.sha256` come from the same
upstream host, so verification catches corrupt or substituted downloads in
transit but not a compromised origin; a committed checksum would reintroduce the
second pin this decision removes. `go.mod` retains the minimum supported Go
version; it may trail the compiler pin but must never exceed it, and
`GOTOOLCHAIN=local` makes Go reject that case. The build version still reaches
the CLI through the existing `-X main.version` linker flag.
