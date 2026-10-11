# Desktop lists sidecar agents through a private path (404 for protocol cores)

Status: fixed 2026-10-11.

## Repro

Install a sidecar core that speaks only Core Protocol v1 (the ACP core,
`github.com/luckylhb90/bnbloc-acp`). The desktop's Agents page shows it
running with "Couldn't list agents: list targets acp: status 404 — retrying",
and its agents never appear in the desktop's local agent picker, although
the bridge drives them (they are online in `GET /v1/my/agents` and answer in
rooms).

## Root cause

`Adapter::targets` (`apps/desktop/src-tauri/src/internal_ext/sidecar_targets.rs`)
always called `GET {localUrl}/v1/agent-targets`, an endpoint of one earlier
sidecar's own REST dialect, not of any public contract. A Core Protocol
sidecar lists targets at `/core/v1/targets`; a manifest sidecar lists them
wherever its manifest's `listTargets` op says. Only sidecars that happened to
serve the old dialect worked.

## Fix

The desktop lists through the contract the sidecar speaks: a sidecar with a
manifest through that manifest's `listTargets` path, mapped with its
`targets.{list,id,provider}` dotted paths; every other sidecar through Core
Protocol `/core/v1/targets`. Tests pin both.
