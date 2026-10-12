# Changelog

## [Unreleased]

## [1.0.0] - 2026-10-11

### Added

- Source-neutral canonical Movie/TV catalog with stable IDs, multi-category
  membership, immutable read indexes, and atomic persistence.
- Incremental refresh, explicit Full Rebuild, impact preview, quarantine, and
  bounded provider-friendly retry.
- Plex-compatible libraries, paging, metadata, artwork, queues, decisions,
  byte-range streaming, and lazy MediaFacts.
- Claimed PMS identity-sidecar and in-memory certificate TLS architecture.
- Web Setup for provider, categories, health, refresh, quarantine, and settings.
- Single-admin Argon2id authentication, local one-time bootstrap, expiring
  sessions, CSRF, login throttling, and password rotation.
- Version/revision exposure and generic ignored local deployment configuration.

### Validated

- Plex Roku, HTPC, Mac, and iOS clients with Direct Play-compatible media.
- Catalog scale near 20,000 movies, 2,500 shows, 6,000 seasons, and more than
  90,000 episodes.

### Limitations

- No transcoding or remuxing.
- Sidecar provisioning and Plex Home grants remain manual.
- No license is selected; the final `v1.0.0` tag is blocked on that decision.
