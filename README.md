# WonkVOD

WonkVOD presents an Xtream-compatible video-on-demand catalog as virtual movie
and TV libraries to stock Plex clients. It serves the Plex-compatible metadata
API and streams source media bytes on demand; it does not import files into a
Plex library or require Plex scanners and analyzers to ingest the provider's
catalog.

The catalog core is source-neutral. Xtream is the current source adapter and
the Plex-compatible HTTP surface is the current client adapter.

```text
Xtream VOD provider
        |
        v
WonkVOD catalog + Plex adapter <--- Web Setup (trusted LAN)
        |
        v
TLS terminator using the identity PMS certificate
        |
        v
stock Plex clients

Claimed identity PMS sidecar ---> Plex identity, cloud presence, TLS certificate,
                                  and managed-user library entitlements
```

## What WonkVOD is not

WonkVOD is not a live-TV tuner or proxy, an xTeVe/Threadfin replacement, a DVR,
a video transcoder, a local-media importer, or a virtual linear-channel
generator. It exposes provider VOD as on-demand Plex libraries and relies on
the playback client being able to Direct Play the source codecs.

## Requirements

- A Linux host with Docker Engine and Docker Compose v2.
- A Plex account capable of claiming a small Plex Media Server identity
  sidecar.
- An Xtream-compatible provider with movie and/or series VOD APIs.
- Network access from WonkVOD to the provider, Plex services, and the LAN Plex
  clients that will use it.
- Persistent storage for configuration and catalog snapshots. Media files are
  not downloaded or cached, so disk use is primarily catalog JSON and normal
  container logs.
- Enough CPU and RAM for the catalog size. Metadata refresh work is bounded by
  the configured provider concurrency; playback streams through the service
  without a media-spooling layer. There is no claimed universal minimum for
  every provider or catalog size.

## Current installation

The current deployment assumes the source checkout is `/opt/wonkvod` and
persistent state is `/opt/wonkvod-runtime/state`. These paths are present in
[`deploy/compose.yml`](deploy/compose.yml); edit the deployment deliberately if
using different host paths.

There is not yet a one-command clean installation. In particular, provisioning
and claiming the identity PMS is still a manual prerequisite.

1. Install Docker Engine and Docker Compose v2 and place the source at
   `/opt/wonkvod`.
2. Create `/opt/wonkvod-runtime/state` for the non-root backend container. The
   directory holds `config.json` and `catalog.json`.
3. Create `/opt/wonkvod-runtime/xtream.env` with the bootstrap variables
   `WONKVOD_XTREAM_URL`, `WONKVOD_XTREAM_USER`, and
   `WONKVOD_XTREAM_PASS`. Protect this file as a credential file. These values
   seed `config.json` on first start; later Web Setup changes are persisted in
   the state directory.
4. Provision the claimed `plex-identity` sidecar described below. It must be on
   the external Docker network `plex-identity_default`, have persistent PMS
   configuration, and have acquired its `plex.direct` certificate before the
   TLS terminator can become useful.
5. Ensure the current host-specific endpoint, certificate mounts, and LAN Web
   Setup bind in `deploy/compose.yml` are appropriate for the installation.
6. Follow [`deploy/README.md`](deploy/README.md) for the current Compose
   bootstrap and normal deployment workflow. Normal upgrades use:

   ```sh
   ./deploy/deploy.sh deploy
   ```

   The script requires a clean worktree, validates Compose, builds a
   commit-labelled backend image, replaces only `wonkvod-backend`, and waits
   for health. It intentionally does not recreate the identity PMS or TLS
   terminator.
7. Open Web Setup on the trusted LAN. In the current deployment it is
   `http://HOST:32501/`.
8. On **Provider**, enter the Xtream server URL, username, and password, then
   test and save the connection.
9. On **Categories**, select the Movie and TV source categories and apply the
   selection.
10. On **Settings**, choose the refresh interval and catalog refresh
    concurrency. Select **Refresh Now** and wait for a successful atomic
    catalog promotion.
11. Confirm Movies and TV Shows are visible from a Plex client, then test a
    known Direct Play-compatible item before testing more demanding codecs.

The identity sidecar, claim, certificate, advertised connection, and tiny
control libraries are current setup debt. They are not yet automatically
created by the Web Setup UI.

## Plex identity architecture

WonkVOD uses a small, separately managed, claimed Plex Media Server container
as an identity sidecar. The sidecar supplies the Plex machine identity, cloud
presence, Plex-managed TLS certificate, and control-plane compatibility that
stock clients expect. It is not WonkVOD's media catalog.

