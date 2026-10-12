# WonkVOD Web Setup

Web Setup is the authenticated administrator service on container port 32501.
The checked-in deployment binds it to loopback by default; a local
`deploy/.env` may bind it to a trusted LAN address. Do not expose plain HTTP to
the public Internet.

## Authentication

Authentication state is separate from provider configuration at
`/var/lib/wonkvod/auth.json` (normally
`/opt/wonkvod-runtime/state/auth.json`). Atomic mode-`0600` writes contain only
a versioned Argon2id hash and password-set timestamp.

When that file is absent, each process start creates a random one-time token
and prints it only to local container/application logs. The raw token is never
persisted or returned by an unauthenticated API. Enter it at `/setup` with a
password of at least 12 characters. Successful setup invalidates the token and
closes the setup flow.

Login creates a random in-memory session lasting 12 hours. Restart invalidates
sessions. The cookie is HttpOnly, SameSite=Strict, Path=/, and Secure when the
Web request uses HTTPS. State-changing APIs require the random per-session
`X-WonkVOD-CSRF` token. Setup/login failures are rate-limited per client.

Normal pages and `/api/*` require authentication. Only `/healthz`, login/setup
pages and APIs, and required branding are public. The Plex listener on port
32499 is separate and is never placed behind Web admin authentication.

Changing the password in Settings validates the current password, atomically
writes a new hash, invalidates every session, and creates a replacement session
for the current browser. There is no browser recovery. A local administrator
may stop only the backend, move `auth.json` to protected backup storage,
restart, and use the new local setup token. Do not hand-edit hashes.

## Catalog behavior

Provider/catalog settings are atomically stored in `config.json`. Environment
values seed a missing file. GET APIs never return the saved provider password;
omitting it from an update preserves it.

Applying category selections never clears the live catalog. Refresh Now builds
and atomically promotes a candidate; failure retains the last usable snapshot.
Preview uses bulk lists to estimate additions, removals, membership changes,
and overlaps without detail calls. Saved concurrency defaults to `2`; one-time
overrides do not change it. Full Rebuild deliberately refetches every selected
object.

Quarantine shows sanitized diagnostics. Retry uses the same atomic incremental
path at concurrency `1` and preserves the live snapshot on failure.

Plex integration in Web Setup is read-only status. The claimed identity,
certificate, cloud registration, and control libraries remain externally
managed. A ready installation states that no setup actions are required.
