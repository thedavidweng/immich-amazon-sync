# CLI and probes

```text
immich-amazon-sync [--config PATH] [--account ID] [--json] COMMAND
```

| Command | Behavior |
| --- | --- |
| `serve` | Run the local service and scheduled backups; stop with SIGTERM. |
| `serve --setup --config PATH` | Explicit first UI setup with a mounted admin secret and a new writable config path. |
| `sync once` | Start/resume a bounded explicit full-scan pass; can write media, managed membership and -- only when explicitly enabled -- Amazon trashing. |
| `deletions plan` | Read-only manifest of assets pending deletion propagation: count, synced total, blocked reason and asset IDs. Changes nothing. |
| `albums list` | Read accessible source album IDs, names and counts. |
| `doctor` | Read-only remote diagnostics and local storage checks; no upload or album creation. |
| `version` | Print the build version without credentials or config. |
| `--help`, `doctor --help`, `--version` | Work without configuration or network access. |

Use `--account ID` to choose an explicitly configured account. Without it,
account-aware operations process their configured scope. Use `--json` for
machine-readable results and finite diagnostic error codes. Do not parse human
help text as an API or infer success from an HTTP response alone.

| Exit | Meaning |
| --- | --- |
| `0` | Success |
| `1` | Operation failure |
| `2` | Usage or configuration error |
| `3` | Credential or permission error |
| `4` | Local storage failure |
| `130` | Cancellation |

The command's `--help` is authoritative for additional management operations.
Every process accessing a state volume must respect the single-writer boundary.
Stop `serve` before running a separate stateful CLI invocation.

Human-readable account lines report `confirmed`, `existing`, `deleted`,
`deletion_pending`, `pending`, `failed` and `unconfirmed` media counts. `existing` means a verified copy was reused from
elsewhere in the same Amazon account; it is backed up but not at the configured
target parent. `--json` carries the full status object.

For each selected account, one `sync once` invocation attempts at most 64 distinct
media jobs and advances full discovery by at most eight page steps; an already
active library delta can also advance. Cursors and remaining work persist.
Exit 0 means that bounded pass succeeded, not that all historical media is backed
up. Inspect `scanning`,
`work_remaining`, retry deadlines and `last_success` in the
[JSON status](status.md). Use `serve` for automatic continuation without waiting
for the next periodic scan; the bounds are work limits, not a wall-clock timeout.

`--setup` is never inferred from a missing file. It rejects an existing config
(including a symlink), missing `--config`, `--account`, missing admin secret or
accounts supplied through `IMMICH_URL`. The parent directory must already exist
and be writable. No config is created until the first valid account is saved.
Then restart with `serve --config PATH`, without `--setup`. Normal
`serve --config /missing` remains `config_unreadable`, exit 2.

With an admin secret, `serve` also supports zero accounts without an explicit
config path. Its dashboard is read-only for configuration, as is
`serve --account ID`; account controls remain available. An unfiltered explicit
config path enables saving only when its directory is writable. The stock
Compose config bind is read-only. See [local management](../how-to/local-management.md).

| HTTP route | Meaning |
| --- | --- |
| `GET /healthz` | Process is running. |
| `GET /readyz` | Core local services can work. Remote credential/network failures belong in diagnostics. |
| `POST /events/{account}` | Independently bearer-authenticated durable asset hints; see [event HTTP reference](events.md). |

Probes do not expose credentials or prove successful cloud transfer.
The Docker healthcheck uses BusyBox `wget` against both fixed loopback endpoints,
with no secrets or external network dependency. The image listens on
`0.0.0.0:8080`; native config defaults to `127.0.0.1:8080`. Compose publishes
host loopback only.

Optional HTML routes and form fields are documented in
[management HTTP reference](management.md); they are not a JSON management API.
Event HTTP statuses are distinct from CLI exits: `invalid_event` maps to exit 2,
`webhook_unauthorized` to 3, and `album_unconfirmed`, `membership_guard` and
`webhook_unavailable` to 1 when reported through CLI diagnostics.
