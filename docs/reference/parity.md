# Parity baseline and evidence grades

Comparison date: **2026-10-22 UTC**. This is the checked-in baseline for the
Amazon parity program in issue #16. It names each capability in scope, its
current evidence grade and the ticket that delivers it. It never upgrades
offline contracts into live claims; dated provider evidence stays in
[compatibility.md](compatibility.md).

## Compared trees

| Tree | Exact pin | Notes |
| --- | --- | --- |
| This service | `main` `53877cc81e7368b00aa308be034a87ac5197726e` | PR #15 merged 2026-10-22; the acceptance baseline. |
| User-maintained Google fork, combined #5/#6/#7 tree | `thedavidweng/immich-gphotos` `3c2ec7925436e9eddea6c001253ede7cba3aaed4` (2026-10-17T05:04:31Z) | The integrated head shared by PRs #5, #6 and #7. This, not the fork's current `main`, is the comparison baseline. |
| Same fork, separate #9 fix | `da17f7c024932027065d664ebe5b7c65ae67e4ba` (2026-10-20T01:57:52Z) | Must be applied on top of the combined tree for a complete comparison. |
| `aphotos` SDK | `v0.1.4`, tag commit `366da81406f806924af6d9a3934564a6c3d84e73` (released 2026-10-21T03:59:14Z) | `SetFavorite`, `GetUsage`, `NodeSettings`. |
| Immich contract | `v3.3.4`, `69e0cb8606137c4f8dc0e7207a672ff8be39f358` | Pinned OpenAPI extraction and asset fixtures. |

The four user-authored fork PRs were **open and unmerged** when the spec was
written, and are still open at this comparison. The fork's current `main` does
not contain them, so it must not stand in for the combined tree.

## Upstream PR state, verified 2026-10-22

Checked with `gh api repos/ImAleeexx/immich-gphotos/pulls/<n>`; `merged` was
`false` and `state` was `open` for every row.

| PR | Title | State | Head | Last updated (UTC) |
| --- | --- | --- | --- | --- |
| #5 | `fix(sync): re-date direct-read originals whose mtime drifted from the capture date` | open, unmerged | `3c2ec79` | 2026-10-17T05:05:00Z |
| #6 | `feat(sync): sync favorite status from Immich to Google Photos` | open, unmerged | `3c2ec79` | 2026-10-17T05:05:03Z |
| #7 | `feat(sync): mux Apple Live Photos into Motion Photos before upload` | open, unmerged | `3c2ec79` | 2026-10-17T05:05:05Z |
| #9 | `fix(store): migrate legacy 'trashed' asset state to ineligible` | open, unmerged | `da17f7c` | 2026-10-20T01:58:42Z |

## Evidence grades

| Grade | Meaning |
| --- | --- |
| **Live evidence** | Exercised against a disposable real Amazon account; operation, date and limits are recorded in [compatibility.md](compatibility.md). |
| **Offline contract** | Proven only by pinned offline fixtures and repository tests. No live-provider claim. |
| **Not yet tested** | Promised by the spec but not implemented or proven yet. The delivering ticket names the grade it must reach. |
| **Unsupported** | No verified Amazon capability exists, or the capability is Google-specific and deliberately not ported. No implementation or user-facing claim. |

## Capability record

