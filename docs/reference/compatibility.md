# Compatibility and evidence

Research baseline: **October 20, 2026, America/Vancouver**. Acquisition crossed
October 21 UTC. These pins describe inspected contracts; they do not establish
live-account success. The capability-by-capability comparison against the
combined `immich-gphotos` baseline, with its evidence grades and delivering
tickets, is in [parity.md](parity.md).

| Boundary | Pin/evidence | Limit |
| --- | --- | --- |
| Immich stable | `v3.3.4`, `69e0cb8606137c4f8dc0e7207a672ff8be39f358`, released October 20, 2026 | Official contract/source inspection and offline adapter fixtures; a live `ca` run used Immich `3.3.3`, not this pin. |
| Immich comparison | `v2.0.0`, `e4234af3b3bd32f5dd12a2b0808158339deded43` | Offline owned-album/library contracts include timeline and archive; v2 shared albums are unsupported. Other v2/v3 releases are not individually verified. |
| Immich main | `166e93559f123313cf9cb4e7324c4ee30e21c618`, OpenAPI says `3.3.0-rc.0` | Stale development metadata, not the stable release number or minimum supported version. |
| aphotos SDK | `v0.1.4` | Released tag containing the corrections from SDK PR #13: strict list/final-status validation and typed transient Retry-After, retaining GetNode auth/rate-error preservation. |
| Local runtime | Offline Go, CLI and container contracts plus one live `ca` acceptance run | Real Amazon upload, node re-read, album creation, membership, account-wide MD5 reuse and opt-in trash propagation verified once for one account and one Immich album; format playback, credential renewal and multi-account behavior remain unverified. |

