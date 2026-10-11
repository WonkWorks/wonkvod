# Legacy WonkVOD architecture

The retired WonkVOD architecture projected media through FUSE and relied on Plex ingestion and reconciliation. It was superseded by the current virtual Plex/API adapter architecture, which does not require the legacy runtime. The legacy runtime was retired from the live server on October 11, 2026.

The final legacy source is commit `9dfe3f6bcbe13b832ea52e986b5d9c31ea20eab2`, tagged `legacy-wonkvod-final-2026-10-10`. Its complete Git history is preserved in `/opt/wonkvod-legacy-archive.bundle`.

For historical archaeology, clone the standalone bundle into a separate working directory. The legacy implementation is intentionally not a dependency of current WonkVOD.
