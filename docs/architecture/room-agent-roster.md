# Room Agent Roster and Remote Deployment

How members discover which agents can be driven in a room, and why this works
identically whether the gateway is local or deployed remotely.

## Two agent paths

bnbloc drives agents two ways, with different remote behavior:

- **Path A — "Run a local agent" (`POST /v1/local-agent/run|targets|discover`).**
  The GATEWAY dials the caller's local daemon. `validateLocalAgentURL` restricts
  the target to loopback / `host.docker.internal`. This is **single-host only**:
  a remote (cloud) gateway cannot reach a user's laptop, so these endpoints
  return empty / 403 / 404. The web hides Path A when it detects a remote
  gateway (see below).
- **Path B — chat drives an agent (the Bridge).** The user's Bridge dials
  OUTBOUND to the gateway (login, room SSE subscription, activity posts) and
  drives its own local daemon over loopback. The gateway never dials the laptop.
  Path B is the remote-safe path and the product's primary remote story: run the
  desktop app locally, point its `gatewayUrl` at the remote gateway, and
  `@`-message an agent in a room.

`bnbloc-relay` / `device-link` are NOT the agent path — relay carries only
peer-to-peer file sync (`gosync` in mutagen mode) and mobile Trickle ICE
rendezvous.

## Room agent roster

The composer's agent picker must show the agents that can actually be driven in
a room. The gateway cannot list a remote user's agents (it cannot reach their
machine), so the source of truth is a **roster the Bridge advertises**, not a
projection of the caller's own local targets.

Data flow:

1. **Advertise.** When a Bridge subscribes to a room's event stream it appends
   `?agents=<core>/<provider>,…` (the `(core, provider)` pairs it can drive,
   from each core's `ListTargets`). See `services/bridge` `consumeRoom` /
   `advertiseParam`.
2. **Track.** `bnbloc-realtime` registers the advertisement against the
   authenticated subject in an in-memory `AgentRoster`
   (`internal/realtime/roster.go`). Liveness mirrors `Presence`: refcounted per
   `(room, owner)` with a leave-grace window, so a Bridge reconnect does not
   churn and its agents are withdrawn shortly after it disconnects. A member can
   only advertise its own agents (owner = authenticated subject).
3. **Fan out.** A change publishes a transient `agent_roster` event over the
   room lane. Late subscribers reconcile via the read endpoint.
4. **Read.** `GET /v1/rooms/{roomId}/agents` returns the union of advertised
   rosters (membership-checked, fail closed). The gateway routes it to
   `bnbloc-realtime`.
5. **Consume.** The web builds the composer's selectable agents from this roster
   (`api.roomAgents` + the live `agent_roster` event), merged with
   `shared-agents` borrow grants. Own entries are drivable directly; others are
   borrowable (still gated by borrow authorization at drive time).

The roster is transient presence, not durable activity: it is never persisted
and is rebuilt from live advertisements, unlike the agent-activity store.

## Remote gateway detection and gating

`useLocalAgent` exposes `isRemoteGateway`: true when the probe has resolved, the
client is not in the desktop shell, there is no reachable core (`GET /v1/cores`
all unavailable), and no manual local URL is set. The web uses it to hide Path A
("Run local agent") entry points and steer users to the desktop-app + chat flow.

## Runtime overrides

- `BNBLOC_GATEWAY_TOKEN` — a static bearer the Bridge sends on gateway calls
  instead of minting an owner session token via the dev login. Set it when the
  gateway is fronted by an enterprise IdP (SSO/OIDC/SAML) that rejects the dev
  login. The desktop shell surfaces this as the "Gateway token" setting
  (`settings.gateway_token` → `BNBLOC_GATEWAY_TOKEN`). Empty keeps the default
  owner-login path (local/dev).
- The ★ default agent (`settings.default_agent`) is **not** a bridge env: a
  no-provider chat is people-only and drives nothing, so there is no bridge-side
  default-target to honor. The desktop web reads `default_agent` via
  `getSettings()` to pre-select the composer's own-agent recipient.
- `BNBLOC_BRIDGE_DISABLED_PROVIDERS` — comma-joined `core/provider` pairs the
  user turned off in the "Agent sources" panel; the bridge omits them from a
  room's roster (`advertiseParam`) and refuses a chat that still names one
  (`handleChat`), before any borrow side effects.
