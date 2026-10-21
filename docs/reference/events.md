# Event HTTP contract

`POST /events/{account}` accepts durable hints for one configured account ID.
It shares the service listener with probes and optional management. It requires
its own bearer secret, independently of admin sessions and remote credentials.
See [event setup](../how-to/event-hints.md) for mounting and network access.

Send JSON with `Authorization: Bearer SECRET`, where `SECRET` is the trimmed
content of the configured `webhook_secret_file` or `WEBHOOK_SECRET_FILE`.
The file is read for each request. Its raw size must not exceed 4 KiB and its
trimmed secret must contain at least 32 bytes.

## Payloads

The compact form contains a stable asset ID:

```json
{"asset_id":"00000000-0000-4000-8000-000000000001"}
```

Native Immich Workflow webhooks are accepted directly:

```json
{
  "type": "AssetV1",
  "trigger": "AssetCreate",
  "data": {
    "asset": {
      "id": "00000000-0000-4000-8000-000000000001"
    }
  }
}
```

Native triggers are `AssetCreate`, `AssetMetadataExtraction` and `AssetTagged`.
Optional outer `config` and `workflow` fields and extra `data.asset` metadata
are accepted and discarded. Only the asset ID is retained. Paths, URLs, owners,
metadata and workflow credentials never select a download source.

Both forms require a hyphenated UUIDv4; uppercase hex is accepted and normalized
to lowercase. Do not combine a nonempty `asset_id` with native envelope fields.
Unknown top-level fields, unsupported native types/triggers, invalid IDs and
trailing JSON values are rejected. The body limit is 64 KiB, with a five-second
body-read deadline. A native payload exceeding that limit is rejected even if
its asset ID is otherwise valid.

## Responses

Responses are JSON objects with a finite `status` string.

| HTTP | `status` | Meaning |
| --- | --- | --- |
| `202` | `accepted` | Hint committed to local durable storage. |
| `400` | `invalid_event` | Invalid, unsupported or oversized payload. |
| `401` | `webhook_unauthorized` | Wrong/missing bearer value or unknown account. |
| `503` | `webhook_unavailable` | Secret unconfigured, unreadable or invalid. |
| `503` | `storage_unavailable` | Local hint persistence failed; no acknowledgment. |

For example, acknowledgment is `{"status":"accepted"}`. It confirms enqueue,
not upload, eligibility or successful album association. The handler makes no
Immich or Amazon calls. Duplicate hints coalesce within their account and survive
restart. A paused account can accept hints while its worker remains paused.

The worker re-reads Immich using the configured URL and key. Album mode checks
the hinted ID against each selected album; library mode reads the asset directly.
An event cannot widen the configured scope or establish absence/deletion.
Polling remains authoritative for missed events and membership changes.
