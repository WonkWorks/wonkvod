# Plex identity setup lifecycle

WonkVOD currently integrates with a separately managed, claimed Plex Media
Server sidecar. This document defines the path toward self-service setup while
keeping an existing identity safe.

## Current sidecar

The current deployment uses:

- official image `plexinc/pms-docker:latest`;
- persistent PMS configuration at `/opt/plex-identity/config`, mounted at
  `/config`;
- container TCP 32400, mapped to host TCP 33400 for administrative access;
- Docker network `plex-identity_default`, where WonkVOD reaches
  `http://plex-identity:32400`;
- FriendlyName `WonkVOD`;
- `ADVERTISE_IP` pointing at the WonkVOD facade on host port 32499;
- control libraries `WonkVOD Movies` and `WonkVOD TV Shows`;
- restart policy `unless-stopped`.

On first provisioning, PMS generates its machine identifier in the persistent
preferences, consumes a short-lived Plex claim token or completes Plex's
supported claim flow, registers the server with Plex, and acquires its
`plex.direct` PKCS#12 certificate. Claim tokens and account tokens are secrets
and must not be logged, returned by status APIs, or committed.

The sidecar renews its certificate in its persistent cache. `wonkvod-tls` mounts
the cache and preferences read-only, validates the certificate and hostname,
keeps decoded key material in memory, polls the source every 60 seconds, and
atomically retains the last good certificate if a reload fails.

The sidecar owns identity and control-plane compatibility, not the virtual
catalog. Managed-user access is currently granted manually through the two
control-library entitlements.

## Plex client authorization

WonkVOD delegates Plex-client authorization to the identity sidecar. For each
new Plex token, the backend makes a direct request to the sidecar's
`/library/sections` endpoint and uses the visible control sections as
entitlement anchors:

- section 1 (`WonkVOD Movies`) grants the virtual Movies library;
- section 2 (`WonkVOD TV Shows`) grants the virtual TV library.

Clients may send the token in the `X-Plex-Token` header or query parameter. A
non-empty header takes precedence. WonkVOD does not accept bearer tokens or
cookies as Plex authorization, and it never logs or persists raw Plex tokens.
Validation results are held only in a bounded in-memory cache keyed by a
SHA-256 token digest: valid grants expire after two minutes and invalid tokens
after 30 seconds. Sidecar failures are not cached and fail closed.

This is separate from Web administrator authentication on port 32501. The Web
login/session and CSRF controls do not authorize Plex protocol requests on
32499, and Plex tokens do not grant Web-administration access.

Do not expose TCP 32499 to the public Internet merely because authorization is
enabled. Before enabling WAN forwarding, validate an actual current managed
user token locally and confirm that Movies-only, TV-only, and no-access grants
produce the expected filtered libraries and 404 responses for unauthorized
items. Keep the identity sidecar's administrative port private.

### Optional remote Plex access

Remote access uses the same Plex-managed TLS identity and Plex-facing
authorization as LAN access. It is optional and should be enabled only after
owner and managed-user entitlement tests pass locally.

Forward one router port only:

```text
WAN TCP 32499 -> <WONKVOD_LAN_IP> TCP 32499
```

Do not forward the Web administration port 32501. Do not enable UPnP or
NAT-PMP merely for WonkVOD, and do not forward the identity sidecar's
administrative port.

Configure the identity sidecar with both raw HTTP facade addresses; Plex Cloud
derives the corresponding trusted `plex.direct` HTTPS connections:

```yaml
services:
  plex-identity:
    environment:
      ADVERTISE_IP: "http://<WONKVOD_LAN_IP>:32499/,http://<RESIDENTIAL_PUBLIC_IP>:32499/"
```

Use the residential WAN address, not the provider VPN address. Configure PMS
remote mapping through its supported preferences API with publication and
manual mapping enabled and port `32499`. A successful resource listing should
contain both a secure local connection and a secure `local=false` connection
on port 32499. Plex Relay is not required for this reference layout.

Provider API, artwork, and media egress remain separate: their dedicated HTTP
transport continues through Gluetun, while the residential address is used
only for incoming Plex-client traffic. A dynamic residential address requires
updating the second `ADVERTISE_IP` value and recreating only the identity
sidecar. Revalidate the router forward, cloud resource listing, trusted TLS,
and authorization whenever that address changes.

## Manual sidecar provisioning (WonkVOD v1.0)

