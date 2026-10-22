# Durable progress and status

`sync once --json` returns an `accounts` array. Each entry contains an anonymous
`account` identifier, a finite `code` and a `status` object. Scheduled service
cycles log aggregate status; the optional authenticated dashboard presents an
overview. There is no public JSON status endpoint.

| Status field | Meaning |
| --- | --- |
| `pending`, `staged`, `running`, `unconfirmed`, `confirmed`, `existing`, `deletion_pending`, `deleted`, `failed`, `blocked`, `skipped` | Media counts by durable state. `confirmed` means a verified node at the configured target parent; `existing` means a verified copy already exists elsewhere in the same Amazon account (another folder or only inside an album) and was reused instead of uploading; `deletion_pending` means Immich reports the asset trashed and the mapped node awaits the sweep; `deleted` means the mapped node was moved to the Amazon trash; `skipped` is not a cloud backup. |
| `scanning` | Active full-scan rules without a completed generation. |
| `paused`, `pause_reason` | Account pause; `pause_reason` is omitted when empty. Manual pause remains until resumed. |
| `errors` | Counts by finite code, including media, active rules, hints and requested album work. |
| `last_success` | Most recent completed account reconciliation, or `null`. |
| `albums_pending` | Active managed albums lacking a target ID, current completed generation or applied name. |
| `albums_failed` | Active managed albums with recorded errors. |
| `albums_unconfirmed` | Album creation may have committed without an established returned target ID. |
| `album_sync_mode` | Active mode: `off`, `selected` or `all_accessible`. |
| `albums_active` | Album rules currently in scope and scheduling work. |
| `albums_inaccessible` | Discovered albums absent from the latest complete accessible listing. Mirroring for them is stopped; their owned destination album, media and memberships are preserved. Also reported as `album_inaccessible` in `errors`. |
| `associations_pending` | Active membership operations not settled as present or absent. |
| `associations_failed` | Active membership operations with recorded errors. |
| `associations_unconfirmed` | Membership addition/removal intents requiring reconciliation. |
| `events_pending` | Durable unresolved hints, including failed hints. |
| `events_failed` | Hints with a recorded processing error. |
| `incremental_scanning` | A library delta scan is active. |
| `last_incremental` | Completed library discovery watermark, or `null`; this is the scan-start boundary, not proof of transfer completion. |
| `work_remaining` | Unpaused work is eligible for continuation now. False does not mean every job succeeded. |
| `next_attempt` | Optional earliest scheduled retry timestamp; omitted when none is scheduled or the account is paused. |
| `usage.state` | `observed`, `stale` or `unavailable`. `observed` requires the provider's calculation timestamp; `stale` keeps existing values but labels a missing timestamp or a failed latest refresh; `unavailable` means no observation exists. |
| `usage.capacity`, `usage.entitlement` | Always `unknown`. Subscription cap, remaining bytes and Prime entitlement are never inferred from usage, region or zero photo counts. |
| `usage.categories` | Observed `photo`, `video`, `other` and any additional provider category, each with `billable`/`total` bytes and counts. Omitted when unavailable; never replaced with zeros. |
| `usage.last_calculated`, `usage.observed_at` | Provider calculation time and local observation time; omitted when unknown. |
| `usage.error_code` | Finite code from the latest usage refresh attempt; omitted when none. |
| `usage.video_threshold_bytes`, `usage.video_threshold_exceeded` | Configured per-account advisory budget and whether fresh observed billable video bytes reach it. |
| `usage.video_hold`, `usage.quota_hold` | New video uploads held at the advisory threshold; uploads held after a real quota rejection. A hold preserves staged originals and never confirms a write. |

Usage is an observation, not a capacity promise. Original bytes are the backup
contract: there is no quality selector, Storage Saver mode or recompression
path. `rate_limited` and `storage_quota_exceeded` are separate finite codes; a
quota rejection keeps the staged original durable and is re-attempted only
after a fresh target read on a later run, so an uncertain write is never
replayed. Usage is refreshed by a sync run; `stale` and `unavailable` are
normal labels for old or failed reads, not zero usage.

Counts such as pending, failed and unconfirmed album work overlap; do not add
them to infer a total. Disabling album mirroring reports zero album/association
counts without deleting preserved state. No private asset IDs, filenames, paths,
URLs or raw remote errors are included in these aggregates.

`all_accessible` reads the complete accessible album listing on a full pass and
periodic reconcile, not on every bounded continuation. A failed listing changes
nothing. An album absent from a successful listing stops mirroring and becomes
`albums_inaccessible`; its mappings and memberships stay, and it is never
treated as deletion evidence.

`last_success` advances only after authoritative full scans, active incremental
work and hints are complete, eligible media is settled, and requested managed
album generations are reconciled. Previous success remains historical evidence
when a later cycle fails. A healthy process, `202 accepted`, completed discovery
watermark or successful bounded CLI pass does not establish that boundary.

`serve` schedules periodic reconciliation, event wakes, immediate bounded
continuation and due retries independently. `scan_interval` is not a cycle
deadline. Library mode overlaps incremental reads by two minutes and starts full
verification when its completed full scan is older than 24 hours. Explicit album
mode performs periodic full membership scans. Failed/partial pagination does
not advance the completed watermark; cursors and work persist across restart.

Event work has priority over library deltas, which have priority over historical
backfill. A new hint can take the next available worker slot; an in-flight media
transfer is not interrupted, and there is no fixed latency guarantee. Accounts
have independent cycles, so one busy or paused account does not serialize all
other accounts.
