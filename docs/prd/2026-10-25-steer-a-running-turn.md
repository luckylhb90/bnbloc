# Steer a running turn: a message sent while the agent works joins that work

Status: slice 1 built, not committed (2026-10-25: contract, bridge `steerInto`, job view `steeredInto`,
web marker and stop-and-do-this-now button, `bnbloc-acp` `steer`). Slice 1: the Core Protocol capability, the bridge's decision, the
`bnbloc-acp` core, the room's marker. Slice 2: other cores (the company core fronting its CLI)
once their agent is known to accept it.

## 1. Why

A member who sees the agent heading the wrong way, or remembers something, says so. Today
the message waits: the turns of one conversation queue (`services/bridge/turn_gates.go`), and
a core runs one turn at a time (`bnbloc-acp`'s `enqueue`/`run`). The correction lands after
the whole run, minutes later, after the work it was meant to change. The owner: every agent
should support steering.

The same thing is happening across the agent ecosystem: a convention for it is shipped by two
ACP agents (`_session/steering`, advertised in `initialize` `_meta.steering.supported`, an
outcome of `injected`, or `promptRequired` when idle) and a standards proposal adds a
`delivery` field to `session/prompt` (`steer` joins the running turn at its next safe point,
`queue` starts one after it; agentclientprotocol/agent-client-protocol#1261, not merged).
Agents differ; some refuse a second prompt outright. So the platform cannot hard-code one
agent's way: it needs a capability a core declares, like `interrupt`.

## 2. What it is

### 2.1 Core Protocol: an optional capability `steer`

- A core declares `steer` in `discover` (the same way as `interrupt`); undeclared means not
  supported, and nothing changes for it.
- `POST …/sessions/{sid}/steer` with `{text}` answers one of:
  - `injected`: joined the running turn at its next safe point;
  - `idle`: no turn is running; nothing was consumed, the caller sends it as an ordinary input;
  - `unsupported`: this agent cannot take it now; nothing was consumed, the caller queues it.
  Anything else (a 4xx/5xx) is an error and the message is queued as before. A steer never
  fails a turn, never cancels one, and never consumes text it did not take.
- `core.Steerer` (`Steer(ctx, ep, workspaceID, sessionID, text) (SteerOutcome, error)`) beside
  `TurnInterrupter`, found through `core.As`. Conformance checks it only when advertised.

### 2.2 The bridge decides, per message, and one message stays one job

A message to an agent is a job, as today (it is billed, audited, retried, shown with its own
reply state, and the activity strip counts jobs). Steering does not merge jobs:

1. The job is claimed. If **its own session** has a turn running (this bridge holds a running
   job on that session key) and the core declares `steer`, the bridge asks the core to `steer`
   the text, framed as a follow-up input is (so the agent knows who said it).
2. `injected`: the new job settles at once as **steered into** the running job: its status is
   `replied` with a diagnostics note `steeredInto: <jobId>`, no reply of its own is expected,
   and the running job's reply, when it comes, answers both. The room shows the member's
   message in the conversation as ever, with a marker "joined the running task".
3. `idle` (the turn ended in the meantime) or no running turn: an ordinary follow-up input, as
   today. `unsupported`, an error, or no `steer` capability: the queue, as today. Nothing a
   core cannot do is attempted, and no core is worse off than now.
4. **The session key is the whole rule for whose message joins whose turn.** A session belongs
   to one requester (docs/prd/2026-10-14-multi-member-rooms.md rule 1): the owner's own turns,
   and each member borrowing the agent (and each member using a platform agent), have a
   session of their own, and a person's conversation, attachments and Library listing never
   reach another's turn. So a message can only steer a turn of **its own session**, that is,
   the same requester's, in the same channel, with the same agent. A second member's message
   to that agent runs in **their** session, as a borrow does today (their quota, their audit,
   their workspace slot); steering neither widens nor narrows that. Two people cannot steer one
   running task, and this is the privacy rule, not a steering limit: writing a second person's
   text into the first person's session is exactly what rule 1 forbids. Working on one task
   together would need a shared session, which changes that model and is a separate design.
5. The steered message is the sender's job: it passes the same admission as any message to the
   agent (membership, the borrow authorization and run quota, the budget), is audited under the
   sender and spends the sender's quota once. Stop is as available as ever.

### 2.3 When steering is not wanted: stop and send, by the member's click

A core that cannot steer, or a member who wants the agent to drop what it is doing, gets a
button on a queued message: "Stop the current task and do this now". It is the member's
click: it stops the running job (the existing cancel, which asks the running job's own
rules: a member may stop a task they started, or one in a room they own) and then the
queued message starts. Nothing stops by itself: a stop discards the unfinished part of a
run (what was already written stays; what was not done is lost), and a person must choose
that.

### 2.4 The agent's side: a core

`bnbloc-acp` fronts ACP agents. It declares `steer` when the agent it runs advertises
`_meta.steering.supported` and forwards a steer as `_session/steering`: `injected` →
`injected`, `promptRequired` → `idle`, a refusal → `unsupported`. An agent that does not
advertise it is not offered steering and is queued as before. The company core is slice 2
and depends on what its CLI can do; if it cannot, it keeps queuing and says so.

## 3. Placement

| Part | Placement | Why | What an organization would swap |
|---|---|---|---|
| The `steer` capability and its outcomes | **Platform contract** (Core Protocol) with a conformance check | Every core and the bridge must read the same three answers. Reason: contracts | Nothing; a core implements it or does not |
| Turning a steer into the agent's own mechanism | **Component** (each core, in its repository) | Facts about one agent's protocol; the platform names none | The core |
| Deciding to steer a message and settling its job | **Platform** (the bridge) | It holds the running turn and the queue, and must keep billing, audit and admission as they are. Reasons: budget, audit, isolation | Nothing |
| The marker and the "stop and do this now" button | **Platform** (web client) | The product's own surface; names no agent | Nothing |

Name test: no agent named in platform code. Fork test: an organization with a different agent
implements the same capability in its core. Replace: a second core installs beside the first.

## 4. Design review

1. **Actors.** The sender (the steered job is theirs: quota, audit, admission; the turn is in
   their own session, so nobody else's conversation is touched); the agent's owner (a borrowed
   agent: the borrow authorization and quota apply to the sender, as to a queued message); a
   visitor (cannot send, so cannot steer). Two members messaging one agent: each has a session
   of their own and their messages never meet; neither can steer the other's turn.
2. **Time.** The turn may end between the decision and the call: the core answers `idle` and
   the message is an ordinary input; a steer that arrives at a restarting core is an error and
   the message is queued (the queue is the safe default). A steer never extends or shortens a
   turn's deadline.
3. **One run, two jobs.** The steered job is terminal at once; the running job's lease,
   deadline, cancel and retry are untouched. If the running job later fails or is cancelled,
   a steered message was still delivered to an agent that has stopped: the room line says
   "the task it joined ended" and the member can resend (the message is in the history).
4. **Cost.** No extra tokens beyond the message; the sender's quota is spent once. No extra
   agent run.
5. **Failure.** A core error, an unknown outcome, a missing capability: the queue. A steer
   that was delivered but whose job could not be marked settled is retried by the job's own
   lease; it must not be sent twice (the bridge records the steer before the call and the job
   id is the idempotency key).
6. **Security.** A steered message is fenced like any message (someone else's text is data
   to check, never an instruction from the platform); steering gives no new reach. The text
   is passed as it was sent; it is not summarised or rewritten.
7. **One owner.** `core.Steerer` and its protocol path (the capability); the bridge's
   `steerOrQueue` (the one place that decides); the job's `steeredInto` note is a status
   diagnostic, read by one function in the web client.
8. **Agents that cannot.** No change, no promise: queueing is still correct and still shown
   as "queued", and the member has the stop-and-send button.
9. **Not here.** Steering a turn on another bridge (a job's session lives on one bridge);
   merging replies; steering a scheduled or dispatched job (they are not a person's
   follow-up).