Primary sources: [stable release](https://github.com/immich-app/immich/releases/tag/v3.3.4),
[stable OpenAPI](https://github.com/immich-app/immich/blob/69e0cb8606137c4f8dc0e7207a672ff8be39f358/open-api/immich-openapi-specs.json),
[v2 OpenAPI](https://github.com/immich-app/immich/blob/e4234af3b3bd32f5dd12a2b0808158339deded43/open-api/immich-openapi-specs.json),
[SDK GetNode](https://github.com/thedavidweng/amazon-photos/blob/v0.1.4/pkg/aphotos/account.go),
[SDK v0.1.4](https://github.com/thedavidweng/amazon-photos/releases/tag/v0.1.4).
The SDK pin is a released tag.
The adapter owns the pinned offline contract check; CI runs it through
`go test ./...`, without a second mutable upstream download.

## SDK list semantics and remaining limits

The pinned SDK distinguishes valid empty list envelopes from malformed
responses. `{"count":0,"data":[]}` succeeds for empty lookups and member lists;
the previous first-upload and empty-album blocker is resolved. Missing/null
`count` or `data`, negative/impossible counts and items without IDs produce a
protocol error. An empty page before its advertised count is also an error.

A single page's `Complete=true` establishes that request's success, not that all
results were enumerated. Callers must check `HasMore`, count and page progress.
All-pages helpers report partial results when unique nodes do not cover the
observed count and stop repeated pages without progress. `SearchIter` semantics
are unchanged. An empty search after an uncertain upload still cannot prove
noncommit because remote indexing may lag.

JSON reads, mutations and uploads require a final HTTP 2xx status; final
1xx/3xx responses are protocol errors, not successful writes or absence.
Typed transient errors retain parsed Retry-After for HTTP 503/other 5xx and
response-body failures. The adapter carries that delay through uncertainty and
post-verification errors. The earlier generic final-status/503 propagation gaps
are resolved. A non-2xx upload result still does not prove remote noncommit.

SDK automatic retry scheduling is unchanged. A positive delay is a scheduling
hint, not permission to repeat an uncertain write. The service rejects
credentialed redirects. Keep this delivery experimental;
offline SDK/adapter and local packaging tests do not establish real cloud success.

## Metadata deadlines

Each adapter metadata operation has a 30-second deadline covering response
headers and JSON decoding, including Amazon album mutations. An earlier caller
deadline or cancellation takes precedence. Metadata read expiry reports
`remote_unavailable`; expiry after a mutation or during post-upload verification
reports `upload_unconfirmed`, because the write may have committed. Existing
retry and uncertain-write policies still apply.

Original downloads and SDK uploads retain the caller context without a fixed
whole-media deadline. Each post-upload metadata verification gets a fresh
30-second deadline; a long media transfer does not consume that allowance.

## Storage usage and quota

`aphotos v0.1.4` exposes `GetUsage`, whose categorized `billable`/`total`
observations and `lastCalculated` are pinned by the offline fixture. It does not
expose a subscription cap, remaining bytes or Prime eligibility. This service
therefore reports observed `photo`, `video` and `other` usage (plus any
additional provider category) next to `capacity: unknown` and
`entitlement: unknown`. A zero billable photo count, a marketplace code or low
usage never proves Prime.

The optional per-account `video_usage_threshold_bytes` compares a user-provided
budget with fresh observed billable video bytes. `hold_videos_at_threshold`
stops dispatching new video uploads at or above it; photos and metadata work
continue, and staged originals stay durable. A stale or unavailable usage read
cannot activate the hold.

A quota rejection is classified as `storage_quota_exceeded` only from an
explicit typed non-429 response: HTTP 400, 403 or 409 carrying a capacity
marker, or HTTP 507. HTTP 429 remains `rate_limited`; 401 and a 403 without a
capacity marker remain authentication/permission errors. The rejected upload
keeps its staged original. A later run re-reads the target before uploading, so
an uncertain write is never replayed and capacity restoration is accepted only
when new work succeeds. Real-account quota behavior is not yet tested; this is
an offline contract.

Original bytes are the backup contract. There is no quality selector, Storage
Saver simulation or recompression path.

## Immich contract

Polling uses `/api/server/version`, `/api/users/me`, `/api/albums`,
`/api/albums/{id}`, `/api/assets/{id}`, `/api/assets/{id}/original` and
`POST /api/search/metadata`. Minimum scopes are `user.read`, `album.read`,
`asset.read`, `asset.download`. Download originals without `edited:true`.

The researched flat metadata search (`albumIds`, `page`, `size`, `withExif`)
exists on both pins and is deprecated since v3.2. Scan each album separately:
multiple flat `albumIds` mean intersection. Require the assets envelope and an
explicit nullable `nextPage`; a short page or a guessed total is not completion.
Current structured search has different `filter`/`cursor` semantics; do not
mix shapes or retry arbitrary errors with another contract. Neither pagination
contract guarantees a database snapshot.

For v2.0.0, the adapter explicitly enumerates separate `timeline` and `archive`
streams for whole-library and owned-album scopes. Both streams must finish;
archived originals remain eligible. v3.3.4 retains the existing flat search
request. v2 shared albums are rejected as unsupported because their search can
omit other contributors' assets. Other v2/v3 releases are not individually
verified. A previously completed scan needs a new full scan to discover
archive history omitted by the earlier implementation.

Immich Workflows begin at v3.0; v2 has none. Discovery uses the plugin methods
and workflow triggers endpoints, not deprecated `/server/features`.
The service directly accepts native `AssetV1` payloads with `AssetCreate`,
`AssetMetadataExtraction` or `AssetTagged`, carrying the ID under `data.asset.id`.
Optional native `config` and `workflow` fields are accepted and discarded.
Payloads are limited to 64 KiB; only the stable UUIDv4 is retained and revalidated
against the configured source scope. See [event HTTP reference](events.md).
Acceptance is covered by offline contracts, not a real Immich Workflow run.

Events provide acceleration hints. There is no established upstream durable
acknowledgment/replay guarantee or album-membership event. Registration can stay
manual; the read-only Source seam never writes. The optional
[managed onboarding](../how-to/managed-workflows.md) uses a separate credential
and only the pinned v3.3.4 endpoints, `/api-keys/me` and `/workflows/triggers`
for capability detection; `PUT /workflows/{id}` is the documented update
operation even though upstream marks it deprecated in favour of an undocumented
`PATCH`. Immich v2 and missing grants fall back to polling with a notice. Keep
the polling key read-only. Polling remains necessary. Owned album mapping and bounded relation
reconciliation are implemented with offline tests; real Amazon album behavior
and regional writes remain unverified.

## Regions

The SDK implementation, rather than its broader descriptive region table,
controls accepted region codes.

| Code | Marketplace | SDK evidence | This service's live evidence |
| --- | --- | --- | --- |
| `us`, `com` | United States | `verified` upstream | untested |
| `ca` | Canada | `experimental` | partial, one account: uploads re-read and confirmed, album created and populated, account-wide MD5 reuse outside the target root and opt-in trash propagation verified; see [live evidence](#canada-live-evidence), [reuse regression evidence](#canada-reuse-regression-evidence) and [deletion propagation evidence](#canada-deletion-propagation-evidence) |
| `uk`, `co.uk` | United Kingdom | `experimental` | untested |
| `jp`, `co.jp` | Japan | `experimental` | untested |
| `at be bg ch cy cz de dk ee es fi fr gr hr hu ie is it li lt lu lv mt nl no pl pt ro se si sk` | Individual supported European marketplaces | `experimental` | untested |
| `au`, `com.au`, `tr`, `eu` | Unsupported by the pinned implementation | rejected | no support claim |

See [pinned regional implementation](https://github.com/thedavidweng/amazon-photos/blob/v0.1.4/pkg/aphotos/region.go).
Successful `doctor` reads do not upgrade a region's write evidence.
Validate each marketplace/media/album operation with disposable data before
relying on it. Record service and upstream versions, date, operation and result
without credentials. Echo Show refresh/playback remains a device behavior.

### Canada live evidence

One live `ca` account was exercised on 2026-10-21 with Immich `3.3.3` and this
service on `main`: nine JPEG originals uploaded, each re-read by node ID with
matching MD5, size, `AVAILABLE` status, owner and target parent; one Amazon
album was created through the returned ID and its membership was read back.
`doctor` reported `region=ca`, `evidence=experimental` and
`target_writes=untested`, because it performs no write.

Limits of that evidence: one account, one Immich user, one album, images only,
largest original about 12 MiB, no video, no credential renewal and no second
Amazon marketplace. It does not establish general `ca` compatibility.

The same run reproduced the content-conflict defect tracked by
[issue #13](https://github.com/thedavidweng/immich-amazon-sync/issues/13): two
assets already existed in the account under an album parent, so the
target-scoped lookup found nothing and the upload was recorded `unconfirmed`.
That was a service defect, not a region incompatibility, and it is not evidence
of successful `ca` compatibility either way.

### Canada reuse regression evidence

On 2026-10-22 the same `ca` account was rerun against a disposable Immich album
holding three originals: two already present in the Amazon account only outside
the target root, and one already at the root.

| Step | Service | Result |
| --- | --- | --- |
| Baseline | `main` before #14 | Both outside-root assets `unconfirmed` / `content_conflict`, two staged originals retained |
| Reconcile | merged #14 | Both outside-root assets `existing`, staging emptied, node IDs `dXXxMfGLR8ah5Xw8tQAfcQ` and `wcSL-kXQTGG_7j_B_OJt_w` |
| Repeat | merged #14 | `existing=2`, `confirmed=1`, `work_remaining=false`, `last_success` set, staging still empty |
| Album mirroring | merged #14 with `mirror_albums` | Album `-q_6sYtmTFiEM6uhtljx3A` created; membership read back listing both reused nodes |

Account-wide `/nodes` MD5 queries returned exactly one owned `AVAILABLE` node per
asset before and after every step, with matching size and the same node IDs, so
reuse performed no upload. The root asset was recorded `confirmed`
(`cWvZnLTGQd2XoUirEkqA5w`), not `existing`.

Limits: three JPEG assets, one account and one album; only nodes that also have a
folder parent were observed, so placement that is reachable *solely* through a
visual collection stays offline-only; the pre-#14 baseline was a local build of
that commit, not a released artifact.

### Canada deletion propagation evidence

On 2026-10-22 the same `ca` account ran the opt-in deletion path over the merged
reuse work. A disposable Immich album held twelve originals already mapped in
the Amazon account, so the pass performed no uploads.

| Step | Action | Result |
| --- | --- | --- |
| Baseline | `sync once` with `deletions: true` | `confirmed=10`, `existing=2`, `deletion_pending=0`; `deletions plan` empty |
| One source trash | Immich trash of one mapped asset | `deleted=1`, staging empty, `last_success` advanced |
| Node outcome | Re-read by ID | The mapped node status changed `AVAILABLE` -> `TRASH`; no other node changed |
| Guard | Immich trash of two more assets | Pass refused: `deletion_guard`, `deletion_pending=2`, `deletions plan blocked=true`; both nodes still `AVAILABLE` |
| Withdrawal | Restore in Immich, next pass | Pending intents withdrawn; both assets back to `confirmed`, plan empty |
| Opt-in default | Trash one asset with `deletions: false` | Asset stayed `confirmed`; its node stayed `AVAILABLE` |
| Cleanup | Restore in Amazon and Immich | Node `AVAILABLE` again; no permanent deletion |

Limits: one account, JPEG only, twelve mapped assets; the guard bound was
exercised with a 2-of-12 pass, so the absolute 500 limit stays offline-only; no
hard delete (asset that never becomes trashed) and no large-library behaviour
were observed; Amazon's trash retention window and permanent deletion were not
exercised.

## Originals and metadata

| Media/metadata | Intended handling | Evidence/limitation |
| --- | --- | --- |
| Eligible image original | Stream original bytes, stage within bounds, compute full MD5/size, verify remote evidence | Offline contracts; individual formats and Amazon rendering untested. |
| Standalone video | Explicit `filters.media_types` including `video`; no transcoding | Quota, accepted codecs and playback require real-account testing. |
| HEIC/HEIF, RAW | Preserve eligible original bytes without claiming preview support | Experimental/unverified format behavior. |
| Android embedded Motion Photo | Original image retains embedded motion bytes | No Amazon motion-playback guarantee. |
| Apple Live Photo | Still and hidden MOV can be separate originals | Still-only backup does not preserve the complete pair; no full Live Photo claim. |
| Hidden/locked/trashed/offline media | Excluded or explicitly reported as unsupported | Only an explicit `isTrashed` observation, with opt-in propagation and a re-verified node, can move a mapped node to the Amazon trash. Hidden, locked and offline states never do. |
| Embedded EXIF | Preserved inside unchanged original bytes | No metadata rewriting. |
| Source original bytes and filesystem mtimes | Read-only on every upload path | Staged and direct originals are never rewritten: no `utime`, no EXIF rewrite, no recompression. Tests assert unchanged bytes and mtimes. |
| Immich-only capture-date edits | Not written separately by the pinned SDK; doctor and status report `capture_date=metadata_not_supported` | Amazon infers the displayed date from the original bytes. When a capture date exists only in Immich metadata, or the original has no usable EXIF date, Amazon's displayed date can differ from Immich's capture date. |

Capture-date support is a capability, not an assumption. The pinned `aphotos`
v0.1.4 release exposes `contentDate` and the EXIF date properties only as
response data; its upload request, settings PATCH and rename cannot set them.
`doctor` and the status object therefore report the finite
`capture_date=metadata_not_supported` and the service never claims that an
Immich-only date edit is mirrored. If a future release adds a setter, it must be
proven by that released SDK and verified by an authoritative read-back of the
resulting node before `supported` is reported. Rewriting original bytes, EXIF or
filesystem timestamps is never an acceptable substitute.

Immich's public `checksum` is base64 SHA-1 bytes, never Amazon MD5. Current
external-library imports may use `SHA1("path:" + normalizedPath)`; the public
asset DTO does not reliably identify the checksum algorithm. Do not claim an
external source checksum verifies original bytes. Compute actual MD5 and byte
size from the staged stream; compare source SHA-1 only when its byte semantics
are established. Otherwise eligible external-library originals and assets with
unknown checksum provenance are backed up with an empty source SHA1 value and
actual computed MD5/size. An explicitly null `libraryId` establishes the
uploaded-original byte-checksum contract for source SHA1 validation. Unknown
provenance alone does not cause a skip; a known byte-checksum mismatch remains
an error. For assets without a byte-checksum, change detection compares the
reported size and Immich `fileModifiedAt` (filesystem mtime); metadata-only
edits that move `updatedAt` do not mark the asset as changed.

Sources: [library checksum assignment](https://github.com/immich-app/immich/blob/69e0cb8606137c4f8dc0e7207a672ff8be39f358/server/src/services/library.service.ts),
[Live/Motion linking](https://github.com/immich-app/immich/blob/69e0cb8606137c4f8dc0e7207a672ff8be39f358/server/src/services/metadata.service.ts),
[SDK upload fields](https://github.com/thedavidweng/amazon-photos/blob/v0.1.4/pkg/aphotos/upload.go).

## Favorite contracts

Immich `AssetResponseDto` at the pinned version requires a non-nullable
`isFavorite`, and the pinned example fixture also covers an absent field. The
adapter maps absence to unknown (`Favorite == nil`) and never to false. A source
without the field therefore performs no favorite write and reports
`favorites_unknown`.

The aphotos `NodeSettings.Favorite` typed value cannot prove presence: a
`settings` object without `favorite` decodes as a synthetic false, and the SDK
also synthesises a `NodeSettings` fallback when a PATCH response omits
`settings`. The adapter therefore reads the retained raw payload and requires an
explicit `settings.favorite` boolean for both the pre-write read and the
post-write verification. A missing field is `favorite_unconfirmed`, and the PATCH
response is never accepted as proof. A lost or timed-out mutation stays
uncertain until a fresh read settles it; no media is uploaded again.

These are offline contract grades from the pinned fixtures. A live disposable
account has not exercised `SetFavorite`; the dated provider matrix remains
issue #28 work.
