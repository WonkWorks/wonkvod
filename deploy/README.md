# WonkVOD deployment

The persistent runtime is defined by `deploy/compose.yml` under the Compose
project `wonkvod`. The backend is private on the internal `wonkvod_facade`
network. `wonkvod-tls` alone publishes TCP 32499 and proxies to the backend.
The `plex-identity` project remains independently managed and is not restarted
for normal WonkVOD backend changes.

## Normal workflow

Commit and test changes, then run from the repository root:

```sh
./deploy/deploy.sh deploy
```

The script refuses a dirty checkout. It derives the seven-character image tag
and full OCI `org.opencontainers.image.revision` label from `git HEAD`, validates
Compose, builds `wonkvod-backend:<short-commit>`, recreates only the backend,
waits for health, and prints the running image ID and revision. The TLS proxy is
not rebuilt or restarted.

Useful commands:

```sh
./deploy/deploy.sh config
./deploy/deploy.sh build
./deploy/deploy.sh status
```

Do not invoke `docker compose build` directly: `compose.yml` intentionally
requires the commit variables supplied by the script, preventing stale or
ambiguous tags.

After deployment, verify the secure endpoint and dependent services separately:

```sh
curl --fail --silent --show-error \
  https://192-0-2-22.fixture.example.invalid:32499/identity \
  >/dev/null
docker inspect --format '{{.State.Health.Status}}' plex-identity
docker inspect --format '{{.State.Health.Status}}' plex
```

## Rollback

Check out the desired known-good commit in a clean worktree and run
`./deploy/deploy.sh deploy`. Existing commit-tagged images remain auditable and
may also be selected manually by exporting the matching `WONKVOD_VERSION` and
full `WONKVOD_GIT_COMMIT`, but the normal and safest rollback is a clean source
checkout followed by the deployment script.
