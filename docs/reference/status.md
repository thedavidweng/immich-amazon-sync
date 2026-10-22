# Durable progress and status

`sync once --json` returns an `accounts` array. Each entry contains an anonymous
`account` identifier, a finite `code` and a `status` object. Scheduled service
cycles log aggregate status; the optional authenticated dashboard presents an
overview. There is no public JSON status endpoint.

| Status field | Meaning |
| --- | --- |
| `pending`, `staged`, `running`, `unconfirmed`, `confirmed`, `existing`, `deletion_pending`, `deleted`, `failed`, `blocked`, `skipped`, `deferred` | Media counts by durable state. `confirmed` means a verified node at the configured target parent; `existing` means a verified copy already exists elsewhere in the same Amazon account (another folder or only inside an album) and was reused instead of uploading; `deletion_pending` means Immich reports the asset trashed and the mapped node awaits the sweep; `deleted` means the mapped node was moved to the Amazon trash; `skipped` is not a cloud backup; `deferred` means media whose new upload waits for the transfer window, including unattempted work while the window is closed. Deferred is not a failure, error or uncertain write. |
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
| `work_remaining` | Unpaused work other than window-blocked uploads is eligible for continuation now. False does not mean every job succeeded. |
| `next_attempt` | Optional earliest scheduled wake timestamp: the earliest retry or, when uploads are deferred, the next transfer-window opening. Omitted when none is scheduled or the account is paused. |
| `deferral_reason` | `transfer_window` while deferred uploads wait for a closed window; omitted otherwise. |
| `transfer_window` | Present only when the account configures a window: `timezone`, `start`, `end`, `open`, and `next_opening` when closed. `next_opening` is DST-correct local wall-clock time in that zone. |
| `bytes_per_second` | Configured combined per-account cap for source-download and destination-upload bytes; zero means disabled. Metadata traffic is not counted. |

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

A configured transfer window defers only new upload dispatch. Outside the
window, scans, metadata and existing-content reads, album reconciliation and
deletion passes still run; staged originals and `unconfirmed` writes keep their
normal semantics. An upload that started before closing finishes and is judged
only by its remote evidence. Deferred uploads are not errors, so `work_remaining`
is false when a closed window is the only reason work cannot run, while
`next_attempt` carries the next opening and the scheduler wakes for it. A window
edit applies to the next dispatch without a restart and without cancelling
running streams; `unconfirmed` reconciliation still never performs a new POST.
