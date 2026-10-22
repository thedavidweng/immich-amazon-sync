# 0003: Pin researched contracts and qualify original fidelity

Status: accepted. Date: 2026-10-20.

The implementation research found material differences from the initial spec.
Record them here without rewriting that baseline.

Use Immich stable v3.3.4, released October 20, 2026, commit
`69e0cb8606137c4f8dc0e7207a672ff8be39f358`, as the current contract pin. Main's
OpenAPI `3.3.0-rc.0` is stale metadata, not a stable release or minimum supported
version. Compare v2.0.0 separately. Keep pinned contract tests offline; moving
upstream monitoring must not make PR tests nondeterministic.

The initial adapter may deliberately use the flat metadata-search contract
shared by those pins, accepting its deprecation since v3.2. Search each album
separately and validate explicit pagination; neither flat pages nor current
cursors imply a snapshot. Do not retry errors using a different request shape.
For v2.0.0, explicitly enumerate both timeline and archive streams for library
and owned-album scopes; completion requires both. Archived originals remain
eligible. Reject v2 shared albums as unsupported because their search can omit
other contributors. v3.3.4 keeps the existing flat request; other v2/v3 releases
are not individually verified. Previously completed history needs a new full
scan to discover archived originals omitted before this correction.
Both v2 album metadata reads must request `withoutAssets=true`; v2 otherwise
embeds all assets and defeats bounded discovery. The pinned v3 metadata
response omits embedded assets and has no such query parameter.

Pin the public aphotos SDK to released tag `v0.1.4`, published through
[SDK PR #13](https://github.com/thedavidweng/amazon-photos/pull/13).
The release includes strict list and final-status validation, preserves
GetNode auth/rate-error propagation, and retains parsed Retry-After delays.
Use the released SDK, not a duplicate HTTP client or local workaround.
Future dependency updates must preserve error propagation, strict list
validation and final-status checks.

Strict SDK list decoding accepts `{"count":0,"data":[]}` and rejects missing or
null fields, invalid counts and items without IDs. Valid empty searches and
member lists no longer block first uploads or empty-album reconciliation.
An incomplete enumeration still cannot prove absence: check `HasMore`, count
and progress, not only a single page's `Complete` flag. An empty search after
an uncertain upload still cannot prove noncommit because indexing may lag.
Do not implement a second Amazon HTTP parser in this service.

JSON reads, mutations and uploads require a final HTTP 2xx status. Transient
errors now retain server Retry-After, including HTTP 503 and response-body
failures. The adapter preserves that delay through unconfirmed upload and
post-verification results. A failed status or a retry delay still cannot prove
an upload did not commit; reconcile uncertain writes before any replay.

Bound each adapter metadata operation, including album mutations, to 30 seconds
through response headers and JSON decoding; earlier caller cancellation or
deadlines take precedence. Read expiry is `remote_unavailable`; expiry after
mutation or during post-upload verification is `upload_unconfirmed`. Keep
original downloads and SDK uploads on the caller context without a fixed
whole-media deadline. Give post-upload metadata verification its own fresh
deadline rather than limiting media transfer duration.

Source checksums are not universally byte checksums: external libraries can
use SHA1 of a normalized path. The public response does not reliably expose
algorithm provenance. Compute destination MD5 and size from actual staged
bytes; only assert source byte-SHA1 equality when provenance is established.
Do not silently waive a known byte mismatch or promise verification of an
unknown/external source checksum.

Otherwise eligible originals with nonempty external `libraryId` or missing
`libraryId` provenance are backed up with an empty source SHA1 value and MD5/size
computed from the actual staged bytes. Only explicitly null `libraryId`
establishes the uploaded-original byte-checksum contract for source SHA1
validation. Unknown provenance does not exclude an original; a known
byte-checksum mismatch remains an error.

Preserving an original preserves its embedded EXIF and any embedded motion
bytes. It does not copy Immich-only timestamp edits: the SDK has no public
upload capture-date field/setter, so doctor and status report the finite
`metadata_not_supported` capability and Amazon's inferred date can differ from
Immich's capture date. If a later SDK release adds a setter, require an
authoritative read-back before reporting `supported`; never rewrite EXIF or
filesystem mtimes instead. Apple Live Photo hidden MOV companions are
separate assets and may not be enumerated with the still. Do not claim complete
paired-original backup or Amazon Live Photo playback without a separate tested
companion policy. HEIC/RAW/video acceptance and all regional/album mutations
retain explicit evidence grades.

Upstream `verified` US routing is SDK provenance, not live verification of this
service. All other accepted regions remain experimental; unsupported au/tr/eu
codes must not acquire support through broader upstream prose. Read-only probes
cannot promote write evidence.

Pinned primary-source links and the evidence matrix are in
[compatibility](../reference/compatibility.md).
