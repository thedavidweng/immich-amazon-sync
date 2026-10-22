# Managed Immich Workflow onboarding

Event hints shorten discovery latency; they never replace polling. Periodic
full and incremental reconciliation stays enabled whether or not a Workflow is
managed. See [event setup](event-hints.md) for the manual webhook path and the
[event HTTP contract](../reference/events.md) for payload limits.

Managed onboarding is an explicit, separately authorized write capability. The
polling key remains read-only. On a supported Immich v3.x server with the
required permissions, the service creates, verifies and later updates exactly
one native Workflow that it owns.

## Prerequisites

- Immich v3.x with Workflows. Immich v2 has none: the service reports a polling
  fallback and makes no write attempt.
- A **second** Immich API key, stored in its own mounted file, with
  `workflow.read`, `workflow.create`, `workflow.update` and `workflow.delete`.
  `workflow.logs` additionally enables read-only run diagnostics. Do not reuse
  the polling key path.
- A callback URL reachable from Immich that ends in this account's event
  endpoint, for example `https://sync.example.net/events/home`. It must be
  HTTP(S), without embedded credentials, query or fragment.
- A writable path for the generated bearer secret. The service creates the file
  with mode `0600` on first apply; it never writes the value into JSON.

## Configure

```json
{
  "accounts": [
    {
      "id": "home",
      "immich_url": "https://photos.example.net",
      "immich_api_key_file": "/run/secrets/immich_key",
      "amazon_region": "ca",
      "amazon_cookie_file": "/run/secrets/amazon_cookie",
      "album_ids": ["11111111-1111-4111-8111-111111111111"],
      "managed_workflow": true,
      "immich_workflow_api_key_file": "/run/secrets/immich_workflow_key",
      "workflow_callback_url": "https://sync.example.net/events/home",
      "workflow_secret_file": "/run/secrets/workflow_secret"
    }
  ]
}
```

`managed_workflow` defaults to `false`; existing accounts never gain write
scope through a migration. The equivalent environment-mode settings are
`MANAGED_WORKFLOW`, `IMMICH_WORKFLOW_API_KEY_FILE`, `WORKFLOW_CALLBACK_URL` and
`WORKFLOW_SECRET_FILE`.

The editor in the [local management interface](../how-to/local-management.md)
exposes the same fields. The generated secret is displayed only as a path.

## What the service does

On each account cycle, when `managed_workflow` is enabled:

1. Detect `Immich v3.x`, the key's Workflow permissions and the declared
   `AssetCreate`/`AssetV1` trigger. A missing version, permission or trigger is
   recorded as a polling notice, not a retry loop.
2. Look up the persisted owned workflow UUID, or list workflows for an exact
   ownership marker (`immich-amazon-sync` plus the account's stable marker).
   Only one exact match is ever adopted; zero matches allow a create.
3. Before creating, persist a durable `creating` intent. After any ambiguous
   create response the next step lists workflows again: one match is adopted,
   no match permits one retry, more than one match fails closed. This is why a
   timeout or retry never produces a second Workflow.
4. Read the workflow back and verify the trigger, step method, callback URL and
   bearer header. A read-back mismatch keeps the state visible and never
   creates a duplicate.
5. On later cycles, verify the persisted UUID in place. A changed callback URL
   or generated secret updates only that UUID.

The service never lists, updates or deletes workflows for other accounts and
never mutates a UUID it did not persist. A workflow that was renamed or edited
externally fails closed with `workflow_not_owned`.

## Rotate the callback or secret

Replace the generated secret file (or delete it to let the service generate a
new one) and/or change `workflow_callback_url`, then apply:

```sh
immich-amazon-sync workflow register --apply
```

Only the owned Workflow is updated. Inbound events accept the generated secret
only while the account's ledger records an active, creating or unconfirmed
owned workflow.

## Disable or remove an account

Turning `managed_workflow` off, or removing the account from configuration,
performs best-effort deletion of only the owned Workflow. The result is durable:

- Success clears the persisted UUID and records `removed`.
- Failure records `cleanup_failed` with a finite code. The dashboard lists
  orphaned workflow cleanup for accounts no longer configured, and the
  structured log records `workflow_cleanup_failed`-class diagnostics without
  URLs or secrets.

`workflow remove` refuses while management is still enabled, because the next
cycle would recreate the Workflow. Disable management first, then retry:

```sh
immich-amazon-sync workflow remove --apply --account home
```

## Commands

| Command | Behavior |
| --- | --- |
| `workflow status` | Read-only persisted state, contract and finite code. No remote calls. |
| `workflow register` | Read-only plan: capability, ownership and the action that would be taken. |
| `workflow register --apply` | Create, adopt, verify or update only the owned Workflow. `--force` re-probes after a polling notice. |
| `workflow remove --apply` | Delete only the owned Workflow for accounts whose management is disabled. |
| `workflow logs` | Read-only aggregate run counts (`completed`, `halted`, `error`) and the latest run time; never IDs, URLs or payloads. |

`--json` is available on each command. No command prints the callback URL, the
bearer secret or the workflow UUID.

## Fallback notices

| Status `workflow_code` | Meaning |
| --- | --- |
| `workflow_version_unsupported` | Immich is not v3.x; polling continues and no write is attempted. |
| `workflow_permission_missing` | The separate key lacks a required Workflow grant. Grant it and run `workflow register --apply --force`. |
| `workflow_trigger_unsupported` | The server does not declare the required trigger/type pair. |
| `workflow_unconfirmed` | A create or update may have committed; the next cycle reconciles by reading. |
| `workflow_ambiguous` | More than one exact owned workflow exists; nothing is created or deleted. |
| `workflow_not_owned` | The persisted UUID no longer carries this service's ownership marker; nothing is mutated. |
| `workflow_cleanup_failed` | Best-effort removal failed; the state stays visible for retry. |

Polling is never disabled by these states. Missed or rejected events are always
recovered by the normal reconciliation schedule.