| Capability | Combined `immich-gphotos` baseline | This service at `53877cc` | Evidence grade | Delivering ticket |
| --- | --- | --- | --- | --- |
| Initial backfill and periodic reconciliation | Yes | Yes | Live evidence (CA, 2026-10-21) | #17 preserves (baseline shipped) |
| Manual native event/webhook ingestion | Yes | Yes | Offline contract | #17 preserves |
| Automatic Workflow create/update/remove | Yes | No (manual only) | Not yet tested | #25 |
| Read-only Workflow log diagnostics | Yes | Doctor only | Not yet tested | #25 |
| Strict dedup and verified remote identity (MD5, size, owned Node) | SHA-1 via `gpmc` | MD5/size/owned Node; `confirmed`/`existing` | Live evidence | #17 preserves |
| Byte-identical originals; no recompression | Yes | Yes | Offline contract + live evidence | #17 preserves; #31 |
| Opt-in trash propagation, 500-item/10% guard, trash only | Yes | Yes (PR #15) | Live evidence (CA, 2026-10-22) | #17 regression coverage; #15 shipped |
| Favorite reads with field-presence semantics | PR #6 tree | Not exposed; fixtures pinned | Offline contract | #18 |
| Star new uploads and Existing Nodes | Yes | No | Not yet tested | #18 |
| Add-only mode that never unstars | Not offered | No | Not yet tested | #18 |
| Authoritative mirror/unstar with consent | PR #6 tree | No | Not yet tested | #19 |
| Shared-Node conflict, drift repair, read-before-retry | No | No | Not yet tested | #19 |
| Trusted read-only direct originals with API fallback | PR #5 tree (mtime re-date) | No | Not yet tested | #23 |
| Explicit selected-album mirroring | Yes | Yes | Live evidence (one album) | #17 preserves |
| All-accessible album discovery and mirroring | Yes | No | Not yet tested | #24 |
| Album rename/removal safeguards | Google-specific | Owned map + bounded membership guard | Offline contract | #17 preserves; #24 extends |
| Destination album capacity handling | 20,000-item split | No assumed limit | Unsupported (20k not ported) | #24 only after verified evidence |
| Daily per-account transfer window | Engine window, no UI | No | Not yet tested | #20 |
| Per-account workers and uplink byte cap | Global-ish worker/cap | Workers + byte cap existing | Offline contract | #17 preserves; #20/#27 clarify |
| Configurable retry policy | Engine object | Fixed conservative bounds | Not yet tested | #27 |
| Filters: type, video, size, RAW, archive, tags, album allowlist | Engine partial | Limited type/video/scope | Offline contract | #21 |
| Durable, reevaluable, non-destructive exclusions | Partial | Skip reasons only | Not yet tested | #21 |
| Categorized storage usage (`photo`, `video`, `other`, `lastCalculated`) | Quota mode proxy | Surfaced in doctor, status, CLI and dashboard with observed bytes/counts | Offline contract | #22 |
| Honest unknown capacity; no quality selector | No | Original bytes only; capacity, remaining bytes and Prime entitlement are `unknown` | Offline contract | #22 |
| Video advisory threshold and opt-in hold; quota error classification | No | Explicit per-account budget; distinct `storage_quota_exceeded` keeps originals staged | Offline contract | #22 |
| Capture date for missing EXIF | PR #5 re-dates direct reads | Preserves EXIF; no source writes | Unsupported (no verified SDK setter) | #31 |
| Apple Live Photo still + MOV original-pair preservation | PR #7 muxes a derived Google Motion Photo | Still-only; hidden MOV excluded | Offline contract (planned) | #26 |
| Native Apple Live Photo pairing and playback | No | No | Unsupported until disposable samples prove it | #30 |
| Google/Samsung Motion Photo mux | Yes (PR #7) | No conversion path | Unsupported (deliberately not ported) | #26 keeps original pairs; #30 is the only native-pairing experiment |
| Managed multi-account local UI | Yes | Yes | Offline contract | #17 preserves |
| Prometheus `/metrics` with low cardinality | Yes | No | Not yet tested | #27 |
| Health/readiness independent of Amazon | Variable | Yes | Offline contract | #17 preserves |
| Status, CLI and UI parity for new state | Partial | Baseline counters | Not yet tested | #27 |
| Fail-closed unknown and partial operations | Variable | Explicit `Unconfirmed` and guards | Offline contract + live evidence | #17 preserves |
| Dated live-provider matrix for new writes | None | Partial CA evidence for existing writes | Live evidence (limited) | #28 |
| Upgrade and dual-architecture release gate | Migration #9 | Schema v3 plus v1 fixture | Offline contract | #29 |

## Deliberately not ported (Google-only)

| Capability | Why it is not ported |
| --- | --- |
| Quality modes `original` / `saver` / `quota` | These are options of the unofficial Google upload protocol. Amazon Photos has no verified isomorphic quality selector: eligible Prime images are stored at original resolution, while videos and other files count separately. Adding an unfulfillable tri-state selector would mislead users and risk data loss. The service always uploads original bytes and reports real usage instead (#22). |
| Motion Photo muxing (PR #7) | Producing a Google/Samsung Motion Photo creates derived media with a new hash and dedup identity. Amazon's official format documentation lists Google/Samsung Motion Photos as unsupported. The conversion is therefore not ported: #26 keeps the still and MOV as independent originals, and #30 may test native Apple pairing with disposable samples only. |
| 20,000-item album split | The 20,000-item bound is a Google Photos limit, not an Amazon one. Copying it would invent overflow behaviour and uncertain duplicate album creation. Amazon capacity handling is implemented only after verified limit evidence (#24); until then no limit is assumed. |

Other Google-only machinery named in the spec's out-of-scope section (Google
credential extraction, WebADB wizard, `gpmc`, Google endpoints) is likewise not
part of this implementation.

## Pinned offline fixtures

Every later slice reads these fixtures instead of inventing request or response
shapes.

### Immich v3.3.4

- `internal/immich/testdata/immich-v3.3.4.openapi.json` — the consumed
  operations and unchanged transitive schemas, with upstream version, commit,
  URL and full-document SHA-256 in `x-source`.
- `internal/immich/testdata/immich-v3.3.4-assets.json` — schema-conforming asset
  examples for favorite true/false/absent, archive visibility, tags
  present/empty/absent, Live Photo still and hidden companion, explicit
  `livePhotoVideoId: null` versus a missing field, and internal versus external
  `libraryId`/`originalPath`.
- `TestPinnedOfficialOpenAPIContract` and `TestPinnedAssetPresenceSemantics`
  prove `isFavorite`, `isArchived`, `visibility` and `originalPath` are required
  and non-nullable; `tags` and `livePhotoVideoId` are optional, with
  `livePhotoVideoId` nullable. `TestPinnedAssetFieldPresence` proves an absent
  `isFavorite` decodes as unknown, not as `false`.

### aphotos SDK v0.1.4

- `internal/amazon/testdata/aphotos-v0.1.4-settings-usage.json` — raw node
  payloads with no `settings`, explicit `settings.favorite` false/true, and a
  `settings` object without `favorite`; a PATCH response without `settings`; a
  categorized usage response and one without `lastCalculated`. `x-source` names
  the SDK tag commit and inspected files.
- `TestPinnedNodeFavoritePresence` proves the SDK distinguishes a missing
  `settings` object (`Settings == nil`) from explicit false, while a `settings`
  object without `favorite` yields the ambiguous typed `false`. Only the
  retained raw payload proves presence, so an absent field can never be read as
  "not favorite".
- `TestPinnedFavoriteMutationResponseCannotStandAsProof` proves the SDK's
  synthetic `NodeSettings` fallback in a PATCH response is not evidence and the
  node must be re-read.
- `TestPinnedUsageCategoriesAndLastCalculated` and
  `TestPinnedUsageWithoutLastCalculatedStaysStale` pin the categorized
  `billable`/`total` shape, `lastCalculated`, unknown category retention and the
  zero-time stale sentinel.
- `TestUsageMapsObservedCategoriesAndKeepsUnknownCategories` and
  `TestUsageWithoutLastCalculatedRemainsVisiblyStale` pin the adapter mapping
  from the same fixture, including unknown-category retention and the zero-time
  sentinel. `TestQuotaRejectionIsDistinctFromRateLimitAndAuthentication` pins
  quota classification against 403/400/409 capacity messages, HTTP 507, a plain
  permission denial, 429 and 401.
- `TestUsageStatusLabelsObservedStaleAndUnavailable` pins observed values,
  stale preservation and unavailable-without-invention. `TestQuotaRejection...`
  pins a staged original surviving a quota rejection and resuming after a fresh
  target read under deletion-on and deletion-off.
  `TestVideoThresholdHoldIsOptInAndResumesWhenUsageFalls` and
  `TestVideoThresholdWithoutHoldOrFreshUsageCannotPauseUploads` pin the
  per-account video advisory and hold behaviour.

### Non-regression tests

`TestBaselineConfirmedAndExistingAreDistinct`, `TestBaselineUnconfirmedRecoveryDoesNotRepost`,
`TestDeletionGuardThresholds` and `TestMembershipRemovalGuardThresholds` pin the
confirmed/existing distinction, read-only Unconfirmed recovery with no second
POST, the deletion guard (exactly 500 allowed, 501 refused, exactly 10% allowed,
above 10% refused) and the owned-membership removal guard under deletion-on and
deletion-off settings where deletion state is relevant.
