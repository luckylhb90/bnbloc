# Core extension pipeline wiring

Status: historical — implemented at the time; the `/v1/platform/core-packages`
routes this doc wires were replaced by the Components API
(`/v1/platform/components`) in the 2026-10-01 clean cutover. The pipeline itself
(`internal/extpipe`) is current.

## Intent

Route the gateway's existing core package install, uninstall, and boot rehydrate
paths through the generic extension pipeline while preserving every existing
HTTP route and response shape.

## Acceptance

- Signed `/v1/platform/core-packages` installs use `extpipe.Pipeline` with the
  existing `corepkg.trustedKeys` trust store.
- Bare-manifest `/v1/cores/install` remains an unsigned super-admin paste path
  and returns `{ "name": ... }`.
- Existing uninstall routes still return `204` and remove the live core entry.
- Gateway boot rehydrates installed core records through the pipeline and keeps
  `resolveCores` extra activation behavior for manifest cores.

## Status

Implemented in this change.
