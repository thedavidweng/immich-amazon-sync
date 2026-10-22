# Local management HTTP contract

Management is optional HTML forms, not a JSON management API. Routes exist only
when an admin passphrase file is configured. They share the normal listener;
`GET /healthz` and `GET /readyz` remain public independently.

| Route | Form/behavior |
| --- | --- |
| `GET /login` | Issue or reuse pre-auth cookie and hidden `csrf` token; authenticated users redirect home. |
| `POST /login` | `csrf`, `password`; rotate session and CSRF on success. |
| `POST /logout` | `csrf`; revoke session. |
| `GET /` | Authenticated overview, aggregate durable counts and account controls. |
| `GET /accounts/new` | Source and Destination editor for a new account. |
| `GET /accounts/{id}/edit` | Existing account editor. |
| `POST /accounts/discover` | `csrf`, source URL/key file and editor fields; source-only album lookup without save or scope selection. |
| `POST /accounts/save` | `csrf`, `original_id`, `id`, `immich_url`, `key_file`, `region`, `cookie_file`, `album_ids`, repeated `album_id`, `media_image`, `media_video`, `max_original_bytes`, `skip_raw`, `include_archived`, `excluded_tags`, optional `library`, `album_sync_mode`, `favorites`, `favorites_takeover` (only with mirror), `deletions`, `deletions_confirmation`, `video_usage_threshold_bytes`, `hold_videos_at_threshold`, the optional transfer-window fields `transfer_timezone`, `transfer_window_start`, `transfer_window_end`, and the managed-workflow fields `managed_workflow`, `workflow_key_file`, `workflow_callback_url`, `workflow_secret_file`. |
| `POST /accounts/{id}/pause`, `/resume`, `/retry`, `/doctor` | `csrf`; account control or read-only diagnostics, not a cloud-write probe. |
| `POST /accounts/{id}/remove` | `csrf`; configuration removal, not local-history or cloud-media deletion. |

`original_id` is empty for a new account. `album_ids` accepts comma/whitespace
separation; repeated `album_id` values come from discovery checkboxes. Boolean
checkboxes are `on` or absent. Scope must be explicit: album IDs or whole library.
All credentials are mounted file paths, never form secret values.
`album_sync_mode` is `off`, `selected` (explicit album IDs, library off) or
`all_accessible` (every readable album; allowed with whole-library backup).
Disabling mirroring stops managed writes and preserves cloud media, album
mappings and memberships.
Transfer-window fields are optional; leave all three empty for continuous
transfers. Saving a window change applies to the next dispatch without restarting
the service and without cancelling in-flight streams.
The overview's durable progress fields are described in the
[status reference](status.md), including separate media, album and association
completion, and the managed Workflow state with its finite notice. Orphaned
workflow cleanup for removed accounts is listed read-only. `album_sync_mode` is
`off`, `selected` (explicit album IDs, library off) or `all_accessible` (every
readable album; allowed with whole-library backup). Disabling mirroring stops
managed writes and preserves cloud media, album mappings and memberships.
Managed workflow fields store mounted paths only; the generated secret is never
rendered. It also shows observed photo/video/other usage, its `state` and
calculation time, an explicit `subscription capacity: unknown` and
`Prime entitlement: unknown`, plus the configured video threshold and any
video/quota hold. Doctor shows the same usage observation and unknowns.

Login without a valid pre-auth cookie/token and authenticated mutations without
CSRF are rejected. See [local management](../how-to/local-management.md) for
session lifetimes, security limits, writable-mode requirements and first setup.
