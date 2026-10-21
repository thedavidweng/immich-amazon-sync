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
v2 shared-album discovery is not a completeness guarantee.

Pin the public aphotos SDK to revision
`6a7dc3ea2ff7c971adbfae3ea72f7b33a9c0b501`, Go pseudo-version
`v0.1.3-0.20261021004957-6a7dc3ea2ff7`, published through
[SDK PR #13](https://github.com/thedavidweng/amazon-photos/pull/13).
This is a public commit pin, not a released tag. It retains the GetNode
auth/rate-error preservation missing from the divergent published v0.1.3 tag.
Use the fixed SDK revision, not a duplicate HTTP client or local workaround.
This pseudo-version orders below the tag; future dependency updates must
preserve both error propagation and strict list validation.

Strict SDK list decoding accepts `{"count":0,"data":[]}` and rejects missing or
null fields, invalid counts and items without IDs. Valid empty searches and
member lists no longer block first uploads or empty-album reconciliation.
An incomplete enumeration still cannot prove absence: check `HasMore`, count
and progress, not only a single page's `Complete` flag. An empty search after
an uncertain upload still cannot prove noncommit because indexing may lag.
Do not implement a second Amazon HTTP parser in this service.

Read JSON requests now require a final HTTP 2xx status. Mutation/upload
acceptance of some final non-2xx statuses below 400 and HTTP 503 Retry-After
propagation remain upstream limitations.

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
upload capture-date field/setter. Apple Live Photo hidden MOV companions are
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
