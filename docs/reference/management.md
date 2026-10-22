# Local management HTTP contract

Management is optional HTML forms, not a JSON management API. Routes exist only
when an admin passphrase file is configured. They share the normal listener;
`GET /healthz` and `GET /readyz` remain public independently.

| Route | Form/behavior |
| --- | --- |
| `GET /login` | Issue or reuse pre-auth cookie and hidden `csrf` token; authenticated users redirect home. |
| `POST /login` | `csrf`, `password`; rotate session and CSRF on success. |
| `POST /logout` | `csrf`; revoke session. |
| `GET /` | Authenticated overview, aggregate durable counts and account controls. |
| `GET /accounts/new` | Source and Destination editor for a new account. |
| `GET /accounts/{id}/edit` | Existing account editor. |
| `POST /accounts/discover` | `csrf`, source URL/key file and editor fields; source-only album lookup without save or scope selection. |
| `POST /accounts/save` | `csrf`, `original_id`, `id`, `immich_url`, `key_file`, `region`, `cookie_file`, `album_ids`, repeated `album_id`, `media_image`, `media_video`, `max_original_bytes`, `skip_raw`, `include_archived`, `excluded_tags`, and optional `library`, `mirror_albums`, `deletions`, `deletions_confirmation`. |
| `POST /accounts/{id}/pause`, `/resume`, `/retry`, `/doctor` | `csrf`; account control or read-only diagnostics, not a cloud-write probe. |
| `POST /accounts/{id}/remove` | `csrf`; configuration removal, not local-history or cloud-media deletion. |

`original_id` is empty for a new account. `album_ids` accepts comma/whitespace
separation; repeated `album_id` values come from discovery checkboxes. Boolean
checkboxes are `on` or absent. Scope must be explicit: album IDs or whole library.
All credentials are mounted file paths, never form secret values.
The overview's durable progress fields are described in the
[status reference](status.md), including separate media, album and association
completion. Album mirroring requires explicit album rules with library mode off.

Login without a valid pre-auth cookie/token and authenticated mutations without
CSRF are rejected. See [local management](../how-to/local-management.md) for
session lifetimes, security limits, writable-mode requirements and first setup.
