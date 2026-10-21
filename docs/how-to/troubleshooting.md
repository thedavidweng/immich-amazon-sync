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
| `external_library_checksum` / `unknown_checksum_provenance` | The original is skipped because a byte-checksum contract is not established. It is not backed up; retain another independent copy. |
| Local storage/lock failure | Stop the competing writer, fix volume ownership/capacity, and retry. Never remove a live process's lock. |

For `unconfirmed`, the write may have committed before its response was lost.
Even a currently empty search result cannot prove noncommit: remote indexing
may lag. The service must keep uncertainty when it cannot prove a matching
Node. Retry is not permission to clear this state. Do not reset the database
to make an error disappear.

The pinned SDK also cannot distinguish an empty list from a missing response
envelope. The adapter rejects empty results even before a first upload and for
empty album memberships. A new SDK pin with validated list envelopes is needed;
credentials or database resets cannot fix this protocol limit.

Healthy/readiness responses describe the process and local services, not
successful remote backup. A remote outage should remain diagnosable without
turning into a container restart loop. Docker's `unless-stopped` policy restarts
an exited process; an `unhealthy` status alone does not restart it.

Inspect media/region limits in [compatibility](../reference/compatibility.md).
If you need support, report the service version/commit, Docker platform,
Immich version, marketplace code, finite error code and a redacted description.
