# Collaboration initiation & the ambient structure layer

Status: exploration / proposed direction
Date: 2026-09-22
Owner: maya
Related: `2026-09-22-shared-work-products-collaboration.md` (P1–P4 artifact
collaboration), `2026-09-11-agent-collaboration-boundary-decision.md` (bnbloc
does not orchestrate agents), `2026-09-11-room-participants-and-borrow-design.md`.

## The question this document answers

The base is built (rooms, membership, multi-agent addressing, borrow,
attachments, realtime, agent-authored artifacts). The open question is *how a
multi-person + multi-agent collaboration actually starts and stays coherent* —
and specifically: **should starting a collaboration be a structured setup
(a "new project" wizard: goal, roles, initial materials), or should it just be
a conversation?**

## Decision / direction

**Conversation is the entry point. Structure is a product of the conversation,
not a precondition for it.** We do NOT build a structured-initiation wizard
(no goal form, no blueprint picker) as the primary path. A collaboration starts
the way real teamwork starts: create a room, say a sentence, @-summon an agent.
Goals, tasks, decisions, and products then **emerge from the dialogue and are
recognized/confirmed** — not pre-declared.

This is deliberately the *light* choice, and it is reversible: if usage later
shows people repeatedly initiate the same kind of collaboration and resent
re-explaining context each time, structured blueprints can be added THEN, with
real data — not pre-built on speculation. Start light, observe, add weight only
on evidence.

## Why (grounded, not just intuition)

Industry and research converge on this, and warn against the wizard:

