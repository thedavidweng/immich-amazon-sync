# Obtain and renew credentials

Create `secrets/` with mode `0700`; keep it outside version control.
The image and Compose examples contain only paths, never real credentials.

## Immich

Create an API key in Immich account settings for the user whose assets you want
to back up. For polling, grant `user.read`, `album.read`, `asset.read`,
`asset.download`. Write only the key value into `secrets/immich-api-key`.
Permissions do not bypass sharing or ownership restrictions. A key with
`asset.read` still needs `asset.download` to obtain originals.

Workflow setup is optional and version-dependent. Polling requires no write or
workflow scopes. Manual workflow creation in Immich can keep this service's
API key read-only. See [compatibility](../reference/compatibility.md).

## Amazon

Sign into Amazon Photos in your own browser on the configured marketplace.
Finish password/MFA/CAPTCHA there. Open Developer Tools, select Network, reload
Photos and choose a successful authenticated request to the regional Drive API.
Copy the **Cookie request-header value**, without the `Cookie:` prefix, into
`secrets/amazon-cookie` using a local editor. The service expects a header value,
not an aphotos profile, browser cookie JSON or a Netscape cookie export.

The value contains session credentials. Do not paste it into a command argument,
issue, chat, screenshot or Compose environment block. Do not export unrelated
browser credentials. The SDK accepts canonical hyphenated cookie names and
normalizes recognized underscore aliases. Use the matching marketplace:
`session-id` plus `ubid-main`/`at-main` for US; other regions have their own
`ubid-acb*`/`at-acb*` names. See the implementation-backed
[region table](../reference/compatibility.md).

On a Linux Docker host:

```sh
sudo chown 10001:10001 secrets/immich-api-key secrets/amazon-cookie
sudo chmod 0400 secrets/immich-api-key secrets/amazon-cookie
docker compose run --rm --entrypoint sh sync -ec \
  'test -r /run/secrets/immich_api_key; test -r /run/secrets/amazon_cookie'
```

Compose file-backed secrets use bind mounts; Compose's secret `uid`, `gid` and
`mode` options cannot change the source-file ownership. Fix host permissions
instead. Docker Desktop file sharing may present ownership differently; the
readability check above is authoritative. Keep the files readable by the
container user while restricting host access.

## Renew a session or source key

An authentication error pauses affected work; it is not a reason to erase state.
Stop the daemon, obtain fresh credentials for **the same source user and Amazon
account**, replace the relevant file with the same restricted ownership, and run:

```sh
docker compose stop sync
docker compose run --rm sync --config /etc/immich-amazon-sync/config.json doctor --json
docker compose up -d --force-recreate sync
```

Recreating remounts files replaced by rename, which an existing bind mount can
otherwise continue reading through the old inode. If the runtime reports a
persisted pause after renewal, use the account retry operation described by
`--help`/the local management interface. Retain all mappings and uncertain jobs.
Never switch the backing user/account under an existing configured account ID.
Use a new ID for a different identity.

A 403 can mean missing permission rather than session expiry. A 429 requires
waiting for the reported retry interval. Amazon credentials are browser sessions,
not OAuth refresh tokens; this service does not automate login challenges.

## Local admin passphrase

The optional dashboard uses a separate mounted file with a unique passphrase
of at least 12 characters. Set `ADMIN_PASSWORD_FILE` or the exact JSON key
`admin_password_file` to its container path. Do not put the value in
JSON or reuse an Amazon credential. Keep the same restricted file permissions
as other secrets. Rotate it by stopping the service, replacing the file and
recreating the container so a renamed file is remounted. Sessions are revoked;
the data volume and account identities remain intact. See
[local management](local-management.md) for explicit setup and security limits.
