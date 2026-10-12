# WonkVOD 1.0.0 Release Notes

WonkVOD 1.0.0 is the first release-candidate checkpoint of the direct Plex
facade. It represents Xtream-compatible VOD as virtual Movie and TV libraries
without FUSE, local ingestion, Plex scanners, or Plex database mutation.

Highlights include a source-neutral catalog, stable identities, incremental
atomic refresh, quarantine/retry, indexed full-scale browsing, managed Plex
Home access through an identity sidecar, Plex-managed TLS, and Direct Play
streaming with byte ranges. Roku, HTPC, Mac, and iOS clients are validated for
compatible source media.

Web Setup now has single-admin Argon2id authentication, local one-time
bootstrap, 12-hour in-memory sessions, CSRF protection, rate limiting, and
password rotation. Deployment configuration is generic and image metadata
exposes application version separately from exact Git revision.

WonkVOD does not transcode or remux. Sidecar claiming, control libraries, and
managed-user grants remain manual. No license is selected, so the final
`v1.0.0` tag and public publication remain blocked pending an owner decision.
