# Your first album backup

Start with a small album containing disposable test originals. This tutorial
uses a local image and a named volume; it needs no published image or release.
It makes real Amazon writes only at the explicitly marked sync step.

Review [SDK list semantics and remaining limits](../reference/compatibility.md#sdk-list-semantics-and-remaining-limits)
before writing. Valid empty lookup envelopes are supported, but an
`upload_unconfirmed` result still requires reconciliation. Do not erase state
or force an upload to bypass uncertainty.

1. Clone the repository, enter it, and run `docker compose build`. Copy
   `docs/reference/config.example.json` to `config.json`. Keep this deployment
   directory and Compose project name stable so upgrades reuse the same volume.
2. Follow [credentials](../how-to/credentials.md) to create the two secret files.
   Use only `user.read`, `album.read`, `asset.read`, `asset.download` for a polling
   Immich key. Complete Amazon sign-in/MFA in your own browser on the correct
   marketplace; no Amazon password is given to this service.
3. Set `immich_url` and `amazon_region` in `config.json`. A source URL may include
   `/api`; otherwise the adapter appends it. The source must be reachable from
   the container: container localhost is not the host or the Immich container.
   Use a trusted LAN hostname or explicitly attach an existing Docker network.
4. Leave the example album UUID temporarily and list accessible albums:

   ```sh
   docker compose run --rm sync --config /etc/immich-amazon-sync/config.json albums list
   ```

   Replace `album_ids` with the desired stable IDs. Names are display labels;
   identical names do not identify an album. Keep `library`, `include_videos`
   and `album_sync_mode` (`off` for the first check) conservative.
5. Diagnose configuration, credentials, remote identities and local storage:

   ```sh
   docker compose run --rm sync --config /etc/immich-amazon-sync/config.json doctor --json
   ```

   Resolve errors before writing. A successful read-only probe confirms access
   to those reads, not upload permissions or regional/media compatibility.
6. **Write step:** run one backup of the selected scope:

   ```sh
   docker compose run --rm sync --config /etc/immich-amazon-sync/config.json sync once --json
   ```

   Inspect the result and your Amazon account. An `unconfirmed` result is not
   success; follow [troubleshooting](../how-to/troubleshooting.md), retaining
   the data volume. One invocation is bounded; a large scope can still have
   pending scans/media after exit 0. Check [durable status](../reference/status.md)
   and start `serve` for automatic continuation.
7. Start periodic operation and inspect the local health probe:

   ```sh
   docker compose up -d
   docker compose ps
   docker compose exec sync wget -q -O - http://127.0.0.1:8080/healthz
   docker compose exec sync wget -q -O - http://127.0.0.1:8080/readyz
   ```

The named volume is initialized with ownership `10001:10001` from the image.
SQLite state and staging persist under `/data`; secret files stay outside it.
Probe success means local readiness. Check account diagnostics/logs for remote
failures. The default scan interval is 15 minutes.
Event hints and continuing backfill do not wait for that interval. After the
basic backup works, optionally configure [native Workflow hints](../how-to/event-hints.md)
or [managed albums](../how-to/managed-albums.md) with disposable test data first.

Stop the daemon before any other process uses its data directory. Use
`docker compose stop sync` to stop and `docker compose up -d` to resume.
`docker compose down` preserves the named volume; never add `--volumes` or `-v`
unless you intend to destroy all local recovery state.
