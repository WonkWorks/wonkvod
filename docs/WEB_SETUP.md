# WonkVOD Web Setup v0

The setup UI is served on the trusted LAN at `http://192.0.2.22:32501/`. Version 0 has no authentication and must not be exposed to the public internet. All web and setup API routes pass through one middleware boundary so session authentication can be added later.

Mutable settings are stored atomically in `/opt/wonkvod-runtime/state/config.json` (mounted as `/var/lib/wonkvod/config.json`). Environment values bootstrap the file on first start; subsequent UI changes never edit Compose. Provider passwords are stored with the configuration file at mode `0600`, are never returned by GET APIs, and remain unchanged when an update omits the password.

Applying library selections saves desired configuration but does not clear the active catalog. Use **Refresh Now** to build and atomically promote a replacement snapshot. A failed refresh retains the last usable catalog. With a valid snapshot, startup becomes healthy immediately and refresh continues asynchronously; a cold start remains unhealthy until its first catalog succeeds.

The Categories page can preview a proposed selection using bulk lists only. It
reports estimated additions, removals, membership-only changes, and overlaps
without fetching item details. More than 100 new movies/shows is presented as a
large expansion with an optional concurrency suggestion.

The saved refresh concurrency remains the steady-state default (`2` by
default). Refresh Now, category expansion, and Full Rebuild may use a validated
one-time override without changing that setting. Scheduled refreshes always use
the saved value. Larger temporary values can shorten a bootstrap but may also
trigger provider limits.

The Quarantine page shows sanitized per-item failure diagnostics and only
offers retry for recoverable classifications. Retry runs the ordinary atomic
incremental candidate/promotion path at concurrency `1`; it never patches the
live catalog, and failure preserves the previous snapshot. Normal incremental
refresh should be cheap. Category bootstrap may be long because new items need
detail enrichment, while Full Rebuild deliberately re-fetches every selected
item.

Plex integration in this milestone is read-only status. Automated Plex identity creation, claiming, cloud registration, and certificate provisioning remain a separate design phase.
