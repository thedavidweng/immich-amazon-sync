# 0005: Durable event hints and bounded owned membership

Status: accepted. Date: 2026-10-20.

Accept compact asset-ID hints and native Immich `AssetV1` Workflow envelopes on
`POST /events/{account}`. Use a separate mounted bearer secret reread per request,
bound bodies to 64 KiB, and commit account-scoped deduplicated hints before HTTP
202. Accept native outer `config`/`workflow` fields but discard them and all asset
metadata except the stable ID. The handler performs no remote calls. Workers
re-read the configured source to establish eligibility and selected scope.
Manual native Workflow setup keeps the polling key read-only; automatic
registration is unavailable through the read-only Source seam.

Keep event wakes, bounded continuation, periodic reconciliation and retry
deadlines independent. Prioritize hints, then library deltas, then historical
backfill without interrupting in-flight transfers. Library deltas overlap the
completed watermark by two minutes; advance it only after valid terminal-page
commit. Perform daily full library verification and periodic full explicit-album
scans. Pagination is not a source snapshot; incomplete reads cannot authorize
absence-based changes. Account cycles remain independent.

Require explicit source album rules for mirroring; reject library-plus-mirroring
rather than silently ignoring it. Persist album creation intent before dispatch
and bind ownership only to the ID returned by our own creation. Preserve
unknown-ID creation uncertainty across retry/restart; do not adopt by name or
automatically recreate. Rename retains identity. Persist and reconcile membership
separately from media so association failures cannot trigger another upload.
Permit safe additions during backfill.

Remove only owned relations in owned albums after a complete successful
generation, eligible-media completion and established baseline. Require a
nonempty desired set and limit actual removals to
`min(500, max(1, baseline / 10))`, using integer division. Count previously absent
owned relations that were manually re-added remotely. Reject an unsafe plan
before partial membership mutation for that album. Disabled rules make no remote
writes; unrelated manual relations and all media remain untouched.

Migrate SQLite schema 1 to 2 in place, preserving confirmed content and account
history. Keep queue, cursor, intent and mapping state in the same persistent
volume. Account success waits for authoritative discovery, hints, media and
requested album generations. Expose aggregate pending/failed/unconfirmed album,
association and event counts plus retry deadlines without private remote data.
Media deletion remains deferred to ticket #8.
