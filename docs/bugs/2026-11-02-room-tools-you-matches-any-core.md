# list_room_agents marked another core's agent as "you" (2026-11-02)

Status: fixed (see Fix).

## Repro

1. A room's roster holds two agents of one member and one provider that
   differ only in their core, one of them advertised without a core (an
   unqualified `?agents=codex`, which realtime accepts): `jack//codex` and
   `jack/acme/codex`.
2. The session of `jack/acme/codex` calls `list_room_agents`: both rows say
   `you: true`. `ask_agent jack/codex` (the other one) is refused as "that
   is you", and `components try` skips it as a target.

## Root cause

`roomAgents` (`services/bridge/roomtools/agents.go`) decided "is this row
the session's own agent" with `a.Core == "" || scope.Core == "" || a.Core ==
scope.Core`: an empty core on either side matched any core. That is a
wildcard on the agent's key (owner, core, provider), the case the Decision
gate and design-review lens 7b name. It was found while reviewing the room
tools for the rooms-work-together design (2026-10-26) and not recorded then.

## Fix

The row is the session's own agent only when owner, core and provider are
all equal, core included; a row or a scope without a core is not "you"
(nothing proves it is). A self-ask that slips through is still refused by
realtime's dispatch guard (`sameAgent` in `services/realtime/dispatch.go`),
which keeps its wildcard on purpose: it refuses, it never acts.
`TestListRoomAgentsMarksYouByTheWholeKey` holds a roster with two agents of
one owner and provider that differ only in core.

## Why it was not caught

Lens 7b (identity, "test it with two"): every room-tools roster fixture had
one agent per owner and provider, so a match that ignored the core could not
fail. The question is already in `docs/design-review.md` 7b; the missing
piece was the test world with two candidates, now added.
