# Enable Immich event hints

Start with working polling and a small disposable test scope. Events accelerate
discovery; polling repairs missed deliveries and membership changes. Native
Immich Workflow payloads are accepted directly without a translator. The exact
request limits and acknowledgment semantics are in the
[event HTTP reference](../reference/events.md).

## Mount a separate shared secret

Create a random secret of at least 32 bytes in `secrets/webhook-secret`, outside
version control. The file must be no larger than 4 KiB. Restrict host access and
make it readable by container UID 10001, as with the
[other mounted credentials](credentials.md). Do not reuse an admin passphrase,
source API key or Amazon Cookie.

The stock Compose file mounts only the source and destination credentials.
Create `compose.events.yaml` in your deployment directory:

```yaml
services:
  sync:
    environment:
      WEBHOOK_SECRET_FILE: /run/secrets/webhook_secret
    secrets:
      - webhook_secret

secrets:
  webhook_secret:
    file: ./secrets/webhook-secret
```

Use the same Compose file combination for subsequent deployment commands:

```sh
docker compose -f compose.yaml -f compose.events.yaml up -d --force-recreate sync
```

Alternatively, mount a read-only secret file in your own deployment and set
`webhook_secret_file` to its container path in JSON. Changing that path requires
a restart. The handler rereads the contents on each request, but replacing a
host file by rename may leave a bind mount on the old inode; recreate the
container after rotation and update the Workflow header.

## Configure the native Workflow

In an Immich version with Workflows, manually create a webhook step for a
supported trigger: `AssetCreate`, `AssetMetadataExtraction` or `AssetTagged`.
Set its URL to the service's `/events/ACCOUNT_ID`, using the exact configured
local account ID. Set an `Authorization` header to `Bearer ` followed by the
mounted shared secret's contents. Protect that header as a credential.

Keep the service's Immich polling key read-only. This service does not register
or modify Workflows automatically. Immich v2 has no Workflow support; use polling
there. Check [compatibility](../reference/compatibility.md#immich-contract)
before depending on a particular server version.

Configure reachability deliberately. Compose publishes host `127.0.0.1` only;
`localhost` inside an Immich container refers to that container. A private
Docker network or protected route must make the service reachable from Immich.
The listener also serves probes and optional management; exposing it affects
all those routes. Keep access restricted rather than publishing it to the
Internet for event delivery.

## Check delivery and recovery

Use a disposable original within the configured scope. HTTP `202` means its
hint is durable, not that Amazon has received it. Inspect `events_pending`,
`events_failed`, media and association counts in the
[status report](../reference/status.md). An out-of-scope or unavailable asset
does not authorize a write or deletion.

For `401`, verify the account ID and bearer header. For `400`, check the native
type/trigger, UUIDv4 and 64 KiB limit. For `503`, check the mounted secret or
local storage according to the finite response code. Keep periodic polling
enabled regardless of delivery success: no durable upstream replay guarantee
or album-membership event is assumed.
