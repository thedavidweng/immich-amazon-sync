# 0001: Independent service with durable, non-destructive state

Status: accepted. Date: 2026-10-20.

Use Go, Cobra, modernc SQLite and the public aphotos Go SDK. SQLite is the only
business state store. Account keys scope every job, mapping and cursor. A single
writer owns a persistent volume; a competing process fails explicitly.

Persist content MD5, size and identity before upload. Persist an uncertain write
state before the network operation. Recover by reading remote metadata, never by
blindly replaying an upload. Media and album membership are independent stages.

The Amazon boundary has no media deletion operation. Source outages, rule
removal and account removal cannot delete remote backups. Optional media deletion
is deferred to a separately reviewed issue, as allowed by the spec.

The spec already agrees the testing seams: public sync use cases with fake
external adapters and real SQLite/filesystem, adapter HTTP contracts, CLI and
container contracts. No further seam confirmation is necessary.
