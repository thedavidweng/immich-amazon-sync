# ADR 0013: Optional metrics, one status report, and bounded retry tuning

Status: accepted (2026-10-22)

## Context

Operators asked for low-cardinality monitoring and per-account retry tuning
without widening the service's private surface. The service already exposes
`/healthz`, `/readyz`, structured logs and an authenticated dashboard, and the
durable ledger is the only state store. Several tickets added counters
(favorites, albums, memberships, usage, Live Photo pairs, windows, direct
reads); a second status computation would drift from them.

## Decision

### One durable report

`Service.StatusReport` reads only the local ledger and never constructs remote
calls. The `status` CLI command, `sync once --json`, the authenticated dashboard,
`GET /status` and `GET /metrics` all consume it. A per-account failure is
reported as a finite `code` with zero counters, never as fabricated values. No
surface may query the database independently for these counts.

### Metrics are optional and private by construction

`metrics: true` registers `GET /metrics` and `GET /status` on the normal
listener; without it both answer `404`. A loopback listener is acceptable on its
own. Any other bind requires `metrics_token_file`, checked with a constant-time
comparison on every request so rotation needs no restart. An unreadable token
file fails closed with `503 metrics_unavailable`. Health and readiness stay
independent of Amazon availability.

Labels are the hashed account plus finite `state`, `code`, `reason`, `category`,
`kind` and `mode` vocabularies. Any other label value collapses to `other`, so a
durable string can never grow cardinality or leak private data. Byte and
direct-read counters are process-local; durable counts come from the ledger.

### Retry tuning narrows, never widens, the safety rules

Each account may set `retry.max_attempts` (1..10) and `retry.max_delay`
(1s..5m). Defaults remain the conservative 5 attempts and 30s ceiling. A remote
`Retry-After` still wins over the local ceiling. The attempt cap applies to
durable retry scheduling for definite failures and reconciliation reads; it
never turns an `unconfirmed` write into a fresh POST. `Unconfirmed` always
re-enters read-before-write reconciliation, and the engine's state machine
enforces that independently of these settings. Configuration validation and
`syncer.New` both reject out-of-range values so no surface can widen the cap.

### Workflow logs are aggregate and authorized

The owned Workflow exposes only per-run `completed`, `halted`, `error` counts and
the latest run time, through the separately authorized Workflow credential.
The CLI already offered this; the authenticated dashboard now offers the same
read-only view. Workflow IDs, callback URLs, bearer secrets and event payloads
are never rendered.

## Consequences

- Adding a status counter means adding it to `syncer.Status` and its renderer;
  the CLI, dashboard, HTTP status and metrics surfaces update together.
- Metrics cannot be enabled accidentally on a public interface, and enabling
  them is an explicit configuration change.
- Retry tuning is bounded by code, not by operator discipline.
- Cardinality stays a function of the finite vocabulary, not of account data.
