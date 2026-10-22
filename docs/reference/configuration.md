# Configuration

Pass a JSON file with `--config PATH`. Unknown fields and trailing JSON values
are errors. Credential file paths are absolute in the container example; relative
paths resolve from the process working directory, not from the config's directory.
See [config.example.json](config.example.json) for the minimal deployment shape.

| Top-level field | Default | Meaning |
| --- | --- | --- |
| `data_dir` | `data` | SQLite, lock and bounded staging; container overrides to `/data`. |
| `listen` | `127.0.0.1:8080` | HTTP listen address; container overrides to `0.0.0.0:8080`. |
| `scan_interval` | `15m` | Periodic reconciliation interval, at least one second; not a cycle deadline or backfill delay. |
| `workers` | `2` | Configured worker limit, 1..16; no throughput guarantee. |
| `max_staging_bytes` | `10737418240` | Positive total staging budget, 10 GiB. |
| `max_staging_files` | `2` | Positive staging file count limit. |
| `bytes_per_second` | `0` | Nonnegative configured transfer limit; zero disables the cap. |
| `log_level` | `info` | `debug`, `info`, `warn`, `error`. |
| `admin_password_file` | unset | Optional mounted admin passphrase file, at least 12 characters; enables the local UI. |
| `webhook_secret_file` | unset | Optional bearer-secret file for `/events/{account}`; trimmed secret at least 32 bytes, raw file at most 4 KiB. |
| `accounts` | empty | Explicit, isolated account configurations. |

