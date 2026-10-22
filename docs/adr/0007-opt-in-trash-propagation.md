# 0007: Opt-in trash propagation, never permanent deletion

Status: accepted. Date: 2026-10-22.

Deletion propagation is a separate, per-account opt-in and is off by default.
It exists because the upstream project this service is compared with propagates
a source trashed asset, and a backup library with no way to remove a mistake is
not equivalent. It must never become an implicit consequence of scanning.

Enabling requires two independent statements in the account config: `deletions:
true` and `deletions_confirmation` exactly `ENABLE DELETIONS`. Turning it off is
never gated, and disabling propagation withdraws every pending intent on the
next pass without touching remote media. The service reads trashed assets only while the flag is on; with it
off, a trashed source asset keeps its existing mapping and no remote state
changes.

Detection is an explicit `isTrashed` observation from the source, never absence
from a page, a hard delete, an unavailable target, an incomplete scan or a
removed album. A detected asset must already carry a durable node mapping that
was verified when it was confirmed or reused. The sweep re-reads that Amazon
node, proves kind, ownership, content MD5 and byte size, and only then calls the
SDK's trash operation. A node still `AVAILABLE` after the mutation, an
unreadable node or a content mismatch is `upload_unconfirmed`, never success.
A crash between mutation and persistence is repaired by the same idempotent
re-read: an already trashed owned node settles the intent.

Reuse keeps the previous state in `prior_state`. Each pass re-reads the source
before acting: if the asset is no longer trashed, the intent is withdrawn and
the row returns to `confirmed` or `existing`. This prevents a restored asset
from being trashed after the fact.

The pass is refused when it would trash more than 500 assets or more than 10% of
the account's mapped library. Neither bound can be disabled. A refusal is
`deletion_guard`, leaves every pending row untouched and is visible in status
and `deletions plan`. The fraction bound exists to catch a restored or truncated
source that marks a large part of the library as trashed at once.

States are `deletion_pending` (durable intent) and `deleted` (terminal, mapping
kept). A `deleted` asset drops out of desired album membership, so normal
bounded membership reconciliation removes the relationship; removing a rule or
account still cannot trash media.

This decision adds no permanent delete, no trash emptying, no restore
automation and no second Amazon implementation. Amazon's trash remains the
recovery mechanism, and restoring is a manual action.
