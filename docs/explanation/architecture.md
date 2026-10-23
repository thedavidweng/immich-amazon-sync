# Architecture and Design Principles

`immich-amazon-sync` is a self-hosted, one-way synchronization service designed to reliably back up original photos and videos from an [Immich](https://github.com/immich-app/immich) instance to **Amazon Photos**.

## Data Pipeline

```
[Immich Instance]
       | (API / Direct Disk Mount)
       v
[Durable SQLite Queue & Staging]
       | (aphotos Go SDK)
       v
[Amazon Photos Cloud]
```

The service runs as a single container with a single persistent data volume (`/data`):
1. **Source Discovery**: Scans Immich assets via polling or optional webhook/workflow hints, scoped by specific album IDs or whole-library mode (`library: true`).
2. **Staging & Hash Verification**: Original media is streamed into local disk staging (or accessed via read-only direct disk mounts) where byte size and MD5 checksums are verified.
3. **Deduplication Check**: Before uploading, the service checks whether an identical file (matching MD5 and byte size) already exists anywhere in the Amazon account.
4. **SDK Upload**: Media is uploaded using the `aphotos` Go SDK with streaming transfers and bounded concurrency.
5. **Authoritative Verification & Album Membership**: Uploaded nodes are re-read by ID to confirm remote commitment before recording success in SQLite, followed by optional album membership assignment and favorite synchronization.

---

## Core Guarantees and Principles

### 1. Original Bytes Contract
The backup contract is the **original bytes**. There is no quality selector, "Storage Saver" downsampling, or recompression path. Embedded EXIF and metadata travel within unchanged original bytes.

Amazon reports observed usage categorized into photos, videos, and other storage. Because Amazon's API does not expose total subscription quota, remaining storage, or Prime entitlement, the service reports these observed numbers alongside `capacity: unknown` and `entitlement: unknown`. Users can optionally set a `video_usage_threshold_bytes` budget to pause video uploads while allowing unlimited photo uploads to continue. See [storage usage and quota](../reference/compatibility.md#storage-usage-and-quota).

### 2. Safety and Non-Destructive Operations
By default, the service **never deletes media from Amazon Photos**. Removing an asset, album, or account from Immich does not delete remote backups.

- **Opt-in Trash Propagation**: Trashing an Immich asset can optionally move the corresponding Amazon node to the Amazon Trash (which is recoverable and never emptied by this service). This requires explicitly setting `deletions: true` and typing the exact `deletions_confirmation` phrase.
- **Safety Guards**: Any single pass that would trash more than 500 assets or more than 10% of the mapped library is automatically refused with `deletion_guard`.
- See [Safety Model](safety.md) for full details.

### 3. Smart Deduplication and Verified Reuse
If an original already exists anywhere in the target Amazon account (in another folder or inside an album), the service reuses the remote node instead of re-uploading:
- Reuse requires exactly one `AVAILABLE`, owned node with matching MD5 and byte size, confirmed by an authoritative read by ID.
- Reused assets are recorded in the ledger as `existing` rather than `confirmed`.
- If zero or multiple matches exist, or if metadata is ambiguous, reuse is refused and a fresh upload proceeds only when absence is proven.
- See [ADR 0006](../adr/0006-reuse-verified-existing-media.md).

### 4. Failure Tolerance and Preserving Uncertainty
Network timeouts or server drops during uploads can leave remote commitment uncertain. The service avoids blind retries:
- The upload intent and exact staged checksum are recorded in SQLite before dispatch.
- Upon restart or retry, the remote state is re-evaluated first.
- A failed or uncertain upload stays `upload_unconfirmed` until an authoritative remote re-read proves commitment or absence.
- See [Safety Model](safety.md#why-the-service-keeps-uncertainty).

### 5. Transfer Windows
To avoid saturating network bandwidth during peak hours, accounts can define an optional daily transfer window in an IANA timezone (e.g. `23:00` to `07:00`, spanning midnight).
- Outside the window, scans and metadata checks continue, but new uploads are deferred.
- Scheduled wakeups are DST-aware.
- See [Operations Guide](../how-to/operations.md#limit-transfer-hours).

### 6. Apple Live Photos
By default (`still_only`), the still photo is backed up and the hidden companion MOV is excluded. With `live_photo_mode: original_pair`, both the still image and companion MOV are uploaded as independent originals with their own hashes and Amazon nodes. No files are muxed, converted, or altered. See [Live Photo Originals](../reference/configuration.md#live-photo-originals).

### 7. Direct Disk Mounts (Read-Only Originals)
To eliminate redundant HTTP downloads from the Immich server when running on the same host or NAS, accounts can configure `direct_roots`:
- Original files are read directly from local filesystem paths mounted read-only (`:ro`).
- Path traversal escapes fail closed (`os.Root`).
- If a mount is missing or unreadable, the service automatically falls back to streaming from the Immich API.
- See [Direct Originals](../how-to/direct-originals.md).

### 8. Favorites Synchronization
Favorite sync is opt-in per account (`off` by default):
- `favorites: add_only`: Stars verified nodes in Amazon Photos if the Immich source is marked favorite. Never unstars.
- `favorites: mirror`: Also unstars nodes, but only if the star was originally applied by this service.
- `favorites_takeover: true`: Allows Immich authority over stars that were manually applied in Amazon Photos.
- Conflicting multi-account sync requests report `shared_target_conflict` and halt mutations on that node until resolved.
- See [ADR 0012](../adr/0012-favorite-mirror-authority.md).

### 9. Eligibility Filters
Configurable rules allow skipping camera RAW formats, assets exceeding a byte limit, specific Immich tags, or archived media. Filter decisions are durable and re-evaluable, so changing a tag or filter requeues the item without rescanning the entire library. See [Eligibility Filters](../reference/configuration.md#eligibility-filters).

### 10. Managed Immich Workflows
When running against Immich v1.118+, the service can automatically register and manage native Immich Workflows to receive event-driven sync triggers with low latency, gracefully falling back to polling if permissions or versions do not support it. See [Managed Workflows](../how-to/managed-workflows.md).

---

## Boundaries and Upstream Contracts

- **Amazon Photos**: Amazon Photos does not offer an official public API. The service relies on the unofficial `aphotos` Go SDK, using web session cookies. Sessions eventually expire and require manual rotation. Always maintain an independent primary backup.
- **Immich**: The service interacts with Immich purely via its public REST API (and optional read-only filesystem mounts). It never modifies Immich databases or assets.
