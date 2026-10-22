# immich-amazon-sync

A self-hosted, one-way backup service that continuously syncs original photos and videos from [Immich](https://github.com/immich-app/immich) to Amazon Photos.

The service runs as a single container backed by a durable local SQLite ledger. It preserves original byte streams without transcoding or quality reduction, queries remote checksums before dispatching uploads to avoid duplicate storage, and enforces non-destructive defaults.

## Quick start

### Prerequisites

- Docker Engine with Compose v2+
- Immich instance and API key
- Amazon Photos session cookie

### 1. Preparation

Clone the repository and prepare configuration and secrets directories:

```bash
git clone https://github.com/thedavidweng/immich-amazon-sync.git
cd immich-amazon-sync
cp docs/reference/config.example.json config.json
mkdir -m 700 secrets
```

### 2. Credentials

Save your Immich API key and Amazon Photos session cookie to secret files:

```bash
echo "YOUR_IMMICH_API_KEY" > secrets/immich-api-key
echo "YOUR_AMAZON_COOKIE_HEADER" > secrets/amazon-cookie
chmod 600 secrets/*
```

For instructions on obtaining the session cookie, see the [Credentials guide](docs/how-to/credentials.md). On Linux hosts, verify that files are readable by container UID 10001 (or set permissions to `0644`).

### 3. Configuration

Edit `config.json` to specify your Immich server URL, Amazon marketplace region (`us`, `ca`, `de`, `uk`, `jp`, etc.), and backup scope (whole library or specific album IDs):

```json
{
  "data_dir": "/data",
  "listen": "0.0.0.0:8080",
  "accounts": [
    {
      "id": "primary",
      "immich_url": "http://immich-server:2283",
      "immich_api_key_file": "/run/secrets/immich_api_key",
      "amazon_region": "us",
      "amazon_cookie_file": "/run/secrets/amazon_cookie",
      "library": true
    }
  ]
}
```

### 4. Verification and startup

Build the container image and run read-only diagnostic checks:

```bash
docker compose build
docker compose run --rm sync --config /etc/immich-amazon-sync/config.json doctor --json
```

Start the synchronization daemon in the background:

```bash
docker compose up -d
docker compose exec sync wget -q -O - http://127.0.0.1:8080/readyz
```

## CLI operations

The container provides diagnostic and inspection commands:

```bash
# Test connectivity and credentials
docker compose run --rm sync --config /etc/immich-amazon-sync/config.json doctor --json

# List available albums and IDs from Immich
docker compose run --rm sync --config /etc/immich-amazon-sync/config.json albums list

# Run a single synchronization pass and exit
docker compose run --rm sync --config /etc/immich-amazon-sync/config.json sync once --json
```

An optional local web dashboard can be enabled by providing an admin passphrase file. See [Local management](docs/how-to/local-management.md).

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/dashboard-dark.webp">
  <img alt="Local dashboard showing one account's backup state, queue distribution and schedule, with detailed counters collapsed" src="docs/images/dashboard-light.webp" width="960">
</picture>

<sub>Screenshot uses sample data.</sub>

## Capabilities

- **Original preservation**: Transfers exact byte streams; never transcodes, compresses, or modifies EXIF data.
- **Deduplication**: Matches byte size and MD5 before dispatch to reuse existing remote files and prevent duplicate uploads.
- **Album synchronization**: Supports selected album IDs, whole-library backups, and remote album mirroring.
- **Deletion safety**: Remote deletions are disabled by default. Optional trash propagation requires explicit typed confirmation and strictly guards against mass deletion.
- **Direct disk access**: Optionally reads asset files directly from read-only host mounts to eliminate network overhead.
- **Transfer windows**: Configurable IANA timezone schedules to restrict uploads to off-peak hours.
- **Apple Live Photos**: Optional preservation of still and companion MOV pairs as independent originals.
- **Favorites synchronization**: Opt-in one-way or two-way mirroring of favorite status.

Internal guarantees, recovery handling, and pipeline details are documented in [Architecture and design principles](docs/explanation/architecture.md).

## Documentation

| Guide | Description |
| --- | --- |
| [First backup tutorial](docs/tutorials/first-backup.md) | Guided walkthrough for initial setup and verification |
| [Credentials](docs/how-to/credentials.md) | Extracting and renewing Amazon session cookies and Immich API keys |
| [Local management](docs/how-to/local-management.md) | Enabling and using the optional local web dashboard |
| [Direct originals](docs/how-to/direct-originals.md) | Mounting local Immich storage to bypass network downloads |
| [Managed albums](docs/how-to/managed-albums.md) | Album sync modes, ID resolution, and membership guards |
| [Operations](docs/how-to/operations.md) | Backup, restore, migrations, and transfer window scheduling |
| [Event hints and workflows](docs/how-to/event-hints.md) | Webhook triggers and [managed Immich workflows](docs/how-to/managed-workflows.md) |
| [Troubleshooting](docs/how-to/troubleshooting.md) | Resolving uncertain writes, auth failures, and error states |
| [Configuration reference](docs/reference/configuration.md) | Complete JSON schema options and defaults ([example config](docs/reference/config.example.json)) |
| [CLI reference](docs/reference/cli.md) | Command syntax, subcommands, flags, and exit codes |
| [Status and metrics](docs/reference/status.md) | Health endpoints, progress counters, and Prometheus metrics |
| [Compatibility](docs/reference/compatibility.md) | Upstream pins, quota handling, and [feature parity](docs/reference/parity.md) |
| [Architecture](docs/explanation/architecture.md) | Service pipeline, deduplication, and operational guarantees |
| [Safety model](docs/explanation/safety.md) | Idempotency, uncertainty tracking, and deletion guards |
| [Architecture decisions](docs/adr/) | Historical record of design choices (ADRs) |

## Acknowledgements

- [immich-gphotos](https://github.com/ImAleeexx/immich-gphotos) for the workflow patterns, reconciler model, and user scenarios.
- [aphotos](https://github.com/thedavidweng/amazon-photos) for the Amazon Photos Go SDK.
- [Rainforest](https://github.com/thedavidweng/rainforest) for local desktop backup patterns and protocol research.
- [vanessa/immich-amazon-sync](https://github.com/vanessa/immich-amazon-sync) for early integration scenarios.

## Disclaimer

Amazon Photos has no official public API. This service uses an unofficial SDK; sessions expire and upstream behavior can change without notice. Always maintain an independent primary backup of your media.

## License

Licensed under [AGPL-3.0-only](LICENSE). Third-party software notices and licenses are documented in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
