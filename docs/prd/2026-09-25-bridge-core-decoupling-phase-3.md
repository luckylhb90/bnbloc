# Bridge core decoupling Phase 3

Status: implemented (d749d5d); the built-in provider it mentions was later removed — see `docs/prd/2026-09-30-no-builtin-cores.md`.

## Intent

Remove core-name special cases from the bridge drive path so each local-agent
core is driven through capabilities and its configured endpoint, not by naming a
specific built-in core in bridge code.

## Acceptance

- Target-id resolution uses an optional core provider capability before falling
  back to live target listing.
- The existing built-in provider still resolves provider names to its native
  `local:<provider>` target ids.
- Session-scoped REST reads use the bound session core endpoint and never fall
  back to another core.
- Bridge startup no longer reads local-agent fallback env vars or runs a
  core-specific discovery branch outside normal core wiring.
- `go build ./...`, `go build -tags internal ./...`, `go vet ./services/bridge/... ./core/...`,
  and `go test ./services/bridge/... ./core/...` pass.

## Status

Implemented in this change.