| Per-account field | Default | Meaning |
| --- | --- | --- |
| `id` | required | Stable local key: 1..128 letters/digits/underscore/hyphen, starting with a letter/digit. |
| `immich_url` | required | Trusted HTTP(S) base URL, optionally including `/api`; no embedded credentials/query/fragment. |
| `immich_api_key_file` | required | File holding the source API key. |
| `amazon_region` | required | Actual marketplace, validated against the SDK; no implicit US choice. |
| `amazon_cookie_file` | required | File holding a Cookie header value. |
| `album_ids` | empty | Explicit source album UUID allowlist. |
| `library` | `false` | Deliberate whole-accessible-library choice, mutually exclusive with `album_ids`. |
| `include_videos` | unset | Legacy video toggle accepted only for migration into `filters.media_types`; supplying both returns `filter_conflict`. |
| `filters` | images only, archive included | Validated eligibility policy; see [eligibility filters](#eligibility-filters). |
| `album_sync_mode` | `off` | Album mirroring scope: `off`, `selected` (requires `album_ids` and `library: false`) or `all_accessible` (discovers every album the API key can read, including with `library: true`). |
| `mirror_albums` | unset | Deprecated boolean form of `album_sync_mode`. `true` maps to `selected` and never to `all_accessible`; combining it with a different mode returns `album_sync_mode_conflict`. |
| `favorites` | `off` | `off` or `add_only`. Add-only stars a verified Amazon Node when the source explicitly reports `isFavorite`; it never unstars. An absent source field is unknown and performs no write. |
| `live_photo_mode` | `still_only` | `still_only` preserves the historical behaviour: a hidden Live Photo MOV is excluded as `hidden`. `original_pair` follows Immich's `livePhotoVideoId` link and backs up the still and its hidden MOV as two independent originals, tracked as one pair. No mux, conversion or Apple pairing is performed. |
| `deletions` | `false` | Propagate an Immich trash to the Amazon trash for mapped assets. Off by default; never permanently deletes. |
| `deletions_confirmation` | unset | Must be exactly `ENABLE DELETIONS` while `deletions` is true; a missing or different phrase rejects the config with `deletion_confirmation_required`. |
| `managed_workflow` | `false` | Opt-in to creating one owned native Immich Workflow. Requires all three workflow fields below. Polling stays enabled; nothing is created on Immich v2 or without the required grants. |
| `immich_workflow_api_key_file` | unset | Separate mounted Immich API key with `workflow.read`, `workflow.create`, `workflow.update` and `workflow.delete`; must differ from `immich_api_key_file`. `workflow.logs` additionally enables diagnostics. |
| `workflow_callback_url` | unset | HTTP(S) URL reachable from Immich that ends in `/events/ACCOUNT_ID`; no embedded credentials, query or fragment (`workflow_callback_url_invalid`). |
| `workflow_secret_file` | unset | Path for the generated bearer secret. The service creates the file with mode `0600`; the value is never stored in JSON or the ledger. |
| `transfer_window` | unset | Optional daily upload window: `{"timezone":"America/New_York","start":"23:00","end":"06:00"}`. `timezone` is an IANA name; `start`/`end` are local `HH:MM` wall-clock times. `start` is inclusive, `end` is exclusive, and `start` may be later than `end` to cross midnight. Equal bounds are invalid, as are unknown or `Local` zones. Unset (or `null`) preserves continuous transfers. |
| `direct_roots` | empty | Optional read-only mappings from an Immich-reported path prefix to a local directory. Each entry has `immich_path` and `host_path`; both must be absolute and clean, and `immich_path` must be unique. Empty means every original is downloaded from Immich. |

### Live Photo originals

Immich links an Apple Live Photo still to its hidden MOV companion through
`livePhotoVideoId`. `live_photo_mode: still_only` (the default) never follows
that link, so the hidden MOV keeps its `hidden` exclusion. `original_pair`
resolves the link and uploads both originals separately: each is staged from its
own source bytes, gets its own MD5 and size, and becomes its own Amazon Node.

Pair membership is durable and separate from media state. A pair is `complete`
only when both the still and the companion have a verified Node
(`confirmed`/`existing`); a missing, trashed, offline or filtered companion
makes it `pair_incomplete`, which is reported in status and `errors` as
`pair_incomplete`. The service never claims a complete Live Photo backup while
the pair is incomplete.

Only a companion referenced by an included still becomes eligible. Unrelated
hidden media stays excluded, the standalone-video `filters.media_types` rule
never excludes a resolved companion, and every other filter rule (size, tags,
archive, RAW) still applies. Nothing is muxed or converted into a Google or
Samsung Motion Photo, no derived hash replaces a source hash, and no Apple
pairing or playback is written to Amazon. Amazon's published format support is
separate from unproven SDK linking and playback, which stays untested until
disposable-sample evidence exists.

Switching modes never re-uploads, overwrites or trashes a Node. Enabling
`original_pair` adopts stills already in the ledger from their stored metadata;
already confirmed stills are not downloaded again. Disabling it withdraws pair
bookkeeping and settles any unsettled companion as `live_photo_companion`,
leaving every uploaded Node intact.

Deletion propagation keeps its existing meaning. With `deletions: false`, a
source-trashed companion changes nothing and an already complete pair stays
complete. With `deletions: true`, a trashed companion is moved to the Amazon
trash by the normal guarded pass and the pair is retired instead of reported
incomplete, because the source no longer holds that pair.

### Eligibility filters

Each account has one validated policy. A newly discovered Asset that the policy
rejects is recorded as `skipped` with a finite exclusion reason and never
downloaded or uploaded. Exclusions are reevaluated when a hint or reconciliation
refreshes metadata or tags, and previously filtered Assets are requeued when the
policy fingerprint changes. No full rescan is required.

| Field | Default | Meaning |
| --- | --- | --- |
| `filters.media_types` | `["image"]` | Included media types; one or both of `image`, `video`, no duplicates. |
| `filters.max_original_bytes` | `0` | Maximum original byte size; `0` means unlimited. When set, an unknown source size (`-1`) is excluded as `size_unknown`, never treated as zero. |
| `filters.skip_raw` | `false` | Skip camera RAW images by lowercased original filename extension. |
| `filters.include_archived` | `true` | Archived originals stay eligible by default, preserving pre-filter behavior. An explicit `false` excludes them. |
| `filters.excluded_tags` | `[]` | Stable Immich tag UUIDs. An Asset carrying any listed tag is excluded. If the source omits tag provenance, affected Assets are excluded as `tag_provenance_unavailable` rather than assumed untagged. |

Finite exclusion reasons are `media_type_excluded`, `raw_excluded`,
`size_excluded`, `size_unknown`, `archived_excluded`, `tag_excluded` and
`tag_provenance_unavailable`. They appear in `status.exclusions` and the
dashboard. Source conditions (`hidden`, `locked`, `offline`, `trashed`,
`unsupported_type`, checksum provenance) remain separate reasons and take
precedence over policy rules.

Filters never delete media, never remove Membership and never authorize trash or
an Amazon `MoveToTrash` call. A confirmed or Existing mapping survives a policy
change; filtering only decides whether unsettled media may be uploaded. Absence
from an eligible or filtered view is not deletion evidence.

When `filters.skip_raw` is true, the documented extension list is:
`.3fr`, `.ari`, `.arw`, `.bay`, `.cr2`, `.cr3`, `.crw`, `.dcr`, `.dng`, `.erf`,
`.fff`, `.gpr`, `.iiq`, `.k25`, `.kdc`, `.mef`, `.mos`, `.mrw`, `.nef`, `.nrw`,
`.orf`, `.pef`, `.raf`, `.raw`, `.rw2`, `.rwl`, `.sr2`, `.srf`, `.srw`, `.x3f`.

The legacy `include_videos` toggle migrates with identical scope: absent or
`false` becomes `["image"]`, `true` becomes `["image","video"]`, and archive
remains included. Supplying both `include_videos` and `filters` is rejected with
`filter_conflict`; saving rewrites the legacy field into `filters`. Invalid
media types, a negative size limit, non-UUID or duplicate excluded tags return
`filter_media_type_invalid`, `filter_max_original_bytes_invalid` or
`filter_excluded_tags_invalid`.

At least one album or explicit library mode is required for each account.
Account IDs must be unique. Zero accounts is only valid when local management
bootstrap is explicitly enabled with an admin credential. `managed_workflow:
true` without a separate workflow key, callback URL or secret path returns
`workflow_configuration_required`; reusing the polling key path returns
`workflow_credentials_conflict`. Disabling management may keep the mounted
paths so best-effort cleanup can still run. See
[managed Workflow onboarding](../how-to/managed-workflows.md). Secret files must be
nonempty and at most 64 KiB for remote credentials; the JSON config limit is
1 MiB on both load and save, including the saved JSON newline. Oversize input
returns `config_invalid_json`; a rejected save leaves the previous file and
running configuration intact. The event handler applies the stricter
webhook-secret limit above.
`album_sync_mode: selected` requires `album_ids` and `library: false`.
`album_sync_mode: all_accessible` maps each readable source album, by stable
UUID, to its own owned destination album. It may be combined with
`library: true` so the whole accessible library is backed up and every
accessible album is mirrored. For whole-accessible-library backup without album
mirroring, explicitly set `library: true`, `album_ids: []` and
`album_sync_mode: off`. This does not bypass source permissions.

Existing `mirror_albums: true` configurations keep exactly their selected album
scope; the legacy boolean never enables `all_accessible`. Unsetting it leaves
album mirroring off. An unknown mode returns `album_sync_mode_invalid`.

`direct_roots` is opt-in and has no default mapping. Only regular files under an
approved root are read; a missing path, an absent mount or a permission error
falls back to the Immich API, while an unclean path, an escaping symlink or a
non-regular file fails closed with a finite `direct_path_*` code. The mount is
never written or retimed. Invalid entries reject the config with
`direct_root_invalid`. See
[direct originals](../how-to/direct-originals.md) for the Compose overlay,
safety rules and diagnostics.

`transfer_window` gates only new outbound media uploads. Metadata reads, favorite
drift reads, album reconciliation, deletion passes and existing-content detection
continue outside the window. An upload already streaming finishes normally; a
closing window never marks it failed or unconfirmed. Work that cannot start is
persisted as `deferred` (status also counts not-yet-attempted upload work as
deferred while the window is closed), never as a failure, blocked or uncertain
state, and it does not consume a retry. The scheduler computes the next DST-correct opening in
the configured zone (a wall time skipped by a spring-forward transition simply
does not open; a repeated fall-back wall time opens in both occurrences) and
wakes for it. Editing the window applies to the next dispatch without restarting
the service or cancelling streams. Invalid values return
`transfer_window_timezone_invalid`, `transfer_window_time_invalid` or
`transfer_window_bounds_invalid`. The IANA database is embedded in the binary, so
a minimal container without system tzdata still resolves zones.

`bytes_per_second` caps combined per-account byte flow: source original downloads
(staging reads, including fallback Immich downloads) and destination uploads are
charged together against the same limit. Each byte is counted once for the
purpose it serves; a single asset is not counted twice for the same purpose.
Metadata requests are not counted. Zero disables the cap.

Events do not change source scope. See [event setup](../how-to/event-hints.md),
[managed albums](../how-to/managed-albums.md) and
[progress scheduling](status.md) for polling, backfill and membership policy.

The admin credential JSON key is exactly `admin_password_file`; its
environment counterpart is `ADMIN_PASSWORD_FILE`. Store a file path in JSON,
never the credential value. Without this setting the management routes are
absent; health/readiness remain public. Zero-account `serve` with this secret is
supported, but a missing config file still needs explicit `--setup`.

UI editing requires an unfiltered explicit config path and a writable parent
directory. Saves validate the entire config and atomically replace it with mode
`0600` before reloading. Every account must use mounted credential file paths;
environment-value-only credentials cannot be serialized through the UI. Failed
validation or persistence does not reload. Changes to `data_dir`, `listen`, the
admin-secret path or webhook-secret path require restart. See
[local management](../how-to/local-management.md) for deployment modes.

Without `--config`, the single-account environment mode uses `IMMICH_URL`,
`IMMICH_API_KEY_FILE`, `AMAZON_REGION`, `AMAZON_COOKIE_FILE`, comma-separated
`ALBUM_IDS`, `LIBRARY` (`true`/`false`), `ALBUM_SYNC_MODE` (`off`, `selected`,
`all_accessible`) and `FAVORITES` (`off`/`add_only`). Filter variables are
`MEDIA_TYPES`, `MAX_ORIGINAL_BYTES`, `SKIP_RAW`, `INCLUDE_ARCHIVED` and
comma-separated `EXCLUDED_TAGS`. The legacy `INCLUDE_VIDEOS` (`true`/`false`) is
accepted instead of `MEDIA_TYPES`; supplying both returns `filter_conflict`.
Optional environment-mode window values are `TRANSFER_TIMEZONE`,
`TRANSFER_WINDOW_START` and `TRANSFER_WINDOW_END`; set all three together or
none. Its account ID is `default`. Use JSON for multiple accounts.
`DATA_DIR`, `LISTEN`, `ADMIN_PASSWORD_FILE` and `WEBHOOK_SECRET_FILE` override
their corresponding values even with a JSON file. Environment-only accounts also
accept `MANAGED_WORKFLOW`, `IMMICH_WORKFLOW_API_KEY_FILE`,
`WORKFLOW_CALLBACK_URL` and `WORKFLOW_SECRET_FILE`.

Direct `IMMICH_API_KEY` and `AMAZON_COOKIE` values exist for environment-mode
compatibility. Avoid them in deployments: environment values can appear in
container inspection and process tooling. Supplying both a direct value and
its file path is an error.

The image sets `DATA_DIR=/data` and `LISTEN=0.0.0.0:8080`; these override JSON.
Keep the Compose host binding `127.0.0.1:8080:8080` unless you deliberately
configure protected remote access. Changing JSON alone cannot override an
existing environment override.
