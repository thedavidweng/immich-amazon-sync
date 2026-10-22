# 0018: Schema upgrades, safe defaults and the release gate

Status: accepted. Date: 2026-10-22.

The SQLite ledger is the only state store, so its upgrade path is part of the
product contract. `user_version` is the single schema identifier. Each step runs
in one transaction, only adds tables or columns, and never rewrites historical
mappings, cursors, album ownership, deletion states, staging references or
confirmed/Existing content. A migration may adopt a legacy representation into
its explicit successor (the video toggle becomes a media-type exclusion) but may
not enable a new write surface: favorites, Live Photo pairs, managed Workflows,
album discovery, transfer windows, usage holds, direct reads and metrics stay
off unless the operator configures them.

Every step is pinned by a checked-in fixture (`internal/syncer/testdata`) and by
an upgrade test from the deployed version-3 schema and from each fixture to the
head. The gate asserts the full durable state list survives, the migrated store
reopens without rerunning a step, and the stock configuration performs no new
mutation even when the source already reports favorite or Live Photo inputs.
Filters, favorites, Live Photo pairs, album mirroring, transfer windows, direct
reads and usage holds each carry deletion-enabled and deletion-disabled
regression coverage, so opting into their behavior never changes media deletion
policy.

An unknown schema version fails closed with `unsupported_schema` and storage exit
4 rather than being treated as current, partially migrated, or rewritten; a
newer volume therefore requires the matching image, not hand-edited SQLite. The
release gate also builds and smokes the container for linux/amd64 and linux/arm64,
including a version-3 migration, a future-schema refusal and a present read-only
originals mount that must be detected and left unmodified. Release evidence stays
graded honestly: offline contracts and container checks do not upgrade a live
provider claim.
