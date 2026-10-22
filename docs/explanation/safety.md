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
All-accessible mirroring discovers albums from the complete accessible listing
and keys each one by its stable source UUID. A failed listing changes nothing;
an album missing from a successful listing stops mirroring without clearing its
owned destination album, node mappings or memberships, and is never treated as
deletion evidence. Renames keep the owned identity.
Safe additions can proceed during backfill, but removals require a completed
baseline, nonempty desired set and the
[fixed membership guard](../how-to/managed-albums.md#understand-removal-guards).

## Deletion propagation

Deletion propagation is opt-in per account and off by default. The service
detects an asset that Immich reports as trashed, requires a durable node mapping
recorded when the media was confirmed or reused, and re-reads that node to prove
kind, ownership, content and that it is still the mapped copy before moving it
to the Amazon trash. A node that is unavailable, owned by someone else, no
longer matches the recorded MD5 and byte size, or cannot be re-read is never
trashed. The service never permanently deletes media and never empties the
Amazon trash; restoring a node is a manual action in Amazon Photos.

Enabling requires both `deletions: true` and the exact confirmation phrase in
`deletions_confirmation`; turning it off is never gated and withdraws every
pending intent on the next pass without touching remote media. Before each pass the
service re-reads the source state: an asset restored in Immich before its sweep
withdraws the pending intent instead of being trashed later. One pass is refused
when it would trash more than 500 assets or more than 10% of the account's
mapped library, whichever comes first; the guard cannot be disabled, and the
refusal is reported as `deletion_guard` while the assets stay `deletion_pending`.

A hard delete in Immich that never passes through the trash state while the
service is watching is not detected. Restoring an asset in Immich after its
Amazon node was trashed does not restore or re-upload it automatically, and a
`deleted` asset is terminal.

The Amazon boundary otherwise has no media deletion operation outside this
explicit propagation path. Empty source results, source outages, old source
snapshots, rule removal and account removal cannot trash backups.

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
