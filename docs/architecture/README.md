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

## Endpoint components

- [Endpoint components](endpoint-components.md) — a notifier or an auth
  provider beside the gateway: its one credential (created at install,
  sealed, rotated), the signed calls it makes, and how it asks for its
  settings (`GET /v1/component-settings`).

## Share targets

- [Share targets](share-targets.md) — a member shares a room, a message or a
  deliverable as a card into a conversation of another app through an
  installed share-target component (`bnbloc.share-target.package/v1`): the
  contract, who builds the card, what the gateway checks and audits.

## Service components

- [Service components](service-components.md) — an endpoint component that
  acts for members and their agents' turns through primitives it declares
  (`bnbloc.service.package/v1`): the attested turn, egress through the
  service (the bridge's proxy and local CA, sandboxed commands through
  `hostProxy`), room facts, cards, pages, callbacks, the sealed store and
  the audit header.

## Rooms and Agents

- [Room agent roster and remote deployment](room-agent-roster.md) — how members
  discover drivable agents in a room (the Bridge-advertised roster), the two
  agent paths (gateway-dials-daemon vs chat-drives-agent), remote-gateway
  detection, and the `BNBLOC_GATEWAY_TOKEN` override.
- [Content components: experts and skill packs](content-components.md) —
  member-authored packages (the shared validator, storage, the one
  visibility function), how a chat's expert and the room's and member's skill
  packs reach an agent session (`agentProfile` or the prompt fallback), the
  safety rules and the HTTP surface.
  MCP tool components (`bnbloc.mcp.package/v1`, platform-published stdio
  binaries or http endpoints that content requires by name) are in the same
  document: distribution to desktops, their settings (platform,
  organization and member values).
- [AI assist](ai-assist.md) — the deployment's assist model behind manual
  entry points (the composer's "enhance prompt", a form's "Fill with AI"):
  the model's settings and client, assist tasks as content resolved by name,
  the context a run reads and the fenced prompt.
- [Decision models](decision-models.md) — calibrated answers (a yes/no
  probability, a choice, a score) through an admin-configured provider:
  `POST /v1/decisions`, `app.ai.decide`, the room relay; the request rules,
  caller auth, budget and audit.
- [Room schedules](schedules.md) — scheduled tasks: recurrence kinds, the
  scheduler's claims and its one run path, and tasks that run when the
  platform changes (platform events recorded once by the gateway).
- [Room tools](room-tools.md) — how an agent lists the room's agents and
  dispatches work to them (`ask_agent`): the bridge's loopback MCP server and
  its per-session bearers, the realtime dispatch route, its authority (the
  parent job's requester) and its guards.
- [Browser component](browser-component.md) — a room agent operates web
  pages in the requester's dedicated Chrome profile: what the platform gives
  it (the relay's facts, the decision rate, the live view, the fence); the
  component itself is in its own repository.
- Components live in their own repositories
  ([PRD](../prd/2026-10-18-components-in-own-repos.md)): `bnbloc-browser`,
  `bnbloc-media-tools`, `bnbloc-local-skills`, `bnbloc-previewers`,
  `bnbloc-acp`, `bnbloc-agent-manager`. They build against the public contract package `component`
  (and `core`, `core/pkgmeta`, `web/previewer-sdk`).

- [Plugin panels](plugin-panels.md) — a plugin's own page on the member's
  desktop: a bundle the package pins, shown in the sandboxed frame, reaching
  its sidecar only through one relay that adds the bearer the desktop keeps.
  The platform names nothing a panel is for.

## Local Bridge

- [bnbloc Bridge](../bridge.md) — the sidecar that drives a member's unmodified
  local agent runtime from room chat: how it subscribes, resolves the host daemon, reflows
  replies, surfaces failed turns, and how the desktop app auto-starts it
  zero-config.
