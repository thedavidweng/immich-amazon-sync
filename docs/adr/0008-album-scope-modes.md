# 0008: Album scope modes and all-accessible discovery

Status: accepted. Date: 2026-10-22. Amends ADR 0005 where it required explicit
album rules and rejected library-plus-mirroring.

## Context

Explicit album rules are safe but require the operator to enumerate albums and
repeat that work for every new album. Ticket #24 asks for an explicit
`album_sync_mode` with `off`, `selected` and `all_accessible`, including use with
whole-library backup. Existing installations already store the boolean
`mirror_albums`, so the change must not widen scope during upgrade.

## Decision

`album_sync_mode` is the only forward form. `off` performs no album writes.
`selected` keeps the previous behaviour: exactly `album_ids`, library off.
`all_accessible` mirrors every album the API key can read, discovered by the
complete album listing.

The legacy `mirror_albums` boolean remains readable. `true` maps to `selected`
and never to `all_accessible`; combining it with a different explicit mode is a
configuration error. An account that upgrades therefore keeps the album scope it
already had.

`library: true` may be combined with `all_accessible`; library backup and album
mirroring are separate dimensions. `selected` still requires explicit albums and
`library: false`.

Discovery runs on explicit full passes and periodic reconciles, not on each
bounded continuation, so an in-flight album scan is not interrupted. A failed
listing changes nothing. A successful listing that omits an album marks it
inaccessible: mirroring for that rule stops and it is reported as
`albums_inaccessible` plus `album_inaccessible`. The owned destination album,
node mappings and memberships are preserved; absence from a listing is not
deletion evidence. If the album returns, the same owned mapping resumes.

Rules gain `origin` (`explicit` or `discovered`), `discovered` (present in the
latest complete listing) and `discovered_at`. The SQLite migration to version 4
only adds these columns: existing rules become explicit and discovered, no rule
is activated and no mapping, cursor or membership changes.

Ownership still comes only from the ID returned by this service's own album
creation. A target album is never adopted by name. An unknown-ID create remains
`album_unconfirmed` across retry and restart and is never re-posted.

Per-album eligibility and permission failures stay on that album's rule. An
inaccessible album cannot authorize membership removal, and an incomplete scan
authorizes nothing. The bounded removal guard is unchanged.

No per-album item limit or album splitting is implemented. The 20,000-item
figure used by Google Photos is specific to that service. This repository has no
dated Amazon Photos album-capacity evidence, so the service sends the complete
desired membership change and reports a finite error if Amazon rejects it rather
than guessing a limit.

## Consequences

All-accessible accounts read one album listing per full pass or reconcile. Album
counts in status include discovered albums. A single inaccessible album is
visible without blocking other albums or deleting their state. Operators who
want a narrower scope choose `selected`; nothing widens automatically.