This is the supported v1.0 procedure for a new Linux/Docker installation. It
creates a small official Plex Media Server whose only jobs are identity, Plex
cloud presence, managed-home entitlements, and certificate renewal. Do not add
the Xtream catalog to this PMS: WonkVOD exposes that catalog virtually.

The commands use `/opt/plex-identity`, host admin port `33400`, facade port
`32499`, and documentation-only address `192.0.2.10`. The paths and admin bind
are examples; the container name, persistent `/config`, advertised facade URI,
and shared Docker network are the important contracts.

### 1. Create persistent state and control media

Create the PMS configuration tree and two small media roots:

```sh
sudo install -d -m 0755 /opt/plex-identity/config
sudo install -d -m 0755 /opt/plex-identity/control-media/movies
sudo install -d -m 0755 \
  '/opt/plex-identity/control-media/tv/WonkVOD Control/Season 01'
```

Supply one harmless, short MP4 that Plex can scan. It is only an entitlement
anchor; it is not served as part of the virtual catalog. Copy the same fixture
into the Movie and TV layouts:

```sh
sudo install -m 0644 /path/to/short-control.mp4 \
  '/opt/plex-identity/control-media/movies/WonkVOD Control.mp4'
sudo install -m 0644 /path/to/short-control.mp4 \
  '/opt/plex-identity/control-media/tv/WonkVOD Control/Season 01/WonkVOD Control - S01E01.mp4'
```

The official image changes ownership of `/config` on first start by default;
do not pre-emptively invent a host UID/GID. The read-only fixture files merely
need to be readable by the container. The `/config` filesystem must support
normal file locking; do not place the PMS database on an SMB/NFS mount that
does not provide it.

### 2. Create the sidecar definition

Install the tracked reference Compose file outside the checkout so the sidecar
has an independent lifecycle:

```sh
sudo install -m 0644 deploy/plex-identity.compose.yml.example \
  /opt/plex-identity/compose.yml
```

Obtain a fresh short-lived token from <https://www.plex.tv/claim>, then create a
protected environment file. `PLEX_CLAIM_TOKEN` below is a placeholder, not a
literal value:

```sh
sudo sh -c 'umask 077; cat > /opt/plex-identity/plex-identity.env' <<'EOF'
TZ=Etc/UTC
PLEX_CLAIM=PLEX_CLAIM_TOKEN
PLEX_IDENTITY_ADVERTISE_IP=http://192.0.2.10:32499/
PLEX_IDENTITY_CONFIG_ROOT=/opt/plex-identity/config
PLEX_IDENTITY_CONTROL_MEDIA_ROOT=/opt/plex-identity/control-media
PLEX_IDENTITY_ADMIN_BIND_IP=127.0.0.1
PLEX_IDENTITY_ADMIN_PORT=33400
EOF
```

Replace the claim placeholder immediately before starting. Replace
`192.0.2.10` with the LAN address clients use to reach this Docker host.
`ADVERTISE_IP` deliberately names the future WonkVOD facade, not PMS port
`33400`. A trailing slash is recommended. Binding the PMS admin port to
loopback is safest; use an SSH tunnel when administering it remotely.

Start only the sidecar project:

```sh
cd /opt/plex-identity
sudo docker compose --project-name plex-identity \
  --env-file plex-identity.env up -d
sudo docker compose --project-name plex-identity ps
```

Using the fixed project name creates the `plex-identity_default` network that
the WonkVOD reference deployment joins. The Compose `hostname` seeds the server
name. During setup, confirm the Plex server Friendly Name is exactly
`WonkVOD`; if Plex presents another initial name, change it under the server's
**Settings > General** without recreating the server.

### 3. Complete and verify the claim

On the Docker host, open `http://127.0.0.1:33400/web`. From another computer,
first create a tunnel and then open that same browser URL locally:

```sh
ssh -N -L 33400:127.0.0.1:33400 operator@192.0.2.10
```

Finish the normal Plex first-run pages. The claim token is temporary and
secret: never commit it, paste it into an issue, or retain it after the claim.
The persisted Preferences file is:

```sh
PMS_DATA='/opt/plex-identity/config/Library/Application Support/Plex Media Server'
PMS_PREFS="${PMS_DATA}/Preferences.xml"
```

Verify the file contains both a claimed account token and a stable processed
machine identity without printing the account token:

