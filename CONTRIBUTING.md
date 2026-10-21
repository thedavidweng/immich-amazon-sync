# Contributing

Read `AGENTS.md`, `GLOSSARY.md`, the relevant `docs/adr/` decisions and
`docs/spec.md` before changing behavior. Track work in GitHub Issues; reference
the ticket in commits and leave verification evidence. Never commit credentials
or private media.

```sh
mise install
mise run check
```

The single read-only gate runs stock `gofmt`, module tidy drift detection,
`go vet`, build, offline tests, the race detector, golangci-lint and jactionlint
workflow safety/syntax checks. Without mise, install the versions in `mise.toml`
and run `sh scripts/check.sh`. Use `mise run fmt` to format intentionally.
Race tests need a host C compiler; shipped binaries use `CGO_ENABLED=0`.

Tests exercise public sync use cases, adapter HTTP contracts, the CLI and
containers. Use fake external systems with real temporary SQLite/filesystems.
Write a failing behavior test before implementation, then the smallest passing
change. The CLI and container seams are already agreed in ADR 0001. No test needs
a real API key, Cookie, Immich server or Amazon account.

Streaming memory checks are compositional: a guarded 5 GiB source stops at a
64 KiB staging quota without uploading, a generated 32 MiB original completes
the engine's real SQLite/staging flow, and a generated 5 GiB original completes
SDK upload plus remote verification through an HTTP transport sink. Tests bound
Go `TotalAlloc`, not RSS, and allocate no whole media payload. This is not a full
successful 5 GiB engine run. The SDK test still performs O(5 GiB) hashing work.

After the runtime builds, run the container contract:

```sh
mise run smoke
# One native platform:
sh scripts/smoke.sh linux/arm64
# Homebrew plugin outside Docker's plugin search path:
BUILDX=/opt/homebrew/lib/docker/cli-plugins/docker-buildx sh scripts/smoke.sh
```

The smoke script builds and loads each architecture separately, asserts the
image architecture and nonroot UID, checks credential-free help/version,
checks that the build version reaches the CLI, parses the documented config
through `doctor` with absent credential files,
starts isolated zero-account and explicit `serve --setup` services with
`--network none`, checks Docker health and both probes, persists a volume marker
across restart and checks clean SIGTERM. At the container HTTP seam it obtains a
pre-auth CSRF token, rejects dashboard access before authentication, checks
cookie/security headers, signs in, reads the authenticated dashboard, rejects
login and logout without CSRF, and verifies logout revokes the session. Setup
does not create a config until an account is saved. It uses a disposable
synthetic admin secret and removes its test containers/volumes. It leaves the
built smoke images for inspection.
Cross-architecture execution needs host emulation; CI uses native amd64 and
arm64 runners. This checks local lifecycle, not a live cloud transfer.

The adapter's pinned Immich contract fixture/test runs in `go test ./...`; keep
its provenance with that fixture rather than adding another downloaded contract
to CI. Test fixture updates separately from upstream monitoring. PR checks
must remain deterministic.

CI uses SHA-pinned official checkout/setup-go/buildx actions and the upstream
jactionlint action. Maintainers publish only by deliberately pushing a new
`vMAJOR.MINOR.PATCH` tag. The publication workflow reruns CI, refuses existing
version/commit image tags, then publishes `ghcr.io/thedavidweng/immich-amazon-sync`
for both Linux architectures, tagged with that version and `sha-<full commit>`.
Publication jobs are serialized so two release tags cannot race to write the
same commit image tag.
It creates neither a Git tag nor a GitHub Release, and publishes no `latest` tag.
Registry tags rely on maintainer policy; use the resulting image digest for
deployment immutability. Configure package visibility in GHCR before announcing
a public image. A failed manifest inspection other than a missing manifest stops
publication.

By contributing, you license your contribution under **AGPL-3.0-only** as
specified in `LICENSE`. Preserve the SDK's full MIT notice.
