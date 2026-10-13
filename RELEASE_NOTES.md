# WonkVOD 1.0.1 Release Notes

WonkVOD 1.0.1 adds secure remote Plex access and hardens both Plex-client
authorization and provider networking.

Plex clients now authenticate virtual catalog and media requests with their
normal Plex tokens. WonkVOD validates those tokens through the claimed identity
sidecar, and Movies/TV visibility follows the user's control-library
entitlements. Missing or unauthorized access is rejected before provider media
is contacted.

Remote use is supported through a Plex-managed, secure direct connection on
TCP 32499. The reference deployment publishes both local and remote
`plex.direct` connections while keeping Web Setup private. Remote owner access
was validated through Plex Cloud, including physical iPhone playback over
cellular/5G.

Provider API, artwork, and media traffic can optionally use a dedicated HTTP
egress proxy such as Gluetun. Plex-client ingress and identity traffic remain
direct. When a provider proxy is configured but unavailable, provider traffic
fails closed instead of falling back to the host connection.

Web Setup authentication remains separate from Plex-client authorization and
should be exposed only on loopback or a trusted network. WonkVOD still does not
transcode or remux, and sidecar provisioning and Plex Home grants remain manual.
