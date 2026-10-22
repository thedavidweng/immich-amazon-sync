# 0009: Opt-in direct reads from a read-only originals mount

Status: accepted. Date: 2026-10-22.

Downloading every original through Immich is correct but wasteful when the same
bytes are already visible to this container. A per-account `direct_roots`
mapping from the source-reported path prefix to a local directory removes that
download for eligible assets. It is an optimisation of input, not a new sync
path: staging, digesting, quotas, worker limits, rate limiting, dedupe,
reconciliation and every durable state stay exactly as they are.

The mapping is empty by default, so existing accounts and upgrades keep the API
stream. An account must approve both an absolute, clean `immich_path` prefix and
the absolute `host_path` that mirrors it; duplicate prefixes, relative paths and
unclean paths reject the config with `direct_root_invalid`. The longest matching
prefix wins. Readiness is evaluated per transfer, so a mount that appears later
is used without a restart.

Eligibility is deliberately asymmetric. A regular file under an approved root is
read in place. Everything that merely means the mount cannot serve this asset —
no mapping, outside every root, absent root, missing file, permission denied —
falls back to the Immich API and is counted as a fallback. A path that is
malformed or resolves to something other than a regular file is
`direct_path_invalid`, `direct_path_escape` or `direct_path_not_regular` and
fails closed: an unapproved or hostile path must never silently download
different bytes. `os.Root` confines every open, so `..` and symlink escapes are
impossible even for an untrusted path; contained symlinks remain usable.

The source path is transient. It is read from the source contract per transfer,
never persisted in asset metadata and never logged, so the ledger and diagnostics
stay path-free. `doctor` reports only `<available>/<configured>` counts.

Byte evidence is unchanged. The mounted original is streamed in 32 KiB reads
through the same staging writer and rate reader, hashed as MD5 plus size, and
verified against the recorded source size and checksum. The file is re-stated
after the read; a size, mode or mtime change, or a checksum mismatch, yields
`source_changed` and records no digest. The service opens the mount read-only,
never creates or truncates a file there, and never touches its mtimes.

The post-dispatch invariant is untouched: the digest and size are committed
before `running`, so a crash after a possible upload reconciles by them and never
repeats the upload. Counters (`reads`, `bytes_avoided`, `fallbacks`, `failures`)
live in the existing store, are process-local and reset on restart; they are
diagnostics, not a second state store.

This decision adds no source write, no second upload pipeline, no daemon, no
index and no fallback that hides a mapping error.
