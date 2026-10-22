# Read originals from a read-only mount

By default every original is downloaded from Immich. When the originals are
already visible to this container, an account can map Immich's reported paths
onto pre-approved local roots and stage the bytes from disk instead. The mount
is strictly read-only input: this service never writes to it, never creates
files in it and never changes its mtimes.

Direct reads are opt-in per account. An account without `direct_roots` keeps the
existing Immich API behaviour, so an upgrade changes nothing.

## Approve the mapping

Immich reports each asset's absolute `originalPath`. Approve a prefix of it and
the local directory that mirrors that prefix:

```json
{
  "accounts": [
    {
      "id": "home",
      "immich_url": "https://immich.example.org",
      "immich_api_key_file": "/run/secrets/immich_api_key",
      "amazon_region": "us",
      "amazon_cookie_file": "/run/secrets/amazon_cookie",
      "library": true,
      "direct_roots": [
        {"immich_path": "/photos", "host_path": "/originals"}
      ]
    }
  ]
}
```

Both paths must be absolute and clean (`/photos`, not `photos` or `/photos/`).
The longest matching `immich_path` wins, so a nested root can override part of a
broader one. An unreadable or absent `host_path` is not a configuration error;
that root simply falls back until it is mounted.

The mapping is configuration-file only: the dashboard preserves it when an
account is edited, but does not offer an editor for it.

The mapping is only correct if `host_path` contains the same bytes Immich keeps
under `immich_path`. A quick check on the host:

```sh
immich_path=/photos/library/ab/cd/asset.jpg
sha1sum "/originals/${immich_path#/photos/}"
```

## Mount it read-only

The stock Compose file mounts only configuration and secrets. Use the optional
overlay, which adds the originals directory as a read-only bind mount and does
not change the read-only root filesystem or the nonroot UID:

```sh
mkdir -p immich-originals
# Immich's upload/library tree, for example, must be readable by UID 10001.
docker compose -f compose.yaml -f compose.direct.yaml up -d
```

The overlay reads `IMMICH_ORIGINALS` for the host path and defaults to
`./immich-originals`. Without the overlay, `/originals` is absent and every
original falls back to the Immich API.

## What is eligible

| Situation | Result |
| --- | --- |
| Regular file under an approved root | Read from disk; no Immich original download. |
| No `direct_roots` configured | Immich API. |
| Path outside every approved root | Immich API (counted as a fallback). |
| Approved root absent, missing file or permission denied | Immich API (counted as a fallback). |
| Relative, unclean or traversal path; symlink escaping the root; device, socket, FIFO or directory | Fail closed with `direct_path_invalid`, `direct_path_escape` or `direct_path_not_regular`. Nothing is downloaded and nothing is uploaded. |
| Bytes differ from the recorded size or source checksum, or the file changes while it is read | `source_changed`; no digest is recorded. |

Symlinks are allowed only when they resolve inside the approved root. Requests
are confined with `os.Root`, so `..` and symlink escapes cannot reach outside it.

## Guarantees

- The staged copy, its MD5 and its size are produced by the same pipeline as the
  API path. Staging quotas, worker limits and `bytes_per_second` still apply, and
  memory stays bounded because the file is streamed in 32 KiB reads.
- A file is re-stated after it is read. A size, mode or mtime change during the
  read is a changed source, never a recorded digest.
- The digest and size are recorded before dispatch. A crash after a possible
  upload still reconciles by that recorded digest and size; it never uploads
  again blindly.
- A source read for eligible bytes only happens when the mapping is enabled. Any
  other asset keeps the API stream.

## Diagnostics

`sync once --json` and the scheduled cycle status include a `direct` object:

```json
"direct": {"reads": 120, "bytes_avoided": 42949672960, "api_bytes": 5368709120,
           "fallbacks": {"missing": 2}, "failures": {"path_escape": 1}}
```

`bytes_avoided` counts bytes served from disk instead of downloaded; `api_bytes`
counts originals downloaded from the Immich API. Counters are
process-local and reset on restart. `doctor` prints
`direct_roots=<available>/<configured>` per account; it never prints a path.
`job_progress` log lines add `read_source` (`direct` or `api`) when an original
was opened.

A path failure stays `blocked` until the mapping is fixed and the account's
**Retry eligible errors** control is used; it is never retried automatically and
never falls back to a download. A `source_changed` result clears when Immich
reports new source metadata for that asset, because the staged digest was never
recorded.
