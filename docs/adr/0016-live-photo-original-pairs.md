# 0016: Preserve Apple Live Photos as original pairs

Status: accepted. Date: 2026-10-22.

Immich links an Apple Live Photo still to a hidden MOV through
`livePhotoVideoId`. The released behavior excluded unrelated hidden media, and
the default `live_photo_mode: still_only` keeps exactly that: the link is not
followed, the companion keeps its `hidden` exclusion, and no migration enables
pairing. `original_pair` is an explicit per-account opt-in.

In pair mode the service resolves only the companion referenced by an included
still. Both originals are staged from their own source bytes, get their own MD5
and size, and become independent Amazon Nodes. Nothing is muxed, converted or
rewritten, no derived hash replaces a source hash, and the service writes no
Apple pairing or playback claim to Amazon. Every other filter rule still applies
to the companion; resolving the link never admits unrelated hidden or
standalone video media.

Pair membership is durable in one ledger table and separate from media state. A
pair is complete only when both still and companion have a verified Node; a
missing, trashed, offline or ineligible companion reports `pair_incomplete`
instead of claiming a full Live Photo backup. Switching modes adopts stills
already in the ledger from stored metadata without re-downloading or re-uploading
them, withdraws pair bookkeeping when disabled, and never rewrites or trashes a
Node. Only a companion explicitly trashed at the source, with deletion
propagation enabled and the normal guarded pass, is moved to the Amazon trash;
the pair is then retired rather than reported incomplete.

Amazon's published iOS Live Photo support is separate from unproven SDK pairing
and playback. Native pairing stays unsupported until disposable samples prove it,
and Google/Samsung Motion Photo muxing is deliberately not ported.
