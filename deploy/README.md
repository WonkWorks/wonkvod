# WonkVOD deployment

`compose.yml` defines the persistent `wonkvod` project. The non-root backend is
private on `wonkvod_facade`; `wonkvod-tls` alone publishes the Plex TLS port.
The independently managed `plex-identity` project is not restarted by normal
backend deployments.

## Local configuration

```sh
cp deploy/.env.example deploy/.env
chmod 0600 deploy/.env
```

Replace the placeholders with this installation's sidecar hostname, URI,
machine identifier, paths, Docker network, and desired binds. Provider
bootstrap credentials belong in the protected file selected by
`WONKVOD_PROVIDER_ENV_FILE`, not in Compose or `deploy/.env`. `config.json`,
`catalog.json`, and `auth.json` live below `WONKVOD_STATE_DIR`. Web Setup binds
to loopback by default.

## Normal workflow

From a clean, tested checkout:

```sh
./deploy/deploy.sh deploy
```

The script supplies application version `1.0.0`, derives image tag/revision
from `git HEAD`, validates Compose, builds the backend, recreates only that
container, and waits for health. It does not restart TLS, the identity PMS, or
production Plex.

```sh
./deploy/deploy.sh config
./deploy/deploy.sh build
./deploy/deploy.sh status
```

Do not call `docker compose build` directly: Compose requires the revision
variables supplied by the script.

After deployment, load the ignored local environment before validating its URI:

```sh
set -a; . deploy/.env; set +a
curl --fail --silent --show-error "${WONKVOD_SECURE_URI}/identity" >/dev/null
docker inspect --format '{{.State.Health.Status}}' plex-identity
docker inspect --format '{{.State.Health.Status}}' plex
```

On first authenticated deployment, obtain the setup token locally:

```sh
docker logs wonkvod-backend 2>&1 | grep 'one-time setup token'
```

Do not paste that output into issues. Complete `/setup`; later starts use the
mode-`0600` `auth.json`.

## Rollback

Check out a known-good commit in a clean worktree and run the same deploy
command. Keep the immediately previous image until health, catalog, Plex API,
and media-range checks pass. Application rollback does not require changing
the identity or TLS container. If the rollback predates Web authentication,
treat its Web port as trusted-only and retain `auth.json` for the forward fix.
