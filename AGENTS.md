# PortPilot Engineering Contract

- `cmd/portpilot-server` and `cmd/portpilot-client` are the only production and development process entrypoints.
- Full-service tests must start services through `make dev`. Test fixtures may provide targets, never alternate PortPilot implementations.
- Proxy configuration files are the source of truth. SQLite must remain disposable.
- The Go server must not implement TLS. TLS terminates at the deployment Nginx boundary.
- Control traffic remains outside smux. TCP and UDP-over-TCP data streams require session-bound one-time tickets.
- Do not add Docker or Compose runtime targets. The Makefile may only build the three release images.
- Before delivery, run the gates relevant to the change. Never lower lint or test requirements to make a change pass.
