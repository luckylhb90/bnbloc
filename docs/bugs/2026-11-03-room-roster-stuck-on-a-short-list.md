# A room's agent roster stayed on a short list after a core restart (2026-11-03)

Status: fixed (see Fix); verified in tests, not yet on QA.

## Repro

1. A member's desktop runs the `acp` core with 9 agents (opencode, codex,
   claude, and 6 guest `*-sandbox` agents). One bridge process serves them in
   every room.
2. The `acp` sidecar is replaced (a desktop update to a new core version
   restarts the daemon). Twice on 2026-11-03: 0.2.27, then 0.2.28.
3. Afterwards `GET /v1/rooms/{room}/agents` on QA lists different agents for
   the same owner in different rooms: `e2e-sandbox-20261030-0041` has 10
   (opencode, codex and claude missing), `smoke-20261030-065755-c752` has all
   13. Stable for 30+ minutes, through page reloads; the composer's picker in
   the first room cannot offer opencode. The acp core itself lists all 9, and
   `GET /v1/my/agents` shows all of them online.

## What is known

- The bridge advertises one list (`advertiseParam`, `services/bridge/bridge.go`)
  to every room; the difference between rooms is in what realtime keeps.
- Right after a restart the acp core lists only the guest agents (it lists a
  host agent only once its probe answered: bnbloc-acp `registry.targets`).
  The bridge caches any successful `ListTargets` (`refreshTargets`) and, on a
  changed roster, cancels every room stream (`refreshRoster`); the reconcile
  re-subscribes them with the current list. So within seconds each room is
  advertised the short list, then the full one.
- realtime's `AgentRoster` (`internal/realtime/roster.go`) keeps one list per
  (room, owner) and refcounts the owner's streams: each `Advertise` overwrites
  the list, a `Withdraw` only decrements. An older stream's late `Advertise`
  (short) after a newer one's (full) leaves the short list as long as any of
  the owner's streams lives; checked with a test against the current code.
- Nothing corrects it: the bridge re-advertises only when its own list
  changes, and it has held the full list since.

## Root cause

Two defects, both confirmed by tests that fail on the code before the fix:

1. The bridge treats "the core has not finished checking an agent" as "the
   agent is gone": a transient list replaces the cached one and is
   advertised.
2. realtime keeps one list per owner and room, written by whichever stream
   advertised last, not by the streams that are alive.

## Fix

- realtime: `AgentRoster` keeps each subscription's advertisement by its
  handle (`Advertise` returns a `RosterSub`, `Withdraw` takes it); an owner's
  agents in a room are those of their **newest live** subscription, and a
  closed subscription's list stops counting at once (the last one's list is
  kept through the grace window). `TestRosterNewestSubscriptionWinsInEitherOrder`
  (two subscriptions of one owner with different lists, opened and closed in
  each order) fails on the old code for the stuck room's interleaving.
- Core Protocol: an optional capability `pendingTargets`; `GET
  /core/v1/targets` also lists, in `pending`, the ids of agents the core is
  still checking. The bridge (`listTargetsKeepingPending`) keeps such an agent
  from its last list, so a core restart no longer reads as the member's agents
  leaving and coming back; a removed agent (not listed, not pending) leaves.
  bnbloc-acp fills `pending` with agents whose probe has not answered.
  `TestRosterKeepsAgentsTheCoreIsStillChecking` (one agent being checked, one
  removed) fails without it.
- Self-check: a bridge reopens each room subscription after 10 minutes,
  staggered by room (`rosterReadvertiseAfter`), so the room's roster is
  re-confirmed from the bridge's current list whatever the server lost or
  kept. `TestRoomStreamReopensToReadvertise`.
- Presence counts subscriptions and carries no per-subscription data, so it
  does not have the defect.

## Why it was not caught

Lens 7b (identity, "test it with two") and lens 2 (time and lifecycle): the
roster tests use one stream per owner; none holds two streams of one owner
with different lists, closed in either order. Lens 11: no test restarts a
core under a running bridge and checks the rooms' rosters afterwards.