```sh
sudo test -s "${PMS_PREFS}"
sudo perl -0777 -ne 'exit(/PlexOnlineToken="[^"]+"/ ? 0 : 1)' "${PMS_PREFS}" \
  && echo 'claim: complete'
MACHINE_IDENTIFIER=$(sudo sed -n \
  's/.*ProcessedMachineIdentifier="\([^"]*\)".*/\1/p' "${PMS_PREFS}")
test -n "${MACHINE_IDENTIFIER}" && printf 'machine identifier: %s\n' "${MACHINE_IDENTIFIER}"
```

The printed identifier is installation-specific; refer to it as
`MACHINE_IDENTIFIER` in notes or examples rather than publishing its value.

After claim success, delete the `PLEX_CLAIM=` line (or leave it empty) in the
protected environment file and apply the definition once more. This removes
the usable short-lived value from the container configuration while preserving
the claimed `/config` state:

```sh
sudo sed -i '/^PLEX_CLAIM=/d' /opt/plex-identity/plex-identity.env
cd /opt/plex-identity
sudo docker compose --project-name plex-identity \
  --env-file plex-identity.env up -d
```

### 4. Wait for the Plex certificate and obtain the secure URI

PMS obtains its `plex.direct` certificate asynchronously. Wait for the
persisted PKCS#12 file; do not delete or reclaim the server if it is not
immediate:

```sh
PMS_DATA='/opt/plex-identity/config/Library/Application Support/Plex Media Server'
until sudo test -s "${PMS_DATA}/Cache/cert-v2.p12"; do
  echo 'waiting for Plex certificate'
  sleep 10
done
echo 'certificate: present'
```

Next, query Plex's resource listing using the account token already stored in
Preferences. The token remains in a shell variable and is not printed. This
script prints only the advertised HTTPS connections for this machine:

```sh
PMS_PREFS="${PMS_DATA}/Preferences.xml"
MACHINE_IDENTIFIER=$(sudo sed -n \
  's/.*ProcessedMachineIdentifier="\([^"]*\)".*/\1/p' "${PMS_PREFS}")
PMS_TOKEN=$(sudo sed -n 's/.*PlexOnlineToken="\([^"]*\)".*/\1/p' "${PMS_PREFS}")
RESOURCE_XML=$(mktemp)
chmod 0600 "${RESOURCE_XML}"
curl --fail --silent --show-error \
  -H "X-Plex-Token: ${PMS_TOKEN}" \
  'https://plex.tv/api/resources?includeHttps=1' > "${RESOURCE_XML}"
TARGET_MACHINE="${MACHINE_IDENTIFIER}" perl -0777 -ne '
  while (m{<Device\b([^>]*)>(.*?)</Device>}sg) {
    my ($attrs, $body) = ($1, $2);
    next unless $attrs =~ /clientIdentifier="\Q$ENV{TARGET_MACHINE}\E"/;
    while ($body =~ /<Connection\b([^>]*)\/?\s*>/sg) {
      my $connection = $1;
      my ($protocol) = $connection =~ /protocol="([^"]+)"/;
      my ($uri) = $connection =~ /uri="([^"]+)"/;
      print "$uri\n" if ($protocol // "") eq "https";
    }
  }
' "${RESOURCE_XML}"
unset PMS_TOKEN
rm -f "${RESOURCE_XML}"
```

Select the URI whose port is `32499` and whose hostname resolves to the Docker
host's LAN address. A sanitized shape is
`https://192-0-2-10.example.plex.direct:32499`. Record the full URI as
`WONKVOD_SECURE_URI` and its hostname alone as `PLEX_TLS_HOSTNAME`.

If the certificate or connection is still absent after several minutes:

1. confirm `docker logs plex-identity` shows a claimed, normally running PMS;
2. confirm outbound DNS/HTTPS access to Plex services;
3. confirm `ADVERTISE_IP` contains the reachable host address and port `32499`;
4. confirm the persisted Preferences contains the same machine identifier; and
5. wait and query again.

Do not delete `/config`, request a new identity, or repeatedly reclaim PMS as a
routine certificate fix.

### 5. Create the two control libraries

In Plex Web for the `WonkVOD` server:

1. Open **Settings**, select the `WonkVOD` server, then open **Manage >
   Libraries**.
2. Choose **Add Library**, select **Movies**, name it exactly `WonkVOD Movies`,
   and add `/control-media/movies` as its folder.
3. Choose **Add Library** again, select **TV Shows**, name it exactly
   `WonkVOD TV Shows`, and add `/control-media/tv` as its folder.
