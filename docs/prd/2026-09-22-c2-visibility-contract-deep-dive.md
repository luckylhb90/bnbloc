# C2 deep-dive — the visibility contract: what exists, what's missing, what to build

Status: deep-dive analysis
Date: 2026-09-22
Parent: `2026-09-22-collaboration-initiation-ambient-structure.md` (capability C2)

## The reframe: C2 is consolidation, not construction

I surveyed the stack before designing. The surprising finding: **most of the
raw visibility signals already exist.** The gap is not "agents are black boxes
with no signals" — it is that the signals are **fragmented, shallow, and never
composed into a single legible contract.** So C2 is: (1) consolidate what's
scattered, (2) fill three real blind spots, (3) present it as one coherent
"who's doing what, on what, needing what" surface.

### What already exists (do NOT rebuild)

| Signal | Where | State |
| --- | --- | --- |
| Agent working/idle, per agent | bridge `postSessionState` → `agent_session_state` event → `sse.ts agentPhase` → `MessageFeed` per-agent "working…" indicator | **Done** |
| Job lifecycle state machine | `agent_job_updated` event (queued→claimed→running→reflowing→replied/failed/timed_out/cancelled), fanned room-wide | **Done** |
| Job status → human labels + failure reasons + diagnostics + Stop button | `AgentJobStatus.tsx` (rich: retry, attempt count, failure detail, dev diagnostics) | **Done** |
| Artifact authorship | `artifact_versions.author_user_id`; version history shows `v{n} · {author} · {time}`; `artifact_updated` carries `updatedBy` | **Done (data), shallow (presentation)** |
| Interrupt plumbing | bridge interrupt + job cancel endpoint + Stop button | **Done (mechanism), not first-class (UX)** |

### The three real blind spots

Everything above is *per-signal, per-surface*. The contract is missing at the
seams:

**Blind spot 1 — no unified "room activity" view.** A member cannot answer
"what is happening in this room right now?" in one glance. Working indicators
live in the message feed; job status lives under a message; artifact changes
live in the Docs panel. When 3 people and 3 agents are active, the state is
smeared across three surfaces. There is no single **presence-of-work** view:
*which agents are working, on what, since when; which jobs are queued/blocked;
which artifacts changed and by whom.*

**Blind spot 2 — human-vs-agent attribution is data, not a visible contract.**
`author_user_id` exists, but a driven agent's reply owner is `owner/provider`
(e.g. `jack/codex`) while a human is a bare id — the UI does not consistently
and unmistakably mark "this came from an agent" vs "this came from a person"
across ALL surfaces (chat, artifact versions, comments, decisions). The AI UX
handbook's core rule: a member must *always* be able to tell which changes came
from a human and which from an agent. Today it's inferrable, not asserted.

**Blind spot 3 — no "proposal vs active vs done vs needs-approval" distinction.**
This is the deepest gap and the one that matters most as P1–P4 land. Today an
agent action is binary: it happened (a reply, a published artifact version) or
it's in-flight (working). There is no first-class notion of an action that is
**proposed and awaiting a human decision.** The AI UX handbook's named
anti-pattern ("the invisible boundary") is exactly this: you cannot tell when an
agent moves from *drafting* to *acting*, and an approval looks like a local edit.
When P2 change-requests and the ambient recognizer's suggestions arrive, they
NEED this state — a proposal must look different from a fait accompli.

## The contract to build

Define one **agent/action state vocabulary** used identically on every surface,
so "the interface is the contract" (AI UX handbook) holds:

```
proposed   — an agent/human suggests a change/decision/task; awaits approval
active     — work in progress (agent working, job running)
blocked    — needs input/approval/authorization to proceed
done       — completed and applied (a published version, an accepted decision)
failed     — terminated without success (with an actionable reason)
```

