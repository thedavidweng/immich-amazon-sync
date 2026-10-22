# Mirror selected album membership

First establish a reliable backup of disposable originals and preserve the
state volume. Set `mirror_albums: true` on an account with explicit `album_ids`.
Keep `library: false`: library backup does not discover or mirror every visible
album, and combining library mode with mirroring returns `invalid_options`.
Reload through the local editor or restart after editing JSON.

This enables real Amazon album writes and bounded membership removals. It never
deletes media. Regional album behavior remains unverified with real accounts;
review [compatibility](../reference/compatibility.md) before relying on it.

## Keep the owned mapping

Each account/source album scope maps to the target ID returned by this service's
own album creation. Names are labels, not identity or evidence of ownership.
Source renames keep the same mapped target ID. Same-name existing Amazon albums
are not adopted.

Eligible originals shared by selected albums reuse confirmed media within that
account; each album relation is independent. A verified copy that already exists
elsewhere in the account (another folder, or only inside an album) is reused too
and counted as `existing`; its node mapping is durable, so the membership is
applied without downloading or uploading the original again. A failed
association retries the relationship without another media upload. Safe
additions can proceed during incomplete historical backfill.

Deletion propagation is a separate opt-in. A `deleted` asset drops out of the
desired member set, so normal bounded membership reconciliation removes that
relationship without touching other media. Removing an album or rule still
does not trash media.

If album creation may have committed without a known returned ID,
`album_unconfirmed` remains visible across retry and restart. The service does
not recreate it or bind a same-name album automatically. Preserve the database
and inspect destination evidence. There is no operator command to force adoption
or erase that uncertainty.

## Understand removal guards

Membership removal requires a complete successful source generation, completion
of its eligible media work, a previously completed baseline, and this service's
owned album and owned relationship. A nonempty desired set is required.
Relationships added independently by the user are preserved.

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

Removing a rule, disabling mirroring or removing an account stops its managed
writes and preserves cloud media and album relationships. Removing membership
from one owned album does not remove its Node or another album's relation.
Use [status counts](../reference/status.md) to distinguish media completion,
association failures and uncertain creation.
