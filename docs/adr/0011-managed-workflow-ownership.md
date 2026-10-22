# ADR 0011: Managed Immich Workflow ownership

## Status

Accepted.

## Context

Immich v3 introduced native Workflows. An optional service-managed Workflow can
reduce event latency, but it requires a write capability against the same
server that holds the source originals. Event delivery is not durable and does
not replace reconciliation. A create can also time out after committing.

## Decision

- Managed onboarding is opt-in per account (`managed_workflow`), off by default,
  and uses a **separate** Immich credential file that must differ from the
  read-only polling key. The ordinary `bridge.Source` seam stays read-only.
- The service creates at most one Workflow per account, with trigger
  `AssetCreate`, method `immich-plugin-core#webhook`, the configured callback
  URL and a generated bearer secret stored only in a mode-0600 file.
- Ownership is persisted in the single SQLite ledger (UUID, contract version,
  state, finite code, desired-config digest). The Workflow name and description
  carry a stable, non-secret account marker. Only the persisted UUID, verified
  against that marker, is ever updated or deleted.
- A durable `creating` intent precedes the POST. An uncertain create is
  reconciled by re-reading the authoritative workflow list: exactly one match is
  adopted, zero matches permits one retry, and more than one fails closed.
- Rotation of the callback URL or secret updates only the owned UUID. Account
  removal or disabling removes only the owned UUID; failure is recorded as a
  durable `cleanup_failed` diagnostic and logged without URLs or secrets.
- Immich v2, missing Workflow permissions or a missing declared trigger produce
  a finite polling notice instead of a failure loop. Periodic reconciliation is
  never disabled.
- Read-only log diagnostics expose aggregate run results only: no trigger data
  IDs, URLs, payloads or workflow UUIDs.

## Consequences

The service can automate Workflow setup without widening the polling key or
risking unrelated Workflows. Loss of the local ledger makes ownership
unprovable by UUID, so the deterministic marker is required for adoption; an
externally edited Workflow fails closed with `workflow_not_owned` rather than
being overwritten. An account removed by editing JSON directly cannot be
cleaned up after its credentials are gone; remove accounts through the local
management UI (or re-add them with management disabled) so cleanup can run.
