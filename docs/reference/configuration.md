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
| `include_videos` | `false` | Include eligible standalone originals classified as video. |
| `mirror_albums` | `false` | Mirror explicitly selected albums; requires `album_ids` and `library: false`. |
| `deletions` | `false` | Propagate an Immich trash to the Amazon trash for mapped assets. Off by default; never permanently deletes. |
| `deletions_confirmation` | unset | Must be exactly `ENABLE DELETIONS` while `deletions` is true; a missing or different phrase rejects the config with `deletion_confirmation_required`. |
| `transfer_window` | unset | Optional daily upload window: `{"timezone":"America/New_York","start":"23:00","end":"06:00"}`. `timezone` is an IANA name; `start`/`end` are local `HH:MM` wall-clock times. `start` is inclusive, `end` is exclusive, and `start` may be later than `end` to cross midnight. Equal bounds are invalid, as are unknown or `Local` zones. Unset (or `null`) preserves continuous transfers. |

At least one album or explicit library mode is required for each account.
Account IDs must be unique. Zero accounts is only valid when local management
bootstrap is explicitly enabled with an admin credential. Secret files must be
nonempty and at most 64 KiB for remote credentials; the JSON config limit is
1 MiB on both load and save, including the saved JSON newline. Oversize input
returns `config_invalid_json`; a rejected save leaves the previous file and
running configuration intact. The event handler applies the stricter
webhook-secret limit above.
Library mode with `mirror_albums: true` returns `source_scope_conflict`; automatic
discovery/mirroring of all visible albums is not implemented.
For whole-accessible-library backup, explicitly set `library: true`,
`album_ids: []` and `mirror_albums: false`. This does not bypass source permissions.

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
`ALBUM_IDS`, and `INCLUDE_VIDEOS` (`true`/`false`). Its account ID is `default`.
Optional environment-mode window values are `TRANSFER_TIMEZONE`,
`TRANSFER_WINDOW_START` and `TRANSFER_WINDOW_END`; set all three together or none.
Use JSON for multiple accounts and explicit library/album-mirroring options.
`DATA_DIR`, `LISTEN`, `ADMIN_PASSWORD_FILE` and `WEBHOOK_SECRET_FILE` override
their corresponding values even with a JSON file.

Direct `IMMICH_API_KEY` and `AMAZON_COOKIE` values exist for environment-mode
compatibility. Avoid them in deployments: environment values can appear in
container inspection and process tooling. Supplying both a direct value and
its file path is an error.

The image sets `DATA_DIR=/data` and `LISTEN=0.0.0.0:8080`; these override JSON.
Keep the Compose host binding `127.0.0.1:8080:8080` unless you deliberately
configure protected remote access. Changing JSON alone cannot override an
existing environment override.
