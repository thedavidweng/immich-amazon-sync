# Compatibility and evidence

Research baseline: **October 20, 2026, America/Vancouver**. Acquisition crossed
October 21 UTC. These pins describe inspected contracts; they do not establish
live-account success.

| Boundary | Pin/evidence | Limit |
| --- | --- | --- |
| Immich stable | `v3.3.4`, `69e0cb8606137c4f8dc0e7207a672ff8be39f358`, released October 20, 2026 | Official contract/source inspection and offline adapter fixtures; no real server tested for this delivery. |
| Immich comparison | `v2.0.0`, `e4234af3b3bd32f5dd12a2b0808158339deded43` | Older owned-media contract comparison; no complete shared-album guarantee. |
| Immich main | `166e93559f123313cf9cb4e7324c4ee30e21c618`, OpenAPI says `3.3.0-rc.0` | Stale development metadata, not the stable release number or minimum supported version. |
| aphotos SDK | `v0.1.3-0.20261021004957-6a7dc3ea2ff7` | Public revision `6a7dc3ea2ff7c971adbfae3ea72f7b33a9c0b501` from SDK PR #13; validates list envelopes and retains GetNode auth/rate-error preservation. |
| Local runtime | Offline Go, CLI and container contracts | No real Amazon upload, album mutation, format playback or credential renewal verified. |

Primary sources: [stable release](https://github.com/immich-app/immich/releases/tag/v3.3.4),
[stable OpenAPI](https://github.com/immich-app/immich/blob/69e0cb8606137c4f8dc0e7207a672ff8be39f358/open-api/immich-openapi-specs.json),
[v2 OpenAPI](https://github.com/immich-app/immich/blob/e4234af3b3bd32f5dd12a2b0808158339deded43/open-api/immich-openapi-specs.json),
[SDK GetNode](https://github.com/thedavidweng/amazon-photos/blob/6a7dc3ea2ff7c971adbfae3ea72f7b33a9c0b501/pkg/aphotos/account.go),
[SDK PR #13](https://github.com/thedavidweng/amazon-photos/pull/13).
The SDK pin is a public commit, not a released tag.
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

Read JSON requests require a final HTTP 2xx status. Mutation/upload handling
still accepts some final non-2xx statuses below 400, and HTTP 503 Retry-After
propagation remains unfixed. The service rejects credentialed redirects, but
these remain upstream protocol limitations. Keep this delivery experimental;
offline SDK tests and local packaging tests do not establish real cloud success.

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
contract guarantees a database snapshot. v2 shared albums can omit other
contributors' assets.

Immich Workflows begin at v3.0; v2 has none. Discovery uses the plugin methods
and workflow triggers endpoints, not deprecated `/server/features`.
`AssetCreate` provides an acceleration hint, not reliable delivery. Current
webhook payloads carry the asset under `data.asset`; there is no established
durable acknowledgment/replay guarantee or album-membership event. Manual
registration keeps the polling key read-only. Automatic discovery/registration
needs explicitly chosen plugin/workflow permissions. Polling remains necessary.

## Regions

The SDK implementation, rather than its broader descriptive region table,
controls accepted region codes.

| Code | Marketplace | SDK evidence | This service's live evidence |
| --- | --- | --- | --- |
| `us`, `com` | United States | `verified` upstream | untested |
| `ca` | Canada | `experimental` | untested |
| `uk`, `co.uk` | United Kingdom | `experimental` | untested |
| `jp`, `co.jp` | Japan | `experimental` | untested |
| `at be bg ch cy cz de dk ee es fi fr gr hr hu ie is it li lt lu lv mt nl no pl pt ro se si sk` | Individual supported European marketplaces | `experimental` | untested |
| `au`, `com.au`, `tr`, `eu` | Unsupported by the pinned implementation | rejected | no support claim |

See [pinned regional implementation](https://github.com/thedavidweng/amazon-photos/blob/6a7dc3ea2ff7c971adbfae3ea72f7b33a9c0b501/pkg/aphotos/region.go).
Successful `doctor` reads do not upgrade a region's write evidence.
Validate each marketplace/media/album operation with disposable data before
relying on it. Record service and upstream versions, date, operation and result
without credentials. Echo Show refresh/playback remains a device behavior.

## Originals and metadata

| Media/metadata | Intended handling | Evidence/limitation |
| --- | --- | --- |
| Eligible image original | Stream original bytes, stage within bounds, compute full MD5/size, verify remote evidence | Offline contracts; individual formats and Amazon rendering untested. |
| Standalone video | Explicit `include_videos`; no transcoding | Quota, accepted codecs and playback require real-account testing. |
| HEIC/HEIF, RAW | Preserve eligible original bytes without claiming preview support | Experimental/unverified format behavior. |
| Android embedded Motion Photo | Original image retains embedded motion bytes | No Amazon motion-playback guarantee. |
| Apple Live Photo | Still and hidden MOV can be separate originals | Still-only backup does not preserve the complete pair; no full Live Photo claim. |
| Hidden/locked/trashed/offline media | Excluded or explicitly reported as unsupported | Not evidence of source deletion; never authorizes media removal. |
| Embedded EXIF | Preserved inside unchanged original bytes | No metadata rewriting. |
| Immich-only capture-date edits | Not written separately by the current SDK | Amazon inferred dates can differ. |

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
an error.

Sources: [library checksum assignment](https://github.com/immich-app/immich/blob/69e0cb8606137c4f8dc0e7207a672ff8be39f358/server/src/services/library.service.ts),
[Live/Motion linking](https://github.com/immich-app/immich/blob/69e0cb8606137c4f8dc0e7207a672ff8be39f358/server/src/services/metadata.service.ts),
[SDK upload fields](https://github.com/thedavidweng/amazon-photos/blob/6a7dc3ea2ff7c971adbfae3ea72f7b33a9c0b501/pkg/aphotos/upload.go).
