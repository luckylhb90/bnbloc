# Architecture

Durable design docs for how bnbloc is built. Point-in-time proposals live in
[`../prd/`](../prd/); this directory holds the standing "how it works / how to
extend it" references.

## Local-Agent Core

- [Adding a local-agent core](core-providers.md) — the `core.Provider` seam, and
  the three ways to add a core (drop-in JSON manifest, native Go provider, future
  sidecar).

## Sandboxes

- [Sandbox Protocol v1](sandbox-protocol.md) — the machine + spaces contract
  for running agent commands in an isolated Linux guest (hybrid execution),
  the `bnbloc.sandbox.package/v1` component kind, and the conformance suite.

## Previewers

- [Previewer components](previewer-components.md) — installable artifact
  viewers (`bnbloc.previewer.package/v1`): package and bundle format, browser
  and desktop serving, the sandboxed frame protocol and SDK, and how to build,
  sign and publish one.

## Rooms and Agents

- [Room agent roster and remote deployment](room-agent-roster.md) — how members
  discover drivable agents in a room (the Bridge-advertised roster), the two
  agent paths (gateway-dials-daemon vs chat-drives-agent), remote-gateway
  detection, and the `BNBLOC_GATEWAY_TOKEN` override.
- [Room tools](room-tools.md) — how an agent lists the room's agents and
  dispatches work to them (`ask_agent`): the bridge's loopback MCP server and
  its per-session bearers, the realtime dispatch route, its authority (the
  parent job's requester) and its guards.

## Local Bridge

- [bnbloc Bridge](../bridge.md) — the sidecar that drives a member's unmodified
  local agent runtime from room chat: how it subscribes, resolves the host daemon, reflows
  replies, surfaces failed turns, and how the desktop app auto-starts it
  zero-config.
