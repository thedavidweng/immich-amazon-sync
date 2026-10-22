# Immich source contract

The offline contract is pinned to official Immich v3.3.4, commit
`69e0cb8606137c4f8dc0e7207a672ff8be39f358`. The fixture contains only the seven
consumed operations and their unchanged transitive schema definitions; its
`x-source` records the upstream URL and full document SHA256.
A separate minimal v2.0.0 fixture pins the album metadata query parameters.

The source requires `user.read`, `album.read`, `asset.read`, and `asset.download`.
It makes no source writes. The configured base is fixed, API-key redirects are
rejected, and asset IDs cannot supply URLs. Identity is an opaque hash of the
configured API base and remote user UUID, independent of key rotation.

Album membership uses the single flat `/search/metadata` contract with `albumIds`,
`page`, `size`, `order`, and `withExif`. This contract exists in the researched
v2.0.0 and v3.3.4 pins and is deprecated since v3.2. There is no request-shape
fallback. v2 shared albums are rejected because their search can omit other
contributors. Other v2/v3 releases have not been individually verified. Album
metadata `assetCount` includes ineligible entries and is not an eligible count.
Both v2 album metadata paths explicitly request `withoutAssets=true` so the
server does not embed the whole album. v3 omits this query; its pinned response
does not embed assets. Membership still comes from bounded search pages.
Pages require explicit count, items, and nullable next-page evidence. These APIs
do not provide snapshot isolation; callers must retain incomplete scan state and
perform periodic full reconciliation.

`SHA1` is base64 byte SHA1 only for uploaded assets with explicit null `libraryId`.
Known external-library assets use path checksums, and missing provenance cannot
prove byte-SHA1 semantics. Both remain eligible with empty `SHA1`; staging must
compute actual content MD5 and size. A byte-SHA1 mismatch with known provenance
must still fail. Unknown original size is `-1`; absent MIME remains empty and
the media kind remains authoritative.

`originalPath` is read as a transient field. It is never persisted in the asset
metadata, never logged and only used to resolve one approved read-only mount;
callers re-read it per transfer instead of trusting a stored copy.

Hidden, locked, trashed, offline, AUDIO and OTHER entries remain in discovery
pages with finite `ExcludedReason` values for callers to skip. Archive remains
eligible. `LivePhotoVideo` describes a hidden VIDEO only, never the still image
which references a companion. Downloading the original still preserves embedded
Android motion data but does not guarantee preservation of a separate Apple MOV
or Amazon Live Photo playback. No pairing or transcoding is performed.
