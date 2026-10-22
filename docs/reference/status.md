# Durable progress and status

`sync once --json` returns an `accounts` array. Each entry contains an anonymous
`account` identifier, a finite `code` and a `status` object. Scheduled service
cycles log aggregate status; the optional authenticated dashboard presents an
overview. There is no public JSON status endpoint.

| Status field | Meaning |
| --- | --- |
| `pending`, `staged`, `running`, `unconfirmed`, `confirmed`, `existing`, `deletion_pending`, `deleted`, `failed`, `blocked`, `skipped`, `deferred` | Media counts by durable state. `confirmed` means a verified node at the configured target parent; `existing` means a verified copy already exists elsewhere in the same Amazon account (another folder or only inside an album) and was reused instead of uploading; `deletion_pending` means Immich reports the asset trashed and the mapped node awaits the sweep; `deleted` means the mapped node was moved to the Amazon trash; `skipped` is not a cloud backup; `deferred` means media whose new upload waits for the transfer window. Deferred is not a failure or error, and no retry is consumed. |
| `exclusions` | Durable skip counts by finite reason, including eligibility filters (`media_type_excluded`, `raw_excluded`, `size_excluded`, `size_unknown`, `archived_excluded`, `tag_excluded`, `tag_provenance_unavailable`) and source conditions (`hidden`, `locked`, `offline`, `trashed`, `unsupported_type`, `source_excluded`, `live_photo_companion`). A policy change requeues filter-managed exclusions; a refreshed hint or reconciliation reevaluates them without a full rescan. |
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
| `favorites_mode` | Effective per-account favorite setting: `off` (default) or `add_only`. |
| `favorites_desired` | Nodes whose source asset explicitly reports favorite, including ones already applied. |
| `favorites_pending` | Desired nodes awaiting a verified star. |
| `favorites_applied` | Nodes whose star was confirmed by an authoritative Amazon re-read. |
| `favorites_failed` | Favorite operations with a definite finite error awaiting their own retry. |
| `favorites_uncertain` | A favorite mutation or read whose committed state is unproven (`favorite_unconfirmed`). Retried only after a fresh read. |
| `favorites_unknown` | Nodes whose source omitted the favorite field; no write is issued and the state is not treated as false. |
| `live_photo_mode` | Effective Live Photo policy: `still_only` (default) or `original_pair`. `still_only` makes no pair claim and always reports zero pair counters. |
| `pairs_complete` | Live Photo stills whose still and hidden MOV companion both have a verified Node (`confirmed`/`existing`). |
| `pairs_incomplete` | Pairs whose companion is missing, trashed, offline or excluded by policy. Reported independently of media state and also counted in `errors` as `pair_incomplete`; it means the Live Photo pair is not fully backed up, not that media failed. |
| `workflow` | Managed Immich Workflow state: `active`, `polling`, `creating`, `unconfirmed`, `failed`, `cleanup_failed`, `removed` or `disabled`; omitted when never requested. |
| `workflow_code` | Optional finite notice or failure for the workflow state, such as `workflow_version_unsupported`, `workflow_permission_missing`, `workflow_unconfirmed` or `workflow_cleanup_failed`. Omitted when healthy. |
| `incremental_scanning` | A library delta scan is active. |
| `last_incremental` | Completed library discovery watermark, or `null`; this is the scan-start boundary, not proof of transfer completion. |
| `work_remaining` | Unpaused work other than window-blocked uploads is eligible for continuation now. False does not mean every job succeeded. |
| `next_attempt` | Optional earliest scheduled wake timestamp: the earliest retry or, when uploads are deferred, the next transfer-window opening. Omitted when none is scheduled or the account is paused. |
| `deferral_reason` | `transfer_window` while deferred uploads wait for a closed window; omitted otherwise. |
| `transfer_window` | Present only when the account configures a window: `timezone`, `start`, `end`, `open`, and `next_opening` when closed. `next_opening` is DST-correct local wall-clock time in that zone. |
| `bytes_per_second` | Configured combined per-account cap for source-download and destination-upload bytes; zero means disabled. Metadata traffic is not counted. |
| `direct` | Process-local read-only mount diagnostics: `reads`, `bytes_avoided`, per-reason `fallbacks` and fail-closed `failures`. Counters reset on restart and never contain a path. Zero everywhere when no `direct_roots` mapping is configured. |
| `capture_date` | Capture-date write capability of the pinned target SDK. `metadata_not_supported` means the service cannot set a capture date remotely and does not claim that Immich-only date edits are mirrored; `supported` is reserved for a released SDK operation verified by an authoritative read-back. The field is always present and is never an implied success. |

A Live Photo pair is tracked separately from its two original media rows. In
`original_pair` mode the still and the companion each upload their own bytes and
own Node; a pair is complete only when both Nodes are verified. Enabling or
disabling the mode never re-uploads, overwrites or trashes a Node, and pair
bookkeeping (`live_pairs`) is derived state, not deletion evidence. Unrelated
hidden media keeps its `hidden` exclusion, and no derived or muxed media is ever
produced.

A `polling` workflow state is a capability notice, not a media failure: periodic
reconciliation is never disabled. Counts such as pending, failed and unconfirmed
album work overlap; do not add them to infer a total. Favorite counters are
separate from media states: a `confirmed` or `existing` node can also be
`favorites_applied`, and favorite retries never change `confirmed`, `existing`
or `unconfirmed` media counts. Disabled favorite sync performs no write and
withdraws unsettled intents while keeping an already-applied star. Disabling
album mirroring reports zero album/association counts without deleting preserved
state. No private asset IDs, filenames, paths,
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

A configured transfer window defers only new upload dispatch. Outside the
window, scans, metadata and existing-content reads, album reconciliation and
deletion passes still run; staged originals and `unconfirmed` writes keep their
normal semantics. An upload that started before closing finishes and is judged
only by its remote evidence. Deferred uploads are not errors, so `work_remaining`
is false when a closed window is the only reason work cannot run, while
`next_attempt` carries the next opening and the scheduler wakes for it. A window
edit applies to the next dispatch without a restart and without cancelling
running streams; `unconfirmed` reconciliation still never performs a new POST.
