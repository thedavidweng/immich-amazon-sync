# Why the service keeps uncertainty

An upload can commit before the response is lost. Repeating the same POST after
a timeout can therefore create another Node. Before dispatch, the service
durably records the account/asset identity, full staged MD5, byte size and
uncertain write intent. Recovery reads remote evidence first. A matching
filename, HTTP 409, an SDK success label or an empty search is insufficient.
Completion requires a Node ID with matching content and destination evidence.

SQLite is the operational truth for mappings, queue and progress. A single
writer owns it and its staging directory. It is not a substitute for an
independent copy of your originals. Preserve the entire data directory across
restarts, image upgrades and credential renewal.

Configured account IDs bind to the source user's remote identity and destination
root identity. Rotating a credential for the same account does not create a new
identity; changing users under the same local ID must stop work. Content and
album mappings stay scoped to that account. Album names cannot substitute for
stable source IDs or prove ownership of an existing target album.

A copy already present elsewhere in the same account is reusable only after the
controlled account-wide MD5 lookup returns exactly one owned, AVAILABLE node
whose re-read MD5 and byte size match. That node is recorded as `existing`
rather than `confirmed`, because it is not at the configured target parent. The
staged original is released and the node mapping is durable, so a later album
generation can attach the remote copy without downloading the original again.
A 409, a filename, a search hit alone or an incomplete enumeration never
establishes reuse.

Media confirmation and Membership are independent. A confirmed upload whose
album operation fails keeps its Node mapping; only the relationship needs
repair. Removing a membership from one managed album must not remove the shared
Node or another album's membership. Incomplete scans and uncertain target album
ownership cannot authorize removals.
Only returned IDs from this service's own album creation establish ownership.
An unknown-ID create remains uncertain; a same-name album is insufficient.
Safe additions can proceed during backfill, but removals require a completed
baseline, nonempty desired set and the
[fixed membership guard](../how-to/managed-albums.md#understand-removal-guards).

The Amazon boundary has no media deletion operation. Empty source results,
source outages, old source snapshots, rule removal and account removal cannot
trash backups. Optional media deletion is deferred to ticket #8 and would
require separate policy, confirmation and audit tests.

Events improve latency; periodic source reads repair missed events. Neither an
unreliable webhook nor offset-like pagination is a completeness proof. A failed
scan must keep its previous completed boundary and avoid absence-based changes.
Native Workflow metadata is untrusted: the service durably retains only an asset
ID and re-reads Immich to validate scope. Event acknowledgment precedes remote
work and cannot mean upload success. Overlapping library deltas and periodic
full scans repair missed hints; they retain durable boundaries without claiming
a source snapshot. See [progress and scheduling](../reference/status.md) and
the [event/membership decision](../adr/0005-durable-hints-and-owned-membership.md).
Read [contract decisions](../adr/0003-upstream-contract-boundaries.md) for the
researched checksum, media-pair and pagination limits.

Local health is deliberately independent of remote service availability.
Restarting a healthy local database because Amazon is offline does not fix
credentials or uncertain writes. Credential files remain outside the image,
mounted read-only; logs use allowed diagnostic fields rather than raw remote
errors or SDK objects.

Local management is opt-in and isolated by loopback publication. Login and all
mutations use session CSRF; in-memory sessions end on logout, restart or secret
rotation. The editor stores credential paths, never values. Config validation
and atomic persistence precede reload; failed saves leave the runtime unchanged.
Saving a valid account enables scheduled work, not merely a read-only preview.
See [local management](../how-to/local-management.md) for writable modes and why
proxy TLS/forwarded headers are not an assumed security boundary.
