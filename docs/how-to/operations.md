# Operate, back up and restore

Run exactly one writer per data directory. Do not scale the Compose service.
Do not use Rainforest's database or credentials directory as this service's
volume. A named volume is recommended; image upgrades must reuse it.

For a host bind mount instead, create its directory before starting and set
owner `10001:10001`, mode `0700`. A new named volume inherits that ownership
from the image; an existing incorrectly owned volume does not. Fix its ownership
while the service is stopped, then run the normal image as nonroot.

## Limit transfer hours

Each account may set one optional daily upload window in an IANA timezone:

```json
"transfer_window": {"timezone": "America/New_York", "start": "23:00", "end": "06:00"}
```

`start` is inclusive and `end` exclusive; `start` later than `end` crosses
midnight. Omit the object for continuous transfer. Equal bounds are rejected;
`Local` and unknown zones are rejected. The zone database is embedded, so no
container tzdata package is required.

Only new uploads pause. Scans, metadata and existing-content checks, album
reconciliation and deletion passes keep running, and an upload already streaming
finishes without becoming uncertain. Deferred uploads are not failures and do
not consume retries. Status and `sync once` report `deferred`, the window's
open/closed state, `next_opening` and `deferral_reason`; the scheduler wakes at
the next DST-correct opening. Editing the window in the dashboard or saving the
config applies to the next dispatch without restarting `serve` or cancelling
in-flight streams. Two accounts with different windows stay independent.

Confirm the window's local time against your own clock: `next_opening` is
computed in the configured zone, including DST transitions.

## Monitor metrics

Metrics are optional and off by default. Set `metrics: true` and keep the
listener private; a loopback `listen` address needs no further configuration:

```json
{"listen": "127.0.0.1:8080", "metrics": true}
```

To bind a non-loopback address, mount a bearer token and require it on every
scrape:

```json
{"listen": "0.0.0.0:8080", "metrics": true, "metrics_token_file": "/run/secrets/metrics_token"}
```

`GET /metrics` returns Prometheus text exposition and `GET /status` returns the
same durable report as `status --json`. Both answer `404` while metrics are
disabled, and both require `Authorization: Bearer <token>` when a token file is
configured. The container image sets `LISTEN=0.0.0.0:8080`, so enabling metrics
there requires `METRICS_TOKEN_FILE` (or a loopback `LISTEN` override). Labels are
the hashed account plus finite state, code, reason,
category and mode values, so no filename, asset UUID, raw account name, URL or
secret is exposed.

Durable counts come from the ledger. Direct/API byte counters and direct-read
fallback/failure counters are process-local and reset when the process
restarts. Health and readiness never depend on Amazon availability, and a
scrape failure does not pause backups. Do not infer a subscription cap or
remaining capacity from usage counters; they are observations only.

## Back up

Stop writes before archiving SQLite, its WAL/SHM files, staging and identity
mappings together. Copying just a live `.db` file is not a consistent backup.
The following uses BusyBox tar in the application image:

```sh
umask 077
docker compose stop sync
docker compose run --rm --no-deps -T --entrypoint tar sync \
  -C /data -czf - . > immich-sync-data.tgz
docker compose up -d sync
```

Check tar's exit status and inspect the archive before relying on it. Protect
and separately back up `config.json` plus all configured secret files with
encryption and restricted access; the data archive does not include those bind
mounts. The [editable UI deployment](local-management.md) instead keeps its
config under `/data/config.json`, so that file is included in its data archive;
its mounted admin/source/destination secrets still need a separate backup.
Keep the image version/digest with each backup. Staged originals are private
media. Restrict access to the archive and the entire volume.

## Restore without overwriting the old volume

Stop the original deployment. In the same directory, use a new Compose project
name so its `data` volume is new. Keep the original volume for rollback:

```sh
docker compose stop sync
docker compose -p immich-sync-restore run --rm --no-deps -T --entrypoint tar sync \
  -C /data -xzf - < immich-sync-data.tgz
docker compose -p immich-sync-restore run --rm sync \
  --config /etc/immich-amazon-sync/config.json doctor --json
docker compose -p immich-sync-restore up -d sync
```

Use the matching backed-up config and credentials. Thereafter use
`-p immich-sync-restore` for this deployment. Do not start the old project too.
Never extract an untrusted archive. Rehearse recovery with disposable data first.
Old snapshots may lack recent mappings; existing uncertain jobs reconcile before
writing, but an old snapshot cannot establish evidence it never recorded.
Do not claim that restoring arbitrary stale state guarantees no duplicates.

## Upgrade or move

Back up first, stop the old instance, update source and run `docker compose build`,
then `docker compose up -d sync`. Preserve project name, volume, account IDs and
credentials. Run diagnostics and inspect account status after starting.

After an image is deliberately published, use its exact version or digest in
`image:` and remove `build:` in your deployment copy if you want registry pulls.
There is no `latest` release contract. Do not create a Git tag merely to deploy a
local build. Downgrading a binary across a database schema change may fail;
restore the matching backup/image together rather than editing SQLite by hand.

The P1 upgrade migrates SQLite schema 1 to 2 in place, preserving confirmed media
and account history while adding durable hints, incremental progress and album
relationships. Later transactional steps add album scope/origin columns (4),
managed Workflow ownership (5), favorite state (6), explicit filter exclusions
(7), the normalized categorized usage snapshot (8), the favorite drift-audit
column (9), and the Live Photo pair ledger with its applied per-account mode
(10). Historical mappings, cursors, rules, album ownership,
confirmed/existing state, deletion state, staging references and unconfirmed
intents are preserved, and no migration widens scope or enables a new write. The
upgrade path is exercised from every checked-in prior fixture under
`internal/syncer/testdata`; the container smoke also migrates a checked-in
version-3 ledger and proves its mapping survives. The real deployed ledger in
this project was schema 2, and a copy of it upgraded on 2026-10-22 with all
seven rows, their states and their node mappings intact and every new feature
still off.

An unknown schema version fails closed. This binary refuses a ledger whose
`user_version` is newer than schema 10, or otherwise out of range, with the
finite `unsupported_schema` code and exit 4. It does not rewrite the version or
attempt a partial migration, so a downgrade needs the matching backup and image.
Take the stopped complete-volume backup before first startup with the new binary.
To roll back, restore that pre-upgrade volume together with its matching
binary/config; an older binary cannot read the newer schema.
Do not delete migration state, queues or album intents to force a downgrade.

When migrating hosts, move the stopped complete data archive, config and secrets.
Ensure the old writer remains stopped before the new one starts. Keep host port
binding on `127.0.0.1`; use authenticated access through a trusted network or
protected tunnel for remote administration. The dashboard does not trust
forwarded headers; do not assume proxy TLS sets its backend cookie Secure.
Review the [session/exposure limits](local-management.md#sessions-and-exposure).
