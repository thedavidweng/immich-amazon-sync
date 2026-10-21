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
| `content_conflict` | A filename or HTTP 409 is not content equality. Investigate confirmed remote MD5/size/identity. |
| `upload_unconfirmed` / unconfirmed count | Retain the state volume and staged original. Reconcile remote metadata; do not force a new upload or rename. |
| Local storage/lock failure | Stop the competing writer, fix volume ownership/capacity, and retry. Never remove a live process's lock. |

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
