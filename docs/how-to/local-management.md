# Set up and use local management

The optional HTML dashboard uses the same listener as health/readiness. Native
defaults bind `127.0.0.1:8080`. The image listens on `0.0.0.0:8080` so Docker can
forward traffic; publish only on host `127.0.0.1`. A local admin passphrase is
separate from your Immich key and Amazon browser session. Never use an Amazon
credential as the admin passphrase.

## Choose a mode

| Mode | Start | Config editing |
| --- | --- | --- |
| Headless | `serve --config PATH`, no admin secret | No dashboard; CLI remains available. |
| Existing configuration | `serve --config PATH` with admin secret | Save requires an unfiltered config and a writable parent directory. |
| Read-only configuration | `serve` without a config path, or `serve --account ID` | Account controls remain available; configuration cannot be saved. |
| First setup | `serve --setup --config NEW_PATH` with admin secret | Creates the config only on the first valid account submission. |

The stock `compose.yaml` intentionally mounts `config.json` read-only. Enabling
the dashboard alone does not make that mount editable. Atomic saves need a
writable **directory**, not a writable single-file bind mount. Use the writable
`/data` configuration below or a separately prepared directory owned by UID
10001. Back up this config with the data volume.

## First setup in a local container

Build the local image and prepare credential files using
[credentials](credentials.md). Create `secrets/admin` with a local editor: use a
unique passphrase of at least 12 characters, not a command-line literal. On a
Linux host give all three files owner `10001:10001` and mode `0400`; keep the
host secret directory restricted. The following uses the same source-secret
paths as the normal Compose example, but a separate container and data volume.
Do not run it beside another writer on that volume.

```sh
docker compose build
docker volume create immich-sync-ui-data
docker run -d --name immich-sync-ui --init \
  --read-only --tmpfs /tmp:size=16m,mode=1777 \
  --cap-drop ALL --security-opt no-new-privileges=true \
  --stop-timeout 30 --publish 127.0.0.1:8080:8080 \
  --volume immich-sync-ui-data:/data \
  --mount "type=bind,source=$(pwd)/secrets/admin,target=/run/secrets/admin,readonly" \
  --mount "type=bind,source=$(pwd)/secrets/immich-api-key,target=/run/secrets/immich_api_key,readonly" \
  --mount "type=bind,source=$(pwd)/secrets/amazon-cookie,target=/run/secrets/amazon_cookie,readonly" \
  --env ADMIN_PASSWORD_FILE=/run/secrets/admin \
  immich-amazon-sync:local serve --setup --config /data/config.json
```

Open `http://127.0.0.1:8080/login` and enter the mounted admin passphrase. In
Source settings enter the trusted Immich URL and
`/run/secrets/immich_api_key`. List visible albums with only the source key;
discovery neither saves configuration nor selects a scope. Choose stable album
IDs or deliberately enable the whole accessible library, never both. In
Destination settings choose the actual marketplace and
`/run/secrets/amazon_cookie`. Leave videos and album mirroring off for a first
small test. The UI accepts paths, not key or Cookie values.

**Saving activates scheduled work and may write to Amazon.** Use disposable
originals first and review [compatibility](../reference/compatibility.md).
The config stays absent until a valid account and explicit scope are submitted;
the parent `/data` directory already exists with the image's nonroot ownership.

After the first save, stop/remove the container, then repeat the `docker run`
command using the **same volume and mounts** with the final line changed to:

```sh
docker stop immich-sync-ui
docker rm immich-sync-ui
# Repeat docker run with this image/command tail instead of --setup:
# immich-amazon-sync:local serve --config /data/config.json
```

Do not repeat `--setup` after the file exists. Normal missing-config errors do
not start setup automatically. Setup also rejects a symlink at the target path,
`--account`, missing admin secret and accounts supplied through `IMMICH_URL`.

For native use, create a private writable data directory, use native credential
paths, and keep the default loopback listener:

```sh
mkdir -m 700 data
ADMIN_PASSWORD_FILE="$PWD/secrets/admin" DATA_DIR="$PWD/data" \
  immich-amazon-sync serve --setup --config "$PWD/data/config.json"
```

## Existing configuration and account controls

To add the dashboard to a deployment, mount its admin file read-only and set
`ADMIN_PASSWORD_FILE` or JSON `admin_password_file` to that path.
Restart the service. A configured admin UI also works with zero accounts.
With no explicit config path or with `--account`, saving config is disabled so
a filtered view cannot overwrite unselected accounts. A read-only bind mount
also prevents saving even when an explicit path is supplied.

The overview shows account pause state, aggregate queue counts, finite error
codes and last successful reconciliation. Account actions pause/resume work,
retry eligible pending work or run read-only doctor diagnostics. Retry is not a
force-upload operation and cannot bypass uncertainty. Removing an account
changes configuration only; it keeps local history and cloud media. Every UI
save requires mounted source/destination credential paths for all accounts.
Local validation and an atomic mode-`0600` save precede reload; a failed save
does not change the running configuration.

## Sessions and exposure

- GET `/login` creates a 10-minute pre-auth session and CSRF token. Login needs
  that cookie and form token, then rotates both for an eight-hour admin session.
- The `immich_admin` cookie is HttpOnly, SameSite=Strict and scoped to `/`;
  it is Secure on a direct TLS request. Logout, process restart and admin-secret
  rotation revoke sessions. A protected tunnel retains the local-only model.
- Every state-changing form needs the current CSRF token. Mismatched Origin
  and `Sec-Fetch-Site: cross-site` requests are rejected. Login is limited to
  five attempts per minute per client address; forwarded headers are not trusted.
- Forms are limited to 64 KiB and five seconds to read. Management responses
  set no-store and a content security policy. Health/readiness stay public and
  do not require sessions or remote credentials.

These controls do not make a publicly exposed HTTP dashboard safe. The service
does not infer TLS from forwarded headers. Do not assume a reverse proxy makes
the backend cookie Secure or preserves per-client throttling. Keep loopback
publication and use a protected tunnel; design and validate any remote-access
boundary separately. The authenticated editor can show configured source URLs,
account IDs, album labels and secret paths, so treat browser output as private.
It does not display secret values, raw remote errors or asset URLs.

Rotate the admin passphrase by stopping the service, replacing its restricted
file and recreating the container so an atomic host replacement is remounted.
Sign in again; do not erase the data volume. Changes to data directory, listener,
admin-secret path or webhook-secret path require restart.
