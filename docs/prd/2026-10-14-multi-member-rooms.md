# Many people, many agents in one room (2026-10-14)

Status: accepted (product owner, 2026-10-14). Revised the same day: no
new cognitive load for agents — the platform does the work and agents see
plain files and conversation; no new room tools, no new guidance rules or
meta-instructions (rules 4, 6, 7 below are stated in that form).

## 1. Why

Every check so far ran one member with their own agents. A room is meant
for several people, each with their own agents, plus borrowed and platform
agents. A live test with two members (each driving an agent from their own
bridge) and a code audit found that the shared context mostly works —
other people's messages and other agents' deliverables reach every agent —
but several paths leak, misattribute or starve.

## 2. Rules

**Privacy and correctness first**

1. **A session belongs to one requester.** A drive's session (and its
   conversation) is keyed by room, core, provider, channel **and the
   requester**: a member who borrows another member's agent, and every
   member using a platform agent, get their own session; the owner's
   conversation, attachments and Library listing never reach a borrower's
   turn, nor the reverse. The workspace may stay shared per agent slot
   (deliverables live there), but per-requester material (attachments,
   library listing) is written per session, not into a shared place.
2. **Records follow the room.** A message deleted or edited in the room is
   deleted or edited in every agent's `.room/` records at its next turn;
   records of a room that is gone are removed.
3. **The sender of a turn is the job's message**, by its sequence, never by
   matching text.

**Context every agent can rely on**

4. **A room's attachments are the room's.** The bridge mirrors the room's
   attachments the requester may read into the session's
   `.room/attachments/` (lazy, bounded, like deliverable copies), and the
   records reference that path next to the message; a message carrying
   only attachments is recorded. No new tool.
5. **A channel is a context boundary.** A session's working records and
   turn input are its own channel's only. Every other channel the agent may
   see is mirrored into its own files (`.room/channels/<name>/`, listed in
   `.room/INDEX.md`): on disk, read only when the agent chooses, costing no
   context otherwise. Each channel's records are paged on their own, so no
   channel starves because another is busy. Deliverables and attachments
   are the room's, indexed room-wide.
6. **A reused session hears what changed.** A turn's input carries the
   messages others posted in its own channel since the agent's last turn, as
   plain conversation lines (capped) before the request — data, not
   instructions — so a correction someone else posted is not missed.
7. **Deliverables are there when the reply is.** A turn's deliverables are
   published before its job is reported done; `ask_agent` returns them;
   `.room/artifacts.md` indexes every deliverable (paged if long), the
   existing `read_artifact` reads older ones. No new tool.
8. **Names, not ids.** Records and prompts show people's display names
   (with the id once, so mentions stay unambiguous); the session states its
   owner and each turn its requester in the existing lines.
9. **A refused borrow asks.** When `ask_agent` (or a member) is refused
   because the owner has not authorized borrowing, the owner gets a request
   they can approve in one step, and the asker is told it was requested.

## 3. Tests

- Two members, each with their own agent, in one room (the release check
  gains this scenario): messages, deliverables, attachments, a correction
  between turns, two channels, ask_agent with and without authorization.
- Unit: per-requester session keys; record rewrite on edit/delete; sender
  by sequence; channel fairness and labels; attachment records and the
  tool; publish-before-done; names in records; the borrow request.
