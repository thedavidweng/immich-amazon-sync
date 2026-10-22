# Diagnose without losing recovery state

Start with `docker compose ps`, `docker compose logs --tail=100 sync`, and the
local health probes. Treat logs as private even though the service emits finite
error codes. Never attach secrets, database contents or raw private URLs to an
issue. Stop the daemon before running another CLI process on the same volume.

| Symptom/code | Action |
| --- | --- |
| `config_invalid_json` | Use JSON, remove unknown fields and trailing values; compare the reference example. |
| `source_scope_required` | Select explicit album IDs, or deliberately enable library mode; these scopes cannot be combined. |
| `secret_unreadable` | Check the exact mount path and UID 10001 file readability. Recreate after atomic credential replacement. |
| `authentication_required` | Renew the same identity's credential; preserve the queue and account binding. |
| `permission_denied` | Check Immich read/download scopes or Amazon account access; repeating login may not solve permission denial. |
| `rate_limited` | Respect Retry-After/backoff; do not create a second worker instance to bypass it. |
| `remote_unavailable` | Check container DNS/network and configured source URL. This is not deletion evidence. |
| `remote_protocol` | Capture only finite diagnostic fields and deployed versions; pagination/schema incompatibility must not become an empty album. |
| `content_conflict` | A filename or HTTP 409 is not content equality. The service re-queries the whole account by MD5; a zero match after a 409 stays explicit rather than becoming a backup claim. Investigate confirmed remote MD5/size/identity. |
| `upload_unconfirmed` / unconfirmed count | Retain the state volume and staged original. Reconcile remote metadata; do not force a new upload or rename. |
| `album_unconfirmed` | Album creation may have committed without a known returned ID. Preserve state; retry/restart cannot safely adopt by name or recreate it. |
| `membership_guard` | Verify source scope/counts and snapshots. Empty or excessive removals are blocked; there is no guard override. |
| `invalid_event` | Check UUIDv4, native type/trigger, top-level fields and 64 KiB body limit; see the event contract. |
| `webhook_unauthorized` | Check the configured account ID and bearer header against the mounted shared secret. |
| `webhook_unavailable` | Check the secret path/readability, minimum 32 trimmed bytes and maximum 4 KiB file size, or configure a managed Workflow for the account. |
| `workflow_version_unsupported` / `workflow_permission_missing` / `workflow_trigger_unsupported` | Managed Workflow capability notice, not a media failure. Polling continues. Fix the separate key's Workflow scopes or use a v3.x server, then run `workflow register --apply --force`. |
| `workflow_unconfirmed` | A create or update may have committed. The next cycle reconciles by reading; never delete or recreate the Remote Workflow manually. |
| `workflow_not_owned` / `workflow_ambiguous` | The persisted UUID no longer matches this service's marker, or more than one exact match exists. Nothing is mutated. Inspect Immich, keep unrelated workflows, and resolve duplicates before re-applying. |
| `workflow_cleanup_failed` | Best-effort removal of a disabled or removed account's owned Workflow failed. Re-add the account with its mounted Workflow key, keep `managed_workflow: false`, and run `workflow remove --apply`. |
| `workflow_secret_unavailable` | The generated secret file is missing or invalid; apply regenerates it with mode `0600`. |
| `source_changed` | Source content evidence (SHA-1, size, or filesystem modification time) no longer agrees with the recorded original. Preserve confirmed/uncertain mappings; retry does not clear this conflict. A metadata-only Immich edit does not trigger it. |
| `deletion_guard` | The pending pass exceeds 500 assets or 10% of the mapped library, so nothing was trashed. Inspect `deletions plan`; restore Immich assets or accept a smaller trash before retrying. Disable propagation to withdraw all pending intents. |
| Deletion trash failure | The asset stays `deletion_pending` with a finite error and retries with backoff. The Amazon node was not confirmed trashed; inspect credentials, ownership and connectivity. |
| Local storage/lock failure | Stop the competing writer, fix volume ownership/capacity, and retry. Never remove a live process's lock. |

