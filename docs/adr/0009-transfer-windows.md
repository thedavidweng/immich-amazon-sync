# ADR 0008: Transfer windows gate only new uploads

## Status

Accepted.

## Context

Some deployments share a metered or busy uplink. Users want uploads during a
chosen local-time window, sometimes across midnight, with predictable behaviour
across daylight-saving transitions. A window must not weaken the durable
`unconfirmed` contract or turn a healthy in-flight upload into an uncertain
write. Status, CLI and the local dashboard must explain why work is waiting and
when it will run.

## Decision

- Config carries one optional `transfer_window` per account: an IANA timezone
  and `start`/`end` local wall-clock `HH:MM` bounds. Absent means continuous
  transfers. `start` is inclusive, `end` exclusive, and `start > end` crosses
  midnight. Equal bounds, `Local`, unknown zones and malformed clock values are
  configuration errors.
- The IANA database is embedded with `time/tzdata` so a minimal container works
  without a system tzdata package. Openness is evaluated against the local wall
  clock. The next opening is found by stepping real instants, so a wall time
  skipped by spring-forward never opens and a repeated fall-back wall time opens
  in both occurrences.
- The gate sits immediately before the first remote upload POST (and before
  staging a not-yet-staged original). Metadata reads, existing-content and
  favorite drift reads, album reconciliation and deletion passes are not gated.
- A gated job is persisted as the durable state `deferred`, with attempts, error
  code, content evidence and any staged original unchanged. Deferred is neither
  failure nor uncertainty, and it consumes no retry. Reaching the window is
  derived from the live window, so an edited window applies to the next dispatch.
- The engine and service share one concurrency-safe window holder per account.
  Reload updates it in place and cancels a running cycle only when account
  settings other than the window changed.
- Status exposes `deferred`, `deferral_reason`, and a `transfer_window` object
  with timezone, bounds, open state and the next opening. `next_attempt` carries
  the earliest retry or window opening; the scheduler wakes from it. A closed
  window alone does not report runnable work.
- `bytes_per_second` remains a single per-account limiter for combined
  source-download and destination-upload bytes. Documentation and the dashboard
  state that scope, so the cap is never described as upload-only.

## Consequences

- Deferral cannot create duplicates or lose uncertainty because it never writes
  to the target.
- A long backlog can show `deferred` counts and a blocked `last_success` until
  the window opens; that is honest, not an error, and retries are untouched.
- Two accounts may use different windows while sharing the store, staging
  budgets and worker pool.
- Window changes do not require a restart; credential, path and listener changes
  keep their existing rules.
- Tests inject a clock and exercise daytime, cross-midnight, spring-gap,
  fall-repeat, closing-midway, reload and two-account independence through the
  engine seam.

## Alternatives considered

- Gating every remote call: rejected because it would block safe metadata,
  album and deletion work and make deferral look like account failure.
- A persisted "deferred until" timestamp independent of the live window:
  rejected because an edited window would strand work until the old deadline.
- Blocking or polling at `scan_interval`: rejected because the next opening is
  computed exactly and the scheduler already wakes for due work.
