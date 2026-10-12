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

