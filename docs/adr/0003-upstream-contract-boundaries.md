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
`71d21d31336d24990145f93cd0c2b164fbb30941`, Go pseudo-version
`v0.1.3-0.20261019091212-71d21d31336d`. The divergent published v0.1.3 tag masks
some GetNode auth/rate failures with another lookup. Use the fixed SDK revision,
not a duplicate HTTP client or local workaround. This pseudo-version orders
below the tag; future dependency updates must preserve the semantic fix.

The pinned SDK still loses field-presence evidence when decoding list responses.
Reject ambiguous/empty searches and empty member lists as unconfirmed, accepting
that this blocks first uploads and empty-album reconciliation until a generic
SDK validation fix is pinned. Do not implement a second Amazon HTTP parser in
this service. Final non-2xx acceptance and incomplete transient Retry-After
propagation remain upstream limitations.

Source checksums are not universally byte checksums: external libraries can
use SHA1 of a normalized path. The public response does not reliably expose
algorithm provenance. Compute destination MD5 and size from actual staged
bytes; only assert source byte-SHA1 equality when provenance is established.
Do not silently waive a known byte mismatch or promise verification of an
unknown/external source checksum.

The initial adapter excludes nonempty external `libraryId` and missing
`libraryId` provenance with explicit reasons. Only explicitly null `libraryId`
establishes the uploaded-original byte-checksum contract. These exclusions are
not successful backups.

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
