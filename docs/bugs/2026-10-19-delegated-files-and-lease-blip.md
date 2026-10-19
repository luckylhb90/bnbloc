# A delegated task's files were out of reach mid-turn, and one failed lease renewal restarted the turn (2026-10-19)

Found on QA. Agent A (opencode) asked agent B (claude-code) with `ask_agent`
to capture four screenshots for a deck. B published them as room
deliverables and A's `room_status` listed their ids, yet A could not use
them, and A's turn was then cut off and ran again from the start while
the room showed it working.

## What happened

1. **The files were out of reach.** `.room/deliverables/` holds a copy of
   each room deliverable, written when a turn starts. B's files were
   published during A's turn, so A's copy did not have them. The
   deliverable's `link` needs a member's session (401 for the agent), and
   `read_artifact` does not return a binary. A began re-capturing the
   screenshots itself.
2. **One failed renewal ended the turn.** 26 minutes in, one lease
   renewal failed in transit. The bridge treated any failed renewal as a
   lost lease and interrupted the turn at once, although the lease had
   40 s left. The lease lapsed, the job was queued again (attempt 2) and
   a new session redid the work from the room's records.

## Fix

- `ask_agent` and `room_status` copy the room's deliverables into the
  caller's workspace as they report a finished task (the same pass a
  turn makes at its start, `sessionWorkspace.CopyDeliverables`), and name
  each one's `path`.
- The lease loop ends the turn only when the server refuses the renewal
  (`permanentGatewayError`: it took the job back, or the job is gone). A
  failure in transit is retried ten times as often; the server, which
  refuses a renewal once it took the lease back, stays the authority.

Tests: `TestAskAgentFlow` (path), `TestDeliverablesCopiedMidTurn`,
`TestLeaseSurvivesTransientRenewalFailures`.
