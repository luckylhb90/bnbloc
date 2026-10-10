# Decision — Agent collaboration is an agent-side extension, not a bnbloc core capability

Status: accepted. Scope decision for "phase 4" of the room-participants /
borrow design (`2026-09-11-room-participants-and-borrow-design.md` §7.4,
"agent↔agent assist"). This document records **what bnbloc does and does not
build** for agent-to-agent collaboration, and why. It supersedes the open
"designed separately" placeholder for phase 4 with a concrete boundary.

## 1. Question

Once a room contains addressable, borrowable agents (phases 1–3, shipped),
should bnbloc implement agent-to-agent collaboration — an agent delegating a
subtask to another member's agent, receiving the result, and continuing — as a
control flow inside bnbloc (bridge / services)?

## 2. Decision

**No. bnbloc does not implement agent orchestration.** Agent-to-agent
collaboration (delegation, hand-back, task decomposition, team loops) is an
**agent-side capability**. bnbloc stays the *room infrastructure for
human+agent collaboration* and exposes three primitives that an
orchestration-capable agent can build on:

1. **Addressing** — every agent has a stable room handle `owner/provider`, and a
   message (from a human or, later, from an agent) can be directed to one.
2. **Authorization** — using another member's agent is gated by that owner's
   per-room borrow authorization (fail-closed, TTL-bounded); see the borrow
   design. Orchestration does not get a bypass.
3. **Visibility** — every agent turn and reply is a room event attributed to its
   `owner/provider` handle, so any collaboration is observable in the feed.

The *orchestration intelligence* — when to delegate, to whom, how to split work,
whether to wait for and re-integrate a result, how to avoid loops — lives in the
agent runtime, not in bnbloc.

## 3. Why

### 3.1 Product philosophy conflict

bnbloc's premise is **the human is a first-class participant**: borrowing needs
the owner's consent, hand-offs are visible in the feed, delivery is
human-directed via the composer recipient. A self-driving agent-team
orchestrator is built on the opposite premise — that human-in-the-loop is a
bottleneck to be removed (this is exactly the stated philosophy of
orchestration-first projects such as oh-my-openagent's `team-core`:
registry + mailbox + tasklist + worktrees driving autonomous agent teams).
Embedding that control flow in bnbloc would fight its own authorization and
visibility model.

### 3.2 The boundary already exists in the architecture

In bnbloc, an agent is an **unmodified provider driven by a bridge**. The
bridge owns transport and claim-by-recipient; the shareagent service owns
authorization; the room owns the event stream. "How an agent decides to
collaborate" is internal to the agent runtime and does not belong to any of
these layers. Agent-team orchestration (the `team-core` shape) is an
agent-runtime concern that should live agent-side — as its own product, or as a
skill/tool injected into an agent — with bnbloc merely *not obstructing* it.

### 3.3 bnbloc already provides what orchestration needs

An orchestration-capable agent in a room (e.g. an agent injected with a
collaboration skill) can already reach another member's agent through the
existing `@owner/provider` addressing and the borrow authorization chain.
bnbloc supplies "addressable + authorized + observable"; the orchestration
logic stays where it belongs. No new bnbloc control flow is required to enable
it.

### 3.4 Known failure modes of in-core orchestration

Autonomous multi-agent orchestration has well-documented production failure
modes — coordination collapse at hand-off boundaries, retry loops / deadlocks,
context drift, and unbounded cost. Owning that control flow inside bnbloc's
transport/authorization core would import those risks into the room
infrastructure. Keeping orchestration agent-side contains them to the runtime
that opted into them.

## 4. Consequences

- bnbloc core (bridge, realtime, activity, shareagent, room) gains **no**
  delegation/hand-back/loop-management logic.
- The phase-4 placeholder in the room-participants design is resolved: there is
  no bnbloc-owned "agent↔agent assist" control flow.
- A later, **narrow, non-coupled extension point** (route 2) may be added so an
  agent can emit a structured, directed collaboration request that bnbloc only
  **parses, authorizes (existing chain), forwards, and shows in the feed** —
  never orchestrates (no result re-injection loop, no team scheduling). That
  extension is optional, disableable, and off the core path. It will go through
  its own design + approval before implementation.
- Agent-side orchestration products/skills remain free to use the existing
  `@owner/provider` addressing + borrow authorization to collaborate in a room.

## 5. Non-goals (reaffirmed)

- No bnbloc-owned agent orchestrator, scheduler, team registry, mailbox, or
  task loop.
- No authorization bypass for agent-initiated use of another member's agent; the
  owner's per-room borrow authorization always applies.
- No credential transfer; the shareagent guarantee is unchanged.

## 6. Deferred requirement — directed collaboration forwarding (route 2, NOT implemented)

Status: **recorded, not implemented.** This is the narrow extension point
referenced in §4. It is captured here so it can be picked up later without
re-deriving the scope. It requires its own design + approval before any code.

### 6.1 What it is

A way for an agent, in its own reply, to direct a subtask at another member's
agent in the same room. bnbloc does exactly four things and stops:

1. **Parse** a directed collaboration request out of an agent's reply text.
2. **Authorize** it through the existing per-room borrow chain (fail closed;
   the target owner must have authorized borrowing of that provider in this
   room — no bypass).
3. **Forward** it to drive the target agent, reusing the existing
   recipient/claim path (the target `owner/provider`'s bridge).
4. **Show** it in the feed: the request as a visible "A → B: <task>" event, and
   B's reply attributed to its `owner/provider` handle.

### 6.2 What it explicitly is NOT (stays agent-side)

- **No result re-injection.** B's reply is posted to the room; it is **not**
  automatically fed back into A's session. A does not auto-continue. Whether to
  act on B's result is a human's or an agent-side orchestrator's decision.
- **No loop/team management.** No hop counting, no A↔B cycle control, no
  scheduler, no task list — because bnbloc never creates the loop in the first
  place (it forwards one directed request, once).
- **No new authorization concept.** Reuses borrow authorization as-is.

### 6.3 Behavioral contract (for the future implementer)

- A request targeting the sender's **own** provider is a normal turn, not a
  collaboration forward.
- A request targeting **another member's** provider with **no live
  authorization** is refused with the existing `owner_not_authorized` visible
  denial; nothing is driven.
- The feature is **optional and disableable**, and off the core message path
  (a quiet room with no directed requests behaves exactly as today).
- One reply may contain at most one actionable directed request in the first
  cut (keep fan-out out of scope until there is a real need).

### 6.4 Open design questions (resolve at design time)

- **Trigger syntax.** Candidates: (A) a strict own-line form
  `@owner/provider: <task>` — lowest false-trigger risk, recommended; (B) any
  in-text `@owner/provider` mention — most natural, but fires when an agent
  merely refers to another agent; (C) an explicit verb prefix,
  e.g. `handoff to @owner/provider: <task>`. A bare in-text mention that is not
  a directed request must never trigger a forward.
- **Provider neutrality.** Parsing agent reply text keeps this zero-injection
  and works for any unmodified provider; confirm no provider emits the chosen
  syntax incidentally.
- **Attribution shape** of the "A → B" event in the feed and how the client
  renders the collaboration edge.

### 6.5 Reuse map (what already exists)

- Handle parsing: the composer's `@owner/provider` recipient-override logic.
- Authorization: shareagent `authorization` endpoints + bridge borrow gate.
- Forwarding: bridge claim-by-recipient + per-`(room,provider)` session.
- Visibility: room event stream + `owner/provider` reply attribution.
