# PortPilot Engineering Contract

- `cmd/portpilot-server` and `cmd/portpilot-client` are the only production and
  development process entrypoints.
- Full-service tests must start the real server, client, and Web through
  `make dev`. Fixtures may provide targets, never alternate PortPilot
  implementations.
- Client and proxy configuration files are the source of truth. SQLite must
  remain disposable and rebuildable from `migrations/`.
- The Go server must not implement TLS. TLS terminates at the deployment Nginx
  boundary.
- Control traffic remains outside smux. TCP and UDP-over-TCP data streams
  require session-bound one-time tickets.
- `td/` is a local, Git-ignored temporary design directory. Production code,
  tests, README, and `docs/` must never reference it.
- Temporary and formal Markdown files use `{number}-{description}.md`. After
  implementation and validation, rewrite stable behavior into `docs/` and
  delete the matching local `td/` file.
- Do not add Docker or Compose runtime targets. The Makefile may only build the
  `portpilot-server`, `portpilot-client`, and `portpilot-web` images.
- Before delivery, run every gate relevant to the change, including `make ci`
  for code, full `make dev` flows for user-visible behavior, and
  `make acceptance` for cross-core release changes. Never lower or bypass a
  gate to make a change pass.
