# Mirror album membership

First establish a reliable backup of disposable originals and preserve the
state volume. Then choose an explicit album mirroring scope:

| `album_sync_mode` | Scope |
| --- | --- |
| `off` (default) | No Amazon album writes. |
| `selected` | Exactly the albums listed in `album_ids`; requires `library: false`. |
| `all_accessible` | Every album the source API key can read, discovered each full pass. |

`all_accessible` may be combined with `library: true`, which backs up the whole
accessible library and mirrors every accessible album. It may also be combined
with `album_ids`, in which case the discovered albums also contribute their
originals. Reload through the local editor or restart after editing JSON.

The deprecated boolean `mirror_albums: true` maps to `selected`; it never turns
on `all_accessible`. This keeps an upgraded account inside the album scope it
already had.

This enables real Amazon album writes and bounded membership removals. It never
deletes media. Regional album behavior remains unverified with real accounts;
review [compatibility](../reference/compatibility.md) before relying on it.

## Keep the owned mapping

Each account/source album scope maps to the target ID returned by this service's
own album creation, keyed by the stable source album UUID. Names are labels, not
identity or evidence of ownership. Source renames update the destination name
and keep the same mapped target ID. Same-name existing Amazon albums are never
adopted.

With `all_accessible`, a full pass or periodic reconcile reads the complete
album listing and reconciles the rule set against it. A failed read changes
nothing. An album that no longer appears is marked inaccessible: its mirroring
stops, it is counted in `albums_inaccessible`, and its owned destination album,
node mappings and memberships are preserved. An inaccessible, hidden or removed
album is never evidence that media or memberships should be deleted. If it
returns, the same owned destination album and memberships resume.

Eligible originals shared by several albums reuse one confirmed Node within that
account; each album relation is independent. A verified copy that already exists
elsewhere in the account (another folder, or only inside an album) is reused too
and counted as `existing`; its node mapping is durable, so the membership is
applied without downloading or uploading the original again. A failed
association retries the relationship without another media upload. Safe
additions can proceed during incomplete historical backfill.

Deletion propagation is a separate opt-in. A `deleted` asset drops out of the
desired member set, so normal bounded membership reconciliation removes that
relationship without touching other media. Removing an album or rule still does
not trash media.

If album creation may have committed without a known returned ID,
`album_unconfirmed` remains visible across retry and restart. The service does
not recreate it or bind a same-name album automatically. Preserve the database
and inspect destination evidence. There is no operator command to force adoption
or erase that uncertainty.

## Understand removal guards

Membership removal requires a complete successful source generation, completion
of its eligible media work, a previously completed baseline, and this service's
owned album and owned relationship. A nonempty desired set is required.
Relationships added independently by the user are preserved. An incomplete scan
authorizes nothing.

The maximum actual removals per album reconciliation is
`min(500, max(1, baseline / 10))`, with integer division. A baseline of 9 permits
one removal; a baseline of 29 permits two; a baseline of 6,000 permits 500.
Previously absent owned members manually re-added remotely also count toward
the planned removals.

An empty desired set or an excessive planned removal batch returns
`membership_guard` before partial membership mutation for that album. There is
no setting to bypass the guard. Check source access, scope, counts and restored
snapshots; do not reset state to force a large removal or empty the target album.
The guard deliberately leaves relations when absence cannot be applied safely.

## Album capacity

No client-side per-album item limit or album splitting is implemented. The
20,000-item figure sometimes cited for another photo service is specific to that
service and is not used here. Dated evidence for an Amazon Photos album capacity
is not available in this repository, so the service sends a complete desired
membership change and reports a finite error if the provider rejects it. It does
not silently split an album or drop members to fit a guessed limit.

Removing a rule, disabling mirroring or removing an account stops its managed
writes and preserves cloud media and album relationships. Removing membership
from one owned album does not remove its Node or another album's relation.
Use [status counts](../reference/status.md) to distinguish media completion,
association failures, inaccessible albums and uncertain creation.
