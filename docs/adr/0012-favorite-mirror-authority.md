# 0012: Favorite mirror authority and shared-target conflicts

Status: accepted. Date: 2026-10-22.

Favorite sync is a separate per-account opt-in. `off` remains the default for
every account, no migration or default enables a mode, and `add_only` keeps its
original contract: star a verified Node when the source explicitly reports
`isFavorite: true`, never unstar.

## Authority

`favorites: mirror` adds an unstar, because a one-way backup leaves curation
inconsistent for users who rely on the Amazon collection. An unstar is only
legal under two conditions, both checked in the same attempt as the mutation:

1. The remote read immediately before the mutation proves an explicit
   `settings.favorite: true`. An absent field is `favorite_unconfirmed` and no
   mutation happens, so stale or partial metadata can never clear a star.
2. The service holds durable authority: the row's `applied` flag records that
   this service applied and verified the star, or the account records
   `favorites_takeover: true`.

Without either authority the row is `preserved`: the Amazon star stays, no write
is issued and status explains why. A star that was only ever observed is never
promoted to an applied star, so manually curated `existing` Nodes survive
enabling mirror. Takeover is a distinct recorded statement, valid only with
mirror (`favorites_takeover_requires_mirror` otherwise), because clearing a
user's manual curation is a different act from reversing this service's own
write.

Every mutation goes through the SDK's single `SetFavorite(ctx, nodeIDs, bool)`;
the service never reimplements the request. Post-write verification requires the
explicit expected value, so the mutation response is never proof and a lost
response stays `uncertain` until a fresh read settles it. No path re-uploads
media.

## Drift

State is refreshed from the source on every periodic pass, and a bounded audit
re-reads settled nodes at most once per six hours, oldest first, at most sixteen
per pass. The audit repairs a manual unstar of a star the source still wants,
and, with takeover, a manual re-star of a node the source cleared. An outage
during the audit is an ordinary per-node failure with backoff, so restart and
outage converge without a retry storm. The audit never includes a node whose
mappings are all trashed, deleted or unconfirmed.

## Shared target

Two local accounts can resolve to the same Amazon identity. Because the single
ledger keys favorite rows by account and Node, a contradictory wish is visible
across accounts. When both rows carry an explicit intent and their desired
values differ, both accounts record `shared_target_conflict` and neither writes
that Node rather than alternating PATCH requests. The conflict is derived on
every pass, so it withdraws when one source wish changes; it is not deletion
evidence and does not change media state.

Trashed, Deleted and Unconfirmed Nodes remain ineligible for both a star and an
unstar, so trashing a source asset never resurrects or alters the Node's
favorite state. Deletion-on and deletion-off suites both cover favorite
behavior.
