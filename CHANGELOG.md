# Changelog

## [Unreleased]

## 1.0.1 - 2026-10-13

### Added

- Plex-token authorization for virtual catalog and media routes.
- Managed-user entitlement enforcement through the sidecar control libraries.
- Secure remote Plex access through a published `local=false` connection.
- Optional provider-specific HTTP proxy support and a Gluetun reference
  configuration.

### Security

- Unauthenticated library and media access is now rejected.
- Unauthorized item and media requests fail before provider access.
- Provider egress can fail closed through a configured proxy.
- Web Setup remains restricted to loopback or a trusted network and must not
  be exposed publicly.

### Validation

- Owner and managed-user Plex tokens, including partial library entitlements.
- External unauthenticated probes and authenticated remote-owner access.
- Physical iPhone access over cellular/5G.
- Provider egress through a Surfshark Dedicated IP using Gluetun.

## [1.0.0] - 2026-10-11

### Added

- Source-neutral canonical Movie/TV catalog with stable IDs, multi-category
  membership, immutable read indexes, and atomic persistence.
- Incremental refresh, explicit Full Rebuild, impact preview, quarantine, and
  bounded provider-friendly retry.
- Plex-compatible libraries, paging, metadata, artwork, queues, decisions,
  byte-range streaming, and lazy MediaFacts.
- Indexed Movie/Show title search through PMS-compatible hub, universal,
  section-local, and legacy search responses.
- Claimed PMS identity-sidecar and in-memory certificate TLS architecture.
- Web Setup for provider, categories, health, refresh, quarantine, and settings.
- Single-admin Argon2id authentication, local one-time bootstrap, expiring
  sessions, CSRF, login throttling, and password rotation.
- Version/revision exposure and generic ignored local deployment configuration.
- MIT License.

### Validated

- Plex Roku, HTPC, Mac, and iOS clients with Direct Play-compatible media.
- Catalog scale near 20,000 movies, 2,500 shows, 6,000 seasons, and more than
  90,000 episodes.

### Limitations

- No transcoding or remuxing.
- Sidecar provisioning and Plex Home grants remain manual.
