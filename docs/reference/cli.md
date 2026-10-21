# CLI and probes

```text
immich-amazon-sync [--config PATH] [--account ID] [--json] COMMAND
```

| Command | Behavior |
| --- | --- |
| `serve` | Run the local service and scheduled backups; stop with SIGTERM. |
| `sync once` | Perform a controlled discovery/transfer pass; this can write media and managed membership. |
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

| HTTP route | Meaning |
| --- | --- |
| `GET /healthz` | Process is running. |
| `GET /readyz` | Core local services can work. Remote credential/network failures belong in diagnostics. |

Probes do not expose credentials or prove successful cloud transfer.
The Docker healthcheck uses BusyBox `wget` against both fixed loopback endpoints,
with no secrets or external network dependency. The image listens on
`0.0.0.0:8080`; native config defaults to `127.0.0.1:8080`. Compose publishes
host loopback only.
