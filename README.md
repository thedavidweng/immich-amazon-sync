# immich-amazon-sync

A self-hosted, one-way backup service: **Immich originals -> durable SQLite
queue -> aphotos Go SDK -> Amazon Photos**. Run one container with one persistent
data volume. Select source albums by ID; whole-library backup requires an
explicit choice.

Amazon Photos has no official public API. This service uses an unofficial SDK;
sessions expire and upstream behavior can change. Keep an independent backup.
Offline tests and read-only diagnostics do not prove real cloud uploads.

Eligible external-library originals and assets with unknown source checksum
provenance use MD5 and byte size computed from the staged original; their source
checksum is not treated as a verified byte checksum. Review the
[compatibility limits](docs/reference/compatibility.md) before deployment.

Media is never deleted from Amazon by this service. Removing a source asset,
album, rule or account does not remove its cloud backup. Optional media deletion
is deferred to [ticket #8](https://github.com/thedavidweng/immich-amazon-sync/issues/8).
Managed album membership is separate from media.

An original that already exists elsewhere in the same Amazon account (another
folder, or only inside an album) is reused instead of uploaded again. Reuse
requires exactly one AVAILABLE, owned node with matching MD5 and byte size,
re-read by ID; the staging file is released, the node mapping is kept and a
requested album membership is still applied. Status counts that asset as
`existing`, not `confirmed`. Zero or multiple matches, mismatched metadata,
unproven ownership and incomplete pagination are never reused and never counted
as `existing`; a fresh asset still uploads only when the complete lookup proves
no copy, while an uncertain write stays `unconfirmed` unless one verified match
is found.

## Compose quickstart

Requires Docker Engine with Compose v2+ and buildx, on Linux amd64 or arm64.

```sh
git clone https://github.com/thedavidweng/immich-amazon-sync.git
cd immich-amazon-sync
cp docs/reference/config.example.json config.json
mkdir -m 700 secrets
docker compose build
```

Create `secrets/immich-api-key` and `secrets/amazon-cookie` using the
[credential guide](docs/how-to/credentials.md). These contain a source API key
and the destination Cookie request-header value, respectively. On Linux, make
both files owned by UID/GID `10001:10001` with mode `0400`. Edit `config.json`
with your source URL, actual Amazon marketplace and selected album IDs.
The example UUID and hostname are placeholders; it contains no credentials.

```sh
docker compose run --rm sync --config /etc/immich-amazon-sync/config.json albums list
docker compose run --rm sync --config /etc/immich-amazon-sync/config.json doctor --json
# This command writes to Amazon; use it only after reviewing the chosen scope.
docker compose run --rm sync --config /etc/immich-amazon-sync/config.json sync once --json
docker compose up -d
docker compose exec sync wget -q -O - http://127.0.0.1:8080/readyz
```

Follow the [first-backup tutorial](docs/tutorials/first-backup.md) for every setup
step. Compose builds the local image, mounts credentials read-only and initializes
the named volume for nonroot UID 10001. It publishes port 8080 only on
`127.0.0.1`; the process listens on all container interfaces. Do not run `sync once`
against the live daemon's data volume.

## Local management

The dashboard is optional; without `ADMIN_PASSWORD_FILE` (or the JSON
`admin_password_file` path), its routes are absent. Keep the existing
Compose quickstart for headless operation. For browser-based first setup, use
explicit `serve --setup --config /data/config.json` with a mounted admin
passphrase file and a writable config directory. Saving an account activates
its scheduled work; no source scope is selected implicitly.

See [local setup and management](docs/how-to/local-management.md) for the
container commands, read-only versus editable modes and session security.
Do not expose the dashboard publicly; publish only on host loopback.

## Documentation

| Need | Read |
| --- | --- |
| Learn the first backup workflow | [Tutorial](docs/tutorials/first-backup.md) |
| Obtain or renew credentials | [Credentials](docs/how-to/credentials.md) |
| Set up or use the optional dashboard | [Local management](docs/how-to/local-management.md) |
| Back up, restore, upgrade or fix permissions | [Operations](docs/how-to/operations.md) |
| Diagnose a blocked or uncertain transfer | [Troubleshooting](docs/how-to/troubleshooting.md) |
| Look up JSON fields and environment variables | [Configuration](docs/reference/configuration.md) |
| Look up CLI, exits and health behavior | [CLI](docs/reference/cli.md) |
| Set up trusted event hints and incremental polling | [Event hints](docs/how-to/event-hints.md) |
| Understand managed album membership and uncertainty | [Managed albums](docs/how-to/managed-albums.md) |
| Interpret progress counters and scheduling | [Status](docs/reference/status.md) |
| Assess versions, regions and media limitations | [Compatibility evidence](docs/reference/compatibility.md) |
| Understand identities and deletion safety | [Safety model](docs/explanation/safety.md) |
| Understand deployment and upstream contract choices | [ADRs](docs/adr/) |

[Rainforest](https://github.com/thedavidweng/rainforest) handles desktop local-file
backup and recovery. [aphotos](https://github.com/thedavidweng/amazon-photos) owns
the reusable Amazon API implementation. This service owns Immich orchestration
and its own database; it does not share Rainforest's state or run its desktop UI.
The scenarios were informed by
[vanessa/immich-amazon-sync](https://github.com/vanessa/immich-amazon-sync) and
[immich-gphotos](https://github.com/ImAleeexx/immich-gphotos).

See [CONTRIBUTING.md](CONTRIBUTING.md) for the offline quality gate and container
tests. Licensed **AGPL-3.0-only**: [LICENSE](LICENSE). The MIT SDK attribution and
full license text are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