- **Real collaboration authors its structure in the conversation, not before
  it.** A study of a startup strategizing entirely in Slack ("Constructing the
  Accomplishable") found strategy is *authored* through referencing, reasoning,
  and revealing in messages over time — actionability and accomplishment are
  displayed and accumulated in dialogue, never pre-filled in a form.
- **The winning pattern is an *ambient* agent that turns unstructured dialogue
  into structure.** DevNous (2025) is exactly this: an ambient, observational
  multi-agent system that sits in the team's chat, detects *actionable intent*,
  and auto-formalizes it into tasks/progress records — its stated purpose is to
  keep valuable information "from slipping through the cracks of a high-velocity
  chat." It defaults to **passive observation** and only intervenes to formalize
  when intent is clear.
- **Decisions already emerge conversationally; the product's job is to *capture*
  them, not to make people pre-declare them.** Cue (Slack autonomous agent)
  extracts a "Decision Contract" (owner, deadline, acceptance criteria) from a
  thread and turns it into reviewable execution. Its thesis: "Slack is where
  teams already coordinate, but coordination normally stops at conversation" —
  the gap is capture-and-act, not setup.

Conclusion: the highest-leverage thing is not a nicer "new room" screen. It is
an **ambient structure layer** that watches the conversation and offers to
solidify the structure that emerges.

## The four capabilities (in priority order)

### C1 — Ambient structure recognition (the core new capability)

A background pass over room conversation that recognizes emergent structure and
offers to solidify it — **never pre-fills, never auto-commits, defaults to not
interrupting.**

Recognizes:
- **Goal**: "we need to get the payment review done this week" → offer: "Set
  this as the room's goal?" (confirmed, not auto-set).
- **Tasks / action items**: "@jack can you take the risk section" → offer a
  task assigned to jack (or `owner/provider` for an agent).
- **Decisions**: a proposal that gained acceptance without objection → offer to
  record it as a decision (this is P4 from the shared-work-products PRD, but
  *triggered by recognition*, not manual authoring).

Two hard requirements from the research:

1. **Cross-talk detection (DevNous).** The recognizer MUST distinguish "this is
   directed intent worth formalizing" from ordinary human chatter, and default
   to silence. Getting this wrong (an agent that keeps interrupting to "formalize
   your task?") is worse than doing nothing. The safe default is: recognize
   quietly, surface a *dismissible, low-friction* suggestion, never block the
   conversation.
2. **Decisions/goals are trajectories, not single messages.** Recognition uses
   the discourse heuristics validated in practice (see `slack-researcher`):
   a decision = proposal → discussion → convergence with no subsequent
   objection; the conclusion lives in a thread's *final substantive replies*,
   not its opening; brevity ("+1", "sounds good") signals strong consensus, a
   long hedged reply signals soft objection, and silence from active
   participants is weak-but-real consent. The recognizer reads the arc, not one
   line.

**Boundary check:** this stays on the right side of
`2026-09-11-agent-collaboration-boundary-decision.md`. Recognition produces
*visibility and suggestions* (a candidate goal, a candidate decision record) —
it does not orchestrate agents or drive a workflow. A human (or an agent, by its
own choice) confirms. bnbloc surfaces; it does not conduct.

### C2 — The visibility contract (the trust substrate, currently missing)

Today a driven agent is a black box that emits a chat reply. Every source on
human-AI UX (the AI UX handbook, the HAX framework, CLEO, Deep Cognition) says
the same thing: **in a shared human-agent workspace, trust comes from a visible
contract, not from a chat bubble.** Members must be able to see, at a glance:

- which agent is doing what right now (working / waiting / blocked / done) —
  we have a `working` session-state signal already; extend it into a first-class
  per-agent status members can see;
- what an agent is changing (which artifact/version) and what context it used;
- what is a *proposal* vs an *active change* vs *complete* vs *needs approval*
  (this is P2 change-requests made visible);
- which contributions came from a human vs an agent (attribution — the version
  store already has `author_user_id`; surface it consistently);
- how to interrupt / redirect / undo.

Anti-pattern to avoid (AI UX handbook, "the invisible boundary"): one
conversational surface where you cannot tell when an agent moves from *drafting*
to *acting*, where an approval button looks the same for a local edit and an
external action. Do not hide consequence to look simple.

This is the substrate under C1, C3, and the whole P1–P4 artifact layer. Without
it, "multi-agent collaboration" is just several black boxes talking.

### C3 — Goal as a first-class (but conversation-born) room object

From the earlier analysis, kept — but its *creation* is via C1 recognition, not
a form. A room can have a Goal (statement, status active/done/abandoned) and a
task list; the Goal is injected into every driven agent's context (like the
`.room/goal.md` analog to how attachments/comments are injected), so agents work
*toward the room's objective* instead of answering one message at a time. This
directly reduces multi-agent thrash: an agent that knows the goal and the open
tasks doesn't re-litigate settled questions or duplicate a peer's work. It also
gives collaboration a beginning and an end (goal → done), so a room isn't an
infinite drift.

### C4 — Design for concurrent collaboration, not just async review (the frontier)

The strongest signal in the recent research (CLEO/CHI 2026, Deep Cognition) is a
paradigm shift we should NOT design ourselves out of: **from sequential
delegation (send → wait → review) to concurrent co-creation.** When an agent's
work becomes visible, humans instinctively jump in mid-execution — CLEO observed
concurrent intervention in ~32% of turns. People want to nudge, take over a
piece, work in parallel, or redirect *while the agent is working*, not only
after.

Implication for us: the P1–P4 artifact primitives should not assume "human
reviews the agent's finished product later." They should evolve toward "human
and agent (and other agents) work on the same product concurrently," which
requires:
- the agent being able to distinguish *feedback on its work* from *independent
  parallel work* by a human/peer (CLEO's core capability), and adapt;
- interrupt/redirect mid-turn (we have interrupt plumbing in the bridge; expose
  it as a first-class UX);
- claims (P3) and change-requests (P2) as the coordination mechanism that makes
  concurrent editing safe.

We do not build all of this now, but we should not make choices that preclude
it (e.g., don't hard-assume one-agent-per-turn ownership of an artifact).

## Recommended sequencing

1. **C2 visibility contract** first — it is the substrate everything else needs
   and the thing most missing today (agents are black boxes). Cheap wins:
   surface per-agent status, artifact-change attribution, proposal-vs-active
   state.
2. **C3 goal object** — small (a table + context injection + a header), and it
   immediately makes every room — including existing ones — purposeful and
   improves agent context. Creation wired to C1 once C1 exists; until then, a
   one-line "set goal" affordance.
3. **C1 ambient recognition** — the differentiating capability, but the riskiest
   (cross-talk, false positives). Build after C2/C3 give it something to
   populate (recognized goal → C3; recognized decision → P4). Start with the
   most valuable, lowest-false-positive recognizer: **decisions** (a clear
   trajectory), then tasks, then goal.
4. **C4 concurrency** — the frontier; keep the door open, build when the base
   collaboration loop (P1–P4 + C1–C3) is proven.

## Explicitly NOT doing (now)

- Structured initiation wizard / goal form / blueprint picker as the primary
  entry. (Revisit only with usage evidence of repeated same-shape collaborations.)
- Agent orchestration / workflow engine (boundary decision stands).
- Auto-committing recognized structure without human confirmation.

## Open questions to deepen next

1. **Who runs the ambient recognizer?** A dedicated room "facilitator" agent, a
   lightweight server-side pass, or a capability of any driven agent? Cost,
   latency, and cross-talk accuracy differ a lot. (Lean: a cheap server-side
   recognizer on the message stream, not a full agent turn per message.)
2. **Cross-talk threshold & interaction:** how does a recognized suggestion
   appear without being noise? (Lean: a quiet, dismissible chip on the message,
   not a bot reply in the feed.)
3. **Decision trajectory detection:** concretely, what window / signals trigger
   "this looks like a decision"? Encode the brevity=consensus / silence=consent
   heuristics — how, and how conservatively?
4. **C4 concurrency:** what is the minimal "agent understands human intervention"
   capability we can reach given the bridge/core seam, without crossing into
   orchestration?