The current sidecar uses the official `plexinc/pms-docker` image, persistent
configuration under `/opt/plex-identity/config`, and the Docker network
`plex-identity_default`. A TLS helper reads the PMS-managed certificate
material read-only and terminates HTTPS on WonkVOD's Plex endpoint. Extracted
private-key material is kept out of ordinary persistent storage.

Two tiny control libraries currently exist on the sidecar:

- `WonkVOD Movies`
- `WonkVOD TV Shows`

Plex Home managed users are granted access to those Plex library entitlements.
WonkVOD's virtual sections then provide the actual browsing and playback
surface. Home-user permission changes are currently manual.

Claim tokens, Plex account tokens, certificate secrets, and provider
credentials must never be committed or printed in logs.

## Configuration

Mutable configuration is stored atomically in
`/opt/wonkvod-runtime/state/config.json` on the host and mounted at
`/var/lib/wonkvod/config.json` in the backend. It includes:

- Xtream base URL, username, and password.
- Enabled Movie category IDs.
- Enabled TV category IDs.
- Catalog refresh interval.
- Catalog refresh concurrency: `1`, `2`, `4`, `6`, `8`, or `10`; default `2`.

Refresh concurrency controls only simultaneous provider metadata/detail
requests during catalog refresh. It does not limit Plex clients, media streams,
MediaFacts inspection, or simultaneous video playback. Lower settings are more
provider-friendly. A change applies to the next refresh and does not resize a
refresh already in progress.

Refresh Now and Full Rebuild also offer a one-time concurrency override. The
override is validated against the same values, applies only to that invocation,
and never changes the saved default. The default of `2` is intentionally
conservative. A large category bootstrap may reasonably use a higher temporary
value, but more concurrency is not always better and can trigger provider
limits.

Web Setup does not return the saved password. Leaving the password blank during
an update preserves the existing value. The UI currently has no authentication
and must remain on a trusted LAN.

## Catalog lifecycle

- The last good `catalog.json` snapshot loads immediately at startup.
- Normal **Refresh Now**, startup refresh, and the six-hour schedule are
  incremental. They fetch selected category lists, reuse unchanged canonical
  records, detect additions/removals/list-level changes, update membership-only
  changes without detail calls, and retry items in quarantine.
- Cold bootstrap with no usable snapshot necessarily fetches details for the
  complete selected catalog.
- **Full Rebuild** is an explicit, confirmed maintenance action that re-fetches
  details for every selected item. It is never selected automatically merely
  because the service restarted.
- Refresh and rebuild run in the background while the current catalog remains
  available. The configured concurrency applies to required detail calls in
  either mode.
- A completed catalog is saved and promoted atomically.
- Cancellation, systemic provider failure, or persistence failure leaves the
  current snapshot active.
- A refresh has a generous six-hour safety deadline. Individual provider
  requests remain separately bounded.
- The same source item in multiple selected categories is one canonical object
  with multiple memberships. It appears once globally and in every matching
  category filter; stable Plex IDs remain based on source identity.
- Isolated items whose metadata cannot be normalized are quarantined rather
  than blocking an otherwise healthy refresh. The **Quarantine** page reports
  sanitized failure class, HTTP/transport state, category membership, failure
  time, and retryability. Retry uses the normal atomic incremental build at
  provider-safe concurrency `1`; it never edits `catalog.json` in place and a
  failed retry leaves the live catalog intact. Older generic quarantine records
  remain readable.
- MediaFacts are inspected lazily when an item needs them and are merged into
  later catalog snapshots by stable source identity. Refresh does not probe the
  entire media catalog.
- Metadata and artwork are projected from the provider. Availability can vary
  by provider item.

Movie list provenance uses the provider's stable identity, name/year,
container, `added` value, and list presentation fields. TV uses equivalent list
presentation fields plus `last_modified`, which is the provider's inexpensive
signal for episode-tree changes. These signals make steady-state refresh cheap,
but they remain provider assertions rather than an independent content audit.
Use the explicit Full Rebuild operation for occasional deep revalidation when
provider behavior or metadata freshness is in doubt; WonkVOD does not schedule
an expensive periodic rebuild implicitly.

Before category selections are saved, **Preview Impact** uses only inexpensive
bulk category lists to estimate new and removed movies/shows, membership-only
changes, and overlaps. It performs no detail enrichment. More than 100 new
top-level items is treated as a large expansion: the UI explains that a longer
bootstrap is expected and suggests—but never automatically applies—a temporary
concurrency value. Normal incremental refreshes should remain short when few or
no detail requests are required; an unusually long steady-state run points to a
provider or network issue. Full Rebuild is explicitly expensive because every
selected item is re-fetched.

