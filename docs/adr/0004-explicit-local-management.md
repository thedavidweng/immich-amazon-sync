# 0004: Explicit local setup with file-backed secrets

Status: accepted. Date: 2026-10-20.

Management is optional on the existing listener. No admin credential means no
management routes; probes remain public and independent of remote accounts.
Keep native loopback defaults and host-loopback container publication. An HTML
form interface suffices; do not create a parallel JSON management API.

Setup is an explicit `serve --setup --config NEW_PATH` operation, not a fallback
for missing or malformed configuration. Require a mounted admin passphrase of
at least 12 characters and an existing writable parent directory. Reject
existing paths including symlinks and filtered/environment account setup. Only
the first valid scoped account submission creates the file; normal subsequent
starts omit `--setup`.

Config editing requires an explicit unfiltered config path. No-path or
`--account` runs retain account controls without write access to configuration.
The stock Compose config mount stays read-only. Atomic replacement requires a
writable directory; document a separate `/data/config.json` deployment rather
than promising that a writable single-file bind can be replaced.

UI saves use mounted credential paths for every account, validate locally,
atomically persist mode 0600, then reload. Never serialize secret values or
reload failed saves. Listener/data/secret-path changes require restart. Removing
an account preserves local history and cloud media; retry cannot authorize
blind replay of an uncertain write.

Use pre-auth CSRF for login, rotate session/token on authentication, and require
session CSRF on all mutations. Sessions are in memory and end on logout,
restart or secret rotation. HttpOnly/SameSite cookies, origin checks, throttling,
bounded forms and no-store/CSP complement loopback isolation, not replace it.
Do not trust forwarded headers or claim proxy TLS makes the backend cookie
Secure. The public container HTTP seam verifies local login/CSRF/logout without
cloud credentials on both architectures.