4. Let both scans finish and confirm each library contains its one control
   item. Do not add provider media or the WonkVOD catalog to either library.

These two real PMS sections are identity and entitlement anchors. Their section
records let Plex Home grant access to the corresponding virtual WonkVOD Movie
and TV sections; the fixture bytes themselves are not the catalog.

### 6. Grant Plex Home access

In Plex Web, open **Settings > Plex Home**, select a managed user, edit the
user's library access, select the `WonkVOD` server, and grant both:

- `WonkVOD Movies`
- `WonkVOD TV Shows`

Save the change for every managed user who should browse WonkVOD. This step is
required even though WonkVOD—not the control PMS—serves the virtual catalog.
Allow signed-in clients time to refresh their cached server resources.

### 7. Map sidecar values into `deploy/.env`

Copy `deploy/.env.example` to the ignored `deploy/.env`, then populate it using
the following map:

| Variable | Source | Required / example |
|---|---|---|
| `WONKVOD_APP_VERSION` | Release being deployed | Keep `1.0.0` for this release. |
| `WONKVOD_SECURE_URI` | HTTPS URI from the resource query | Required; e.g. `https://192-0-2-10.example.plex.direct:32499`. |
| `PLEX_TLS_HOSTNAME` | Hostname portion of that URI | Required; e.g. `192-0-2-10.example.plex.direct`. |
| `WONKVOD_MACHINE_IDENTIFIER` | `ProcessedMachineIdentifier` printed above | Required. |
| `WONKVOD_PLEX_PORT` | Facade port in `ADVERTISE_IP` | Required; reference value `32499`. |
| `WONKVOD_WEB_BIND_IP` | Host interface for Web Setup | Recommended default `127.0.0.1`; use a trusted LAN address only intentionally. |
| `WONKVOD_WEB_PORT` | Web Setup host port | Reference value `32501`. |
| `WONKVOD_STATE_DIR` | Writable WonkVOD state created during installation | Required; reference `/opt/wonkvod-runtime/state`. |
| `WONKVOD_PROVIDER_ENV_FILE` | Protected Xtream bootstrap file | Required; reference `/opt/wonkvod-runtime/xtream.env`. |
| `PLEX_IDENTITY_CONFIG_DIR` | PMS data directory below the `/config` bind | Required; reference `/opt/plex-identity/config/Library/Application Support/Plex Media Server`. |
| `PLEX_IDENTITY_CONTROL_MEDIA` | Existing harmless control MP4 | Required by the v1 Compose bind; e.g. `/opt/plex-identity/control-media/movies/WonkVOD Control.mp4`. |
| `PLEX_IDENTITY_NETWORK` | Network created by the fixed sidecar project name | Required; `plex-identity_default`. |
| `PLEX_IDENTITY_UID` / `PLEX_IDENTITY_GID` | Numeric owner of `Preferences.xml` and `cert-v2.p12` (`stat -c '%u:%g'`) | Required so the non-root TLS process can read both; commonly `1000`/`1000`, but inspect rather than assume. |
| `WONKVOD_PROFILE_REQUESTS` | Optional request timing diagnostics | Keep `0` normally. |

Keep this file mode `0600`. It must not contain a Plex account or claim token.

### 8. Preflight and deploy

Run these checks before starting WonkVOD:

```sh
PMS_DATA='/opt/plex-identity/config/Library/Application Support/Plex Media Server'
PMS_PREFS="${PMS_DATA}/Preferences.xml"

docker inspect --format '{{.State.Status}}' plex-identity
curl --fail --silent --show-error http://127.0.0.1:33400/identity >/dev/null
sudo test -s "${PMS_PREFS}"
sudo test -s "${PMS_DATA}/Cache/cert-v2.p12"
sudo perl -0777 -ne 'exit(/PlexOnlineToken="[^"]+"/ ? 0 : 1)' "${PMS_PREFS}"
sudo sed -n 's/.*ProcessedMachineIdentifier="\([^"]*\)".*/machine: \1/p' "${PMS_PREFS}"

PMS_TOKEN=$(sudo sed -n 's/.*PlexOnlineToken="\([^"]*\)".*/\1/p' "${PMS_PREFS}")
LIBRARY_XML=$(mktemp)
chmod 0600 "${LIBRARY_XML}"
curl --fail --silent --show-error \
  -H "X-Plex-Token: ${PMS_TOKEN}" \
  http://127.0.0.1:33400/library/sections > "${LIBRARY_XML}"
perl -0777 -ne '
  while (/<Directory\b([^>]*)>/sg) {
    my $attrs = $1;
    $movies = 1 if $attrs =~ /title="WonkVOD Movies"/ && $attrs =~ /type="movie"/;
    $shows = 1 if $attrs =~ /title="WonkVOD TV Shows"/ && $attrs =~ /type="show"/;
  }
  END { exit(($movies && $shows) ? 0 : 1) }
' "${LIBRARY_XML}" && echo 'control libraries: present'
unset PMS_TOKEN
rm -f "${LIBRARY_XML}"
```