## Ports and paths

Current deployment defaults:

| Purpose | Current endpoint/path |
|---|---|
| Secure Plex-compatible endpoint | HTTPS TCP `32499` |
| Trusted-LAN Web Setup | HTTP TCP `32501` |
| Backend Plex listener | TCP `32499`, internal Docker network only |
| Identity PMS administrative mapping | host TCP `33400` to sidecar `32400` |
| Runtime state | `/opt/wonkvod-runtime/state` |
| Configuration | `/opt/wonkvod-runtime/state/config.json` |
| Catalog snapshot | `/opt/wonkvod-runtime/state/catalog.json` |
| Identity PMS state | `/opt/plex-identity/config` |

The checked-in Compose file contains installation-specific LAN and
`plex.direct` values. Review those values when installing on another host.

## Upgrades, backups, and rollback

Commit and test source changes, then use `./deploy/deploy.sh deploy`. The script
refuses dirty source and labels images with the exact Git revision. Rollback is
normally a clean checkout of a known-good commit followed by the same deploy
command; see [`deploy/README.md`](deploy/README.md).

Back up these non-reproducible assets:

- `/opt/wonkvod-runtime/state/config.json`
- `/opt/wonkvod-runtime/state/catalog.json`
- `/opt/plex-identity/config`
- any locally customized deployment configuration or branding

The catalog can be rebuilt from the provider, but its saved snapshot preserves
immediate startup and lazy MediaFacts. The identity configuration contains the
claimed PMS identity and certificate lifecycle state. There is no persistent
media cache or spool that needs backup.

## Limitations

- WonkVOD does not transcode or remux video or audio. Client codec and container
  support determine whether an item Direct Plays.
- Identity sidecar provisioning, Plex claiming, and managed-user entitlements
  are not yet fully self-service.
- Web Setup is trusted-LAN only and currently has no authentication.
- Large provider catalogs can take a long time to refresh, especially at the
  conservative default concurrency.
- Provider metadata may be incomplete or temporarily unavailable.
- Roku movie and TV browsing/playback have been validated for compatible media;
  compatibility is not guaranteed for every codec combination.
- iPhone client compatibility remains unresolved.

## Troubleshooting

### Refresh is taking a long time

Open **Overview** to see the running state and elapsed time. The previous
catalog remains active. Large selections can legitimately take hours with the
default concurrency of 2. Do not repeatedly restart a healthy refresh.

### Refresh failed

Overview displays the sanitized failure. The previous snapshot remains active.
Check provider reachability and container logs. A provider that rejects
parallel requests may require concurrency 1 or 2; do not raise concurrency as
a first response to failures.

### Category changes do not appear in Plex

**Apply Categories** saves desired selection only. Select **Refresh Now** and
wait for successful promotion.

### A client reports a transcoding or playback error

WonkVOD does not transcode. Confirm the source container, video codec, audio
codec, channel layout, subtitle format, and the client's Direct Play support.

### Plex identity or claim problems

Check the **Plex** page, the `plex-identity` container health, its persistent
configuration, claim state, and cloud presence. Do not delete or reclaim a
working identity as a routine troubleshooting step.

### A managed user cannot see WonkVOD

Confirm that the user has Plex Home access to both identity-sidecar control
libraries, `WonkVOD Movies` and `WonkVOD TV Shows`. Do not recreate those
libraries solely to refresh permissions.

### TLS or secure URI is unavailable

Check the **Plex** status page, `wonkvod-tls`, certificate readiness in the
identity sidecar, DNS resolution for the advertised `plex.direct` hostname,
and the cloud resource connection. Never copy certificate private keys into
logs or ordinary persistent files.

## Uninstalling or retiring WonkVOD

Stop and remove the WonkVOD backend and TLS containers using the Compose project.
Decide explicitly whether to retain or remove `/opt/wonkvod-runtime/state`.
Treat the claimed identity sidecar separately: preserve it for rollback or
retire it deliberately, including its Plex account registration. Removing
WonkVOD does not require removing or modifying an unrelated production Plex
server.

## Further documentation

- [Web Setup](docs/WEB_SETUP.md)
- [Plex identity setup lifecycle](docs/PLEX_SETUP.md)
- [Deployment and rollback](deploy/README.md)
- [Legacy architecture](docs/LEGACY_ARCHITECTURE.md)

