# 0006: Reuse verified media that already exists outside the target parent

Status: accepted. Date: 2026-10-21.

Keep the target-scoped `Find` strict: it may only return AVAILABLE, owned nodes
at the requested parent, and only a complete zero-or-one page. The queue must
not widen that lookup, because a hit there is used as placement evidence.

After the strict lookup proves no copy at the target parent, perform one
controlled account-wide MD5 lookup before staging-derived upload. Reuse only an
exactly-one AVAILABLE node owned by the authenticated account whose re-read MD5
and byte size match the staged original. Re-read the node and every parent to
prove kind, status and ownership. Zero matches mean the content is absent;
multiple matches, mismatched metadata, unproven ownership, invalid pagination
and any read failure stay `upload_unconfirmed`. A 409 or a filename is never
placement evidence.

Record reuse durably in the existing asset row: `confirmed` when the verified
node is at the configured target parent, `existing` when it lives in another
owned folder or only in owned albums. Both states keep the node ID and release
the staged original, and both count as settled for discovery and as desired
album members. `existing` is deliberately not reported as `confirmed`; status
exposes it separately so "backed up elsewhere" is never presented as "uploaded
to the target".

Recover an uncertain write the same way: reconcile with the account-wide lookup
instead of repeating the POST. If the lookup proves no copy exists, keep the
uncertainty and the staged original; surface `content_conflict` only when the
write itself was a conflict and no verified copy exists.

This decision adds no second Amazon API implementation, no automatic delete and
no separate state ledger. Media deletion remains deferred to ticket #8.