A photo whose only Amazon copy already lives in another folder or only inside
an album is reconciled through the controlled account-wide MD5 lookup. Exactly
one AVAILABLE, owned node with matching MD5 and size is reused: its staging file
is released, the durable node mapping is kept and a requested album membership
is still applied. The status counts that asset as `existing`, not `confirmed`.
Zero matches on a fresh asset mean no copy was found, so the normal upload
proceeds; after an uncertain write, zero matches keep it `unconfirmed`. Multiple
matches, mismatched metadata, unproven ownership and incomplete pagination are
never reused: a fresh asset fails closed with the diagnostic, and an uncertain
write stays `unconfirmed`.

For `unconfirmed`, the write may have committed before its response was lost.
Even a currently empty search result cannot prove noncommit: remote indexing
may lag. The service must keep uncertainty when it cannot prove a matching
Node. Retry is not permission to clear this state. Do not reset the database
to make an error disappear.

The pinned SDK accepts valid empty list envelopes and rejects malformed or
incomplete responses. An empty lookup before a first upload is no longer blocked
by envelope ambiguity; it does not clear uncertainty from an earlier write.
See [SDK list semantics and remaining limits](../reference/compatibility.md#sdk-list-semantics-and-remaining-limits).

Metadata operations expire after 30 seconds, including time spent waiting for
headers or decoding JSON, unless the caller cancels or expires sooner. A read
expiry reports `remote_unavailable`; expiry after a mutation or during
post-upload verification reports `upload_unconfirmed`. Preserve uncertain state
and reconcile instead of replaying the write. This is not a whole-media timeout:
original downloads and uploads retain the caller context, and post-upload
metadata verification gets its own fresh deadline.

On Immich v2.0.0, whole-library and owned-album discovery now includes both
timeline and archived originals. Previously completed history requires a new
full scan; continuing an old completed scan cannot discover the omitted archive.
v2 shared albums remain unsupported rather than returning an incomplete backup.
See [Immich compatibility](../reference/compatibility.md#immich-contract).

## Discovery and album progress

Successful `sync once` is a bounded pass, not proof that a large backfill is
complete. Use `serve` for continuation and inspect
[durable status](../reference/status.md), including `albums_failed`,
association/event counts and optional `next_attempt`. A future retry deadline
can leave `work_remaining` false while failed work remains.

After fixing source access or invalid pagination, account Retry can invalidate
failed incomplete full generations and start fresh discovery. Changed album
counts automatically invalidate interrupted generations. Retry preserves manual
pause, confirmed content and uncertain writes; resume a manually paused account
separately. Do not alter SQLite cursors or remove the volume.

Album associations retry independently from media. Follow
[managed album recovery and guards](managed-albums.md); empty source results,
missing albums and incomplete scans never authorize media deletion. A webhook
`202` proves only local enqueue; use [event setup](event-hints.md) and the
[response contract](../reference/events.md) to diagnose delivery separately.

## Local management

No login page is expected without a configured admin file. A passphrase shorter
than 12 characters or an unreadable mount prevents management startup. For
`csrf_invalid`, load the login form again in the same browser session; do not
disable CSRF or paste a stale token. Logout, restart and admin-secret rotation
invalidate sessions. Wait for the login throttle after repeated failures.

Missing config is not setup: use explicit `serve --setup --config NEW_PATH`
only for a new path. Omit `--setup` after the first valid save. The normal
read-only Compose config mount cannot accept an atomic UI save; use the
[writable-directory deployment](local-management.md). No-path and `--account`
runs deliberately disable configuration editing, and every saved account needs
mounted credential paths. Do not loosen file permissions or erase the database
to bypass these errors.

## Health and support

Healthy/readiness responses describe the process and local services, not
successful remote backup. A remote outage should remain diagnosable without
turning into a container restart loop. Docker's `unless-stopped` policy restarts
an exited process; an `unhealthy` status alone does not restart it.

Inspect media/region limits in [compatibility](../reference/compatibility.md).
If you need support, report the service version/commit, Docker platform,
Immich version, marketplace code, finite error code and a redacted description.
