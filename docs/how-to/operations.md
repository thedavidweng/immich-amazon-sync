# Operate, back up and restore

Run exactly one writer per data directory. Do not scale the Compose service.
Do not use Rainforest's database or credentials directory as this service's
volume. A named volume is recommended; image upgrades must reuse it.

For a host bind mount instead, create its directory before starting and set
owner `10001:10001`, mode `0700`. A new named volume inherits that ownership
from the image; an existing incorrectly owned volume does not. Fix its ownership
while the service is stopped, then run the normal image as nonroot.

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
relationships. Later steps add album scope/origin columns and a normalized
categorized usage snapshot (schema 5), also in place; historical mappings,
cursors, confirmed/existing state and deletion state are preserved, and no
migration widens scope or enables a new write. Take the stopped complete-volume
backup before first startup with the new binary. To roll back, restore that
pre-upgrade volume together with its matching binary/config; an older binary
cannot read the newer schema.
Do not delete migration state, queues or album intents to force a downgrade.

When migrating hosts, move the stopped complete data archive, config and secrets.
Ensure the old writer remains stopped before the new one starts. Keep host port
binding on `127.0.0.1`; use authenticated access through a trusted network or
protected tunnel for remote administration. The dashboard does not trust
forwarded headers; do not assume proxy TLS sets its backend cookie Secure.
Review the [session/exposure limits](local-management.md#sessions-and-exposure).
