# 0017: Capture-time fidelity and honest metadata limits

Status: accepted. Date: 2026-10-22.

The pinned `aphotos` v0.1.4 release exposes `contentDate` and the EXIF date
properties only as response data. Its upload request, settings PATCH and rename
cannot set them. There is therefore no verified way to mirror a capture date that
exists only in Immich metadata, and the service must not pretend otherwise.

Doctor and status report the finite `capture_date=metadata_not_supported`
capability. When a capture date lives only in Immich, or the original has no
usable EXIF date, Amazon's displayed date can differ from Immich's; documents say
so instead of promising a write. Rewriting original bytes, EXIF or filesystem
mtimes to "fix" the date is never acceptable: it destroys the backup contract and
creates a different hash.

Original bytes are unchanged on every upload path, including the trusted direct
read. Tests assert byte identity and unchanged mode and mtime for staged and
mounted originals, and a production-code guard forbids timestamp or EXIF writes
from creeping into a later read path. Embedded EXIF is preserved precisely
because the bytes are.

A future release may add a setter. Enabling `supported` then requires that
released operation plus an authoritative read-back of the resulting Node; an
offline fixture or an upload response alone is not proof. Until then the honest
capability stays `metadata_not_supported`, and capture-date parity remains
explicitly unsupported rather than simulated.