In Plex Web, confirm the Friendly Name and the two one-item control libraries.
Confirm the resource-query output supplied the `32499` HTTPS URI, then return
to the WonkVOD checkout:

```sh
cd /opt/wonkvod
./deploy/deploy.sh config
./deploy/deploy.sh deploy
set -a; . deploy/.env; set +a
curl --fail --silent --show-error "${WONKVOD_SECURE_URI}/identity" >/dev/null
```

Continue with the one-time Web Setup token, provider configuration, category
selection, first refresh, and client validation in the main README.

## Setup state model

The intended orchestration state machine is:

| State | Meaning | Normal next transition |
|---|---|---|
| `not_installed` | No sidecar definition or persistent configuration exists. | Initialize sidecar files. |
| `container_present` | Definition/config exists but PMS is not yet answering. | Start PMS and wait. |
| `unclaimed` | PMS answers but is not owned by a Plex account. | Supply a fresh claim token and trigger claim. |
| `claiming` | A bounded claim operation is in progress. | `waiting_for_identity` or `error`. |
| `waiting_for_identity` | PMS is starting or a claimed machine identifier is not yet observable. | `certificate_pending`. |
| `certificate_pending` | Identity exists but the advertised secure endpoint does not yet validate. | `control_libraries_pending`. |
| `control_libraries_pending` | Identity/TLS are ready but required entitlement libraries are missing. | Create/verify the two control libraries. |
| `cloud_registration_pending` | Local identity is ready but Plex cloud presence/connection is not confirmed. | `ready`. |
| `ready` | Claimed identity and WonkVOD secure endpoint are usable. | Remain ready; monitor certificate renewal. |
| `error` | A bounded operation failed with a sanitized diagnostic. | Retry the failed transition without deleting known-good state. |

Recovery must be idempotent. A retry may start or inspect an existing sidecar,
but must never delete persistent PMS state, silently create a new machine
identity, or reclaim an already claimed server.

## Implemented safe seam

`GET /api/plex/setup` is a read-only status endpoint. It currently derives
`waiting_for_identity`, `unclaimed`, `certificate_pending`, or `ready` from the
existing identity endpoint, claimed state, machine identifier, and validated
TLS endpoint. `GET /api/plex/status` remains a compatible alias with the same
fields.

The response declares `setup_mode=externally_managed_sidecar`, reports that
control-library verification is manual, and sets
`setup_actions_available=false`. The Plex UI shows **Ready** for an existing
claimed identity with valid TLS and does not render destructive setup controls.

The current backend intentionally has neither the Docker socket nor a Plex
account token. It therefore cannot safely distinguish every pre-install state,
inspect cloud registration, or enumerate protected PMS libraries by itself.

## Future API and orchestration seams

Future implementation can extend the state response and add narrowly scoped,
authenticated actions for:

1. initializing an empty sidecar configuration directory and Compose fragment;
2. accepting a short-lived claim token without echoing or persisting it beyond
   the claim operation;
3. starting a bounded claim and observing its transition;
4. verifying the generated machine identifier without changing it;
5. waiting for and validating certificate readiness;
6. creating or verifying the two tiny control libraries through supported PMS
   APIs;
7. verifying sanitized cloud presence and advertised connections.

These operations require the existing Web administrator authentication plus a
narrowly privileged host-side orchestrator. The current authenticated backend
does not have those host privileges, and Plex account tokens must not be sent
back to browser JavaScript.
A real Plex OAuth flow has not been implemented; until one exists, claim-token
acquisition remains a clearly marked manual step.

## Intentionally not automated

This milestone does not initialize, recreate, restart, reclaim, or mutate the
working sidecar. It does not regenerate certificates, alter cloud resources,
change the machine identifier, recreate control libraries, or edit Plex Home
permissions. End-to-end claim automation requires a disposable identity and an
authenticated orchestration boundary before it can be validated safely.