Every collaborative object (a turn, an artifact change, a change-request, a
recognized decision/goal suggestion) reports one of these states, rendered with
one consistent visual language and one consistent attribution ("by jack" vs
"by jack/codex 🤖"). This is the substrate C1 (recognized suggestions are
`proposed`), C3 (goal/tasks have state), C4 (concurrent work needs live state),
and P1–P4 (change-requests are `proposed`→`done`) all plug into.

## Concrete build (incremental, reuses existing signals)

### Step 1 — Consolidate: a room "activity rail"
A single room-scoped surface (a rail/header strip) that composes the signals
that already exist into one live view:
- per-agent chips: `jack/codex · active · editing architecture.md · 12s`
  (from `agent_session_state` + the job's `target`/artifact link);
- queued/blocked jobs with their reason (from `agent_job_updated` +
  `AgentJobStatus` labels, promoted out from under a single message);
- recent artifact changes with attribution (from `artifact_updated.updatedBy`).

No new backend data — it's a composition of `agent_session_state`,
`agent_job_updated`, `artifact_updated` into one subscriber. This alone kills
Blind spot 1.

### Step 2 — Assert attribution everywhere (Blind spot 2)
A single rule + component: any actor label resolves to `{kind: human|agent,
display, owner?, provider?}` and renders with an unmistakable agent marker,
used in chat, artifact versions, comments, decisions, and the activity rail.
The data is already present (`owner/provider` convention, `author_user_id`);
this is a presentation contract, not a schema change. Small, high-trust win.

### Step 3 — Introduce the `proposed`/`blocked` states (Blind spot 3)
This is the one that needs new backend surface, and it's the bridge to P1–P4
and C1:
- extend the collaborative-object event vocabulary so a change (artifact CR,
  recognized decision/goal, borrow-authorization request) can be published as
  `proposed` with an approve/reject affordance, distinct from an applied change;
- the activity rail and the artifact/decision surfaces render `proposed`
  differently (a pending, actionable card — not a done bubble), with a clear
  approval control that is visually distinct from an ordinary edit;
- an agent moving from drafting to a consequential action surfaces as
  `proposed`/`blocked`, never silently as `done`.

Step 3 is deliberately co-designed with P2 (change-requests) from the
shared-work-products PRD — they are the same state machine viewed from two
angles, so build them together rather than twice.

## Sequencing within C2

1. **Step 1 (activity rail)** — pure composition of existing events; immediate
   "I can see what's happening" win; zero backend risk.
2. **Step 2 (attribution contract)** — small, presentation-only, high trust
   payoff; do alongside Step 1.
3. **Step 3 (proposed/blocked states)** — new surface, co-built with P2
   change-requests; the substrate for C1 suggestions and the ambient layer.

## Boundary & safety notes

- Stays within `2026-09-11-agent-collaboration-boundary-decision.md`: this is
  visibility + approval surfaces, not orchestration. The rail *shows* state and
  offers *approve/reject*; it does not schedule or route agents.
- Avoid the "operational noise" trap (AI UX handbook): the rail shows state and
  proposals, NOT a stream of every internal agent step. Visibility should
  support control, not flood the room. Transient per-token streaming is
  explicitly out (the core exposes only completed messages anyway).
- Realtime discipline (from AGENTS.md): activity-rail signals ride existing
  events; anything transient (working/idle, proposal-pending) is NOT persisted
  to history — it's live state, recovered by a join snapshot, like the artifact
  live-state channel already does.

## Open questions

1. Where does the activity rail live — a room header strip, a collapsible right
   rail, or folded into the existing roster? (Lean: a compact header strip that
   expands; the roster shows *presence*, the rail shows *work*.)
2. Does `proposed` state get its own durable table, or reuse P2's
   `artifact_change_requests` + a generic "pending suggestion" row for C1's
   recognized goals/decisions? (Lean: reuse P2 for artifact changes; a small
   `room_suggestions` table for C1's non-artifact proposals.)
3. Attribution for borrowed agents: a borrowed `jack/codex` driven by another
   member — whose action is it? (The audit already distinguishes actor≠owner;
   surface both: "jack/codex, run by mary".)
