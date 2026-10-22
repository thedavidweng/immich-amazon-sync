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

## Favorite writes

Favorite sync is opt-in per account; `off` is the default for every account and
no migration or upgrade enables it. When `favorites: add_only` is set, a
verified Node mapped from a `confirmed` or `existing` asset is starred only if
the source explicitly reports `isFavorite: true`. An omitted source field is
unknown and cannot authorize a write, and add-only never unstars.

`favorites: mirror` adds an unstar, but only under durable authority: the star
must have been applied by this service and verified earlier, and the unstar is
issued only after a fresh read proves an explicit remote `settings.favorite:
true` immediately before the mutation. A star this service did not apply is
preserved, so manually curated Amazon favorites survive enabling mirror on
`existing` Nodes. `favorites_takeover: true`, legal only with mirror, is the
single recording that lets the source clear those manual stars too. Nothing
about a stale local observation can trigger an unstar: the value that authorizes
it comes from the read in the same attempt.

The pre-write read and the post-write verification both require an explicit raw
`settings.favorite` field. A missing field is `favorite_unconfirmed`; the SDK's
mutation response is never proof. A lost or timed-out mutation stays uncertain
and is retried only after a fresh read, so a committed star or unstar settles
without a second PATCH and no media is uploaded again. Trashed, Deleted and
Unconfirmed Nodes are ineligible, so a source trash never resurrects or alters
their star. Several assets or albums sharing a Node produce one durable outcome
and one write.

Drift is repaired by a bounded audit: each settled node is re-read at most once
per six hours, oldest first, and at most sixteen per pass. A manual unstar of a
star the source still wants is re-starred; with takeover, a manual re-star is
cleared. An outage during the audit becomes an ordinary per-node retry with
backoff, so a restart or a remote outage converges without a retry storm.

Two local accounts can resolve to the same Amazon identity. When they record
contradictory wishes for a shared Node, both stop writing it and report
`shared_target_conflict` instead of alternating PATCH requests. The conflict is
derived on every pass and withdraws as soon as one source wish changes; it never
counts as deletion evidence or affects media state.
## Originals and capture time

Every upload path only reads the source original and writes a separate staging
copy. The service never rewrites original bytes, calls `utime`, or edits EXIF to
influence Amazon metadata; tests assert bytes and filesystem mtimes are
unchanged on the staged and direct-mount upload, verified-reuse and
uncertain-write paths.
Embedded EXIF travels inside the unchanged bytes. Immich-only capture-date edits
are not mirrored: the pinned SDK has no capture-date setter, so `doctor` and
status report `capture_date=metadata_not_supported`. Amazon can therefore display
a different date than Immich for such files and for originals without a usable
EXIF date. A future setter requires a released SDK operation and an authoritative
read-back; rewriting originals is never a substitute.

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

Read-only mounts are optional input only. An approved `direct_roots` mapping lets
an eligible original be staged from disk instead of downloaded, but the service
never writes to, creates in or retimes that mount. The staged bytes are verified
against the recorded size and checksum, the file is re-stated after the read, and
a mutation yields `source_changed` rather than a recorded digest. Paths are
confined with `os.Root`, so traversal and symlink escapes fail closed instead of
falling back. Absent, out-of-root, missing or unreadable paths use the Immich API
stream, so a broken mount degrades to the previous behaviour. See
[direct originals](../how-to/direct-originals.md).

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
