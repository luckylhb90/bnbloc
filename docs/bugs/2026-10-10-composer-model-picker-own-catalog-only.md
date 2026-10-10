# Composer model picker uses only the caller's own agent catalogs (2026-10-10)

Status: fixed (activity + web), uncommitted; pending deploy.

## Repro

1. A super admin publishes a platform agent (`claude-code` under core
   `acme`); a browser-only member adds it to a room and picks it in the
   composer. No model picker appears, although the host's bridge advertises
   a model catalog for that agent.
2. A browser-only member picks another member's agent to borrow: no model
   picker.
3. Worse: the caller also runs an agent with the same (core, provider) as the
   picked recipient (e.g. both have `acme/codex`). The picker shows the
   CALLER's catalog for someone else's agent, so a model the owner's core
   does not offer can be sent.

## Root cause

`web/src/views/Composer.tsx` `effTarget` resolved a picked recipient's model
catalog by matching `agent.targets` (the caller's local desktop targets) or
`bridgeCatalogs` (the caller's own `GET /v1/my/agents` advertisement) on
`(core, provider)` only — never by owner. The only catalog source the web
had was the caller's own: the room roster (`wire.RosterAgent`) carries no
catalog, and platform-agent hosts do not advertise into the room roster at
all. The catalogs exist server-side — every bridge uploads per-agent
`modelOptions` with its heartbeat (`store.BridgeAgent.ModelOptionsJSON`) —
but no room-scoped read exposed another member's.

## Fix

One owner-keyed seam:

- activity serves `GET /v1/rooms/{roomId}/agent-catalogs`
  (`wire.RoomAgentCatalogsReply`): the live bridge-advertised agents, with
  their `modelOptions`, of the room's members, for room members only. An
  agent-host member (a platform agent's host) contributes ONLY the agents it
  publishes as platform agents AND that are added to this room (a live
  platform grant still published by the policy) — never the host's other
  agents. The availability rule is the one shareagent applies, extracted to
  `internal/shareagent.RoomPlatformGrants` and used by both services.
- web: `resolveModelCatalog` (`web/src/lib/agentCatalogs.ts`) resolves the
  catalog by `(owner, core, provider)`: own agents use the local desktop
  target first, then the room catalog; anyone else's agent uses only the room
  catalog, narrowed to an owner-issued grant's `allowedModels` when the owner
  restricted them (platform grants are unrestricted). The owner-less matching
  and the composer's private `GET /v1/my/agents` fetch are gone.
- The catalog shape is now one wire type, `wire.AgentModelOptions`, used by
  both `GET /v1/rooms/{roomId}/agent-catalogs` and `GET /v1/my/agents`
  (same JSON keys as before; a malformed advertisement is omitted).

## Verification

- Go: `services/activity/agent_catalogs_test.go` — owner-keyed rows (two
  members with the same core/provider keep their own catalogs; the newest
  advertisement of an owner's agent wins), membership (403 non-member, 401
  anonymous), agent-host scoping (only the added + published platform agent;
  a revoked platform grant, an unpublished agent with a grant, an
  owner-issued grant of the host's agent, and a never-granted agent are all
  withheld), policy disabled or unreadable and missing stores fail closed
  for hosts only. `internal/shareagent/platform_test.go` pins the shared
  availability rule; shareagent's platform-agent tests still pass over it.
- Web: `web/src/lib/agentCatalogs.test.ts` — no fallback to the caller's
  catalog for another owner's agent, local target first for own agents,
  grant narrowing (order kept, unauthorized default dropped, per-model
  reasoning filtered, empty list unrestricted), platform agents unrestricted.
