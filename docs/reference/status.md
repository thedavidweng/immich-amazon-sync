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
| `associations_pending` | Active membership operations not settled as present or absent. |
| `associations_failed` | Active membership operations with recorded errors. |
| `associations_unconfirmed` | Membership addition/removal intents requiring reconciliation. |
| `events_pending` | Durable unresolved hints, including failed hints. |
| `events_failed` | Hints with a recorded processing error. |
| `incremental_scanning` | A library delta scan is active. |
| `last_incremental` | Completed library discovery watermark, or `null`; this is the scan-start boundary, not proof of transfer completion. |
| `work_remaining` | Unpaused work is eligible for continuation now. False does not mean every job succeeded. |
| `next_attempt` | Optional earliest scheduled retry timestamp; omitted when none is scheduled or the account is paused. |
| `capture_date` | Capture-date write capability of the pinned target SDK. `metadata_not_supported` means the service cannot set a capture date remotely and does not claim that Immich-only date edits are mirrored; `supported` is reserved for a released SDK operation verified by an authoritative read-back. The field is always present and is never an implied success. |

Counts such as pending, failed and unconfirmed album work overlap; do not add
them to infer a total. Disabling album mirroring reports zero album/association
counts without deleting preserved state. No private asset IDs, filenames, paths,
URLs or raw remote errors are included in these aggregates.

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
