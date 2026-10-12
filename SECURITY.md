# Security

Do not report credentials or tokens in a public issue. Until a public security
contact exists, contact the repository owner through an already trusted private
channel.

Keep `deploy/.env`, provider environment files, runtime JSON state, Plex
preferences/tokens, certificates, private keys, logs, and backups out of source
control. Web Setup is intended for loopback or a trusted LAN. The plain Plex
backend listener must remain private behind the PMS-certificate TLS terminator.

No open-source license has yet been selected. Review repository history and
rotate any exposed credential before public publication.
