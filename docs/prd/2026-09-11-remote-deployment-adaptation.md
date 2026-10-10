# Design — Remote-deployment adaptation for Surface 1 (agent aggregation)

Status: **done**. The local-agent value prop is coherent when the gateway is
deployed REMOTELY (cloud) and each user runs their agent daemon + bridge on
their own machine. Local single-host keeps working unchanged. Durable design:
[Room agent roster and remote deployment](../architecture/room-agent-roster.md).

## Verified facts (evidence-based)

- **Path B (chat-drives-agent) is already remote-capable.** The bridge is fully
  OUTBOUND: it dials `GatewayBaseURL`, logs in, subscribes to room SSE, claims
  chat for its owner, drives its LOCAL daemon over loopback, and posts replies
  back via `/v1/rooms/{room}/agent-activity/*`. A user pointing their desktop
  bridge (`gatewayUrl`, configurable in BridgePanel) at a remote gateway gets a
  working agent loop with no inbound-to-laptop connection. Multi-user isolation
  already exists (`serveRecipient` claims only `rec.OwnerID == bridge owner`;
  borrow via room authorization).
- **Path A ("Run a local agent" button) is structurally single-host only.**
  `POST /v1/local-agent/run|targets|discover` make the GATEWAY dial the daemon;
  `validateLocalAgentURL` blocks non-loopback. Remote → 403/502. Must be gated.
- **relay/device-link are NOT the agent tunnel** (file-sync + mobile pairing
  only). No reverse tunnel needs building; outbound HTTPS+SSE already is it.
- **The composer agent picker is wrong for remote.** `RoomView` builds
  `roomAgents` from MY local `agent.targets` and PROJECTS my providers onto every
  other member (`others.flatMap(o => myPairs)`). This is a single-host hack:
  remotely I have 0 local targets (gateway can't reach my laptop) and other
  members' real agents are invisible unless they hold a shared-agents grant.

## Changes (smallest coherent set)

### R1 — Room agent roster (fixes reachability, replaces the projection hack)
The bridge knows exactly which `(owner, core, provider)` agents it serves in a
room. Add a roster advertisement over the EXISTING agent-activity channel:
- Bridge: on room reconcile/first-serve, `POST
  /v1/rooms/{room}/agent-activity/roster` with `{ownerUserId, agents:[{core,
  provider}]}`. Re-advertised on reconnect; the activity store keeps the latest
  per (room, owner). (Mirrors `postSessionState`'s owner+version pattern.)
- Activity: store the per-owner roster for a room; publish a transient
  `agent_roster` event through the hub (like `session-state`).
- Gateway/room read path: `GET /v1/rooms/{room}/agents` returns the union of
  advertised rosters (who can actually be driven here right now).
- Web: `RoomView` builds `roomAgents` from this roster (+ merge shared-agents
  grants for borrow), NOT from projecting my local targets. `useLocalAgent`
  targets stay only for the RunAgentDialog local/desktop path.
- Local single-host: the same bridge advertises its roster, so behavior is
  identical; the projection hack is removed.

### R2 — Detect remote gateway + gate Path A
- `useLocalAgent`/config expose `isRemoteGateway`: true when the gateway is NOT
  same-origin-local AND no directly-connectable core is available
  (`/v1/cores` all unavailable AND not in desktop shell). Concretely: desktop
  shell → local; browser against a non-localhost origin → remote.
- RunAgentDialog + RoomView `startAgent`/"Run local agent" CTA: when remote,
  hide/disable Path A and show guidance — "Run the bnbloc desktop app on your
  machine and point it at this gateway; then message @your-agent in a room."
- The "Run local agent" button in MyRooms becomes secondary/guarded in remote.

### R3 — Desktop remote auth
- Enterprise remote gateways use IdP; `idpToken: owner` login fails. Desktop
  settings + BridgePanel gain an optional Gateway token field →
  `BNBLOC_GATEWAY_TOKEN` (bridge already supports a static token). Empty keeps
  the dev/local login path.

## Non-goals
- Reverse tunnel / relay for agents (unnecessary; outbound already works).
- Making Path A work remotely (fundamentally requires inbound-to-laptop; use
  the desktop bridge + chat flow instead).

## Verification
- Roster: a bridge advertising `(owner, core, provider)` makes the composer
  picker show that agent WITHOUT the gateway reaching the daemon; two owners'
  rosters stay distinct.
- Remote gate: with a simulated remote gateway (all cores unavailable, not
  desktop), Path A is hidden and chat-drive guidance shows; Path B still works.
- Local unchanged: single-host still lists targets and runs agents as before.
- Windows: no path/proc changes; env + HTTP only.
