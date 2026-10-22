# 0015: Honest storage usage and an opt-in video hold

Status: accepted. Date: 2026-10-22.

`aphotos` v0.1.5 exposes categorized `GetUsage` observations and their provider
calculation time. It does not expose a subscription cap, remaining bytes or Prime
eligibility. The service therefore reports observed `photo`, `video` and `other`
bytes and counts next to `capacity: unknown` and `entitlement: unknown`, and a
zero billable count or low usage never implies Prime. The observation is
normalized and stored in the existing ledger so status, doctor, CLI and the
dashboard agree; no raw remote payload or inferred capacity is retained.

Quality selectors are not implemented. Original bytes are the backup contract,
and an unfulfillable `original`/`saver`/`quota` tri-state would either mislead or
recompress media. The Google-only quality modes stay deliberately unsupported.

The optional per-account `video_usage_threshold_bytes` compares a user-provided
budget with fresh observed billable video bytes. `hold_videos_at_threshold` stops
dispatching new video uploads at or above it; photos, metadata work and
verification continue, and staged originals stay durable. A stale or unavailable
usage read cannot activate the hold, so an outage never invents a pause.

A quota rejection is classified `storage_quota_exceeded` only from an explicit
typed non-429 response: HTTP 400, 403 or 409 carrying a capacity marker, or HTTP
507. HTTP 429 stays `rate_limited`, and 401 or a 403 without a capacity marker
stays authentication/permission. The rejected upload keeps its staged original,
and a later run re-reads the target before uploading, so an uncertain write is
never replayed and restored capacity is accepted only when new work succeeds.
Real-account quota behavior remains unverified and is documented as such.
