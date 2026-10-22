# Session continuation: a core that restarts says its conversation goes on (2026-10-22)

Status: built 2026-10-22, not released. Protocol, bridge, `bnbloc smoke` and the
core that keeps conversations are done with tests (each mutation-checked); not yet
observed in a real restart: the release gate in §4 step 6 is that observation. Owner: Core Protocol v1 (additive), the bridge
(`services/bridge`), `bnbloc smoke`. A core that keeps conversations across its own
restart implements the core side.

## 1. Why

A core may keep an agent's conversation across its own restart (an upgrade, a crash,
the desktop restarting) but be unable to bring the *session* back whole: a session
that wore an expert's MCP servers and env cannot be restored without those secrets,
so the core keeps only the conversation and expects the caller to create a new session
in the same workspace, which then **adopts** that conversation. The agent keeps what it
knew. The caller does not know that, so for a member:

- 2026-10-22, read from the code, not yet observed in a real run: after every core
  restart the bridge finds the old session id unknown (`sessionReachable`), posts
  *"<agent> continues in a new session (its previous session could not be continued):
  it was handed a summary of this conversation"* to the room, and puts a summary of
  the conversation into the first prompt (`roomlog.ComposeHandover`, which may call
  the summarizer). Both are wrong when the conversation was adopted: the agent
  remembers, the notice says it does not, and the summary repeats what it knows at a
  token cost. It happens once per active session per restart.
- The core picks the conversation to adopt as "the latest of this workspace and
  harness". A workspace has one working tree and **a session per channel**
  (`2026-10-13-sessions-per-channel.md`), so after a restart the first channel to ask
  can adopt another channel's conversation, and the others find nothing (all orphans
  of the workspace are dropped). That is a conversation crossing channels: a privacy
  lens failure, present today and found while looking at the notice.
- `bnbloc smoke --scenario long-task` checks that one session id answered every turn.
  Across a restart the id changes by design, so the release gate's `session` line
  fails for a core that did the right thing.

## 2. What it is (the end state)

The bridge has to know **before** it composes the prompt, because the notice and the
summary are decided there. So the signal comes on the probe, and the create names what
it continues; the answer to a create is a check, not the signal.

### 2.1 The contract (Core Protocol v1, additive)

- **A session the core can continue.** A request addressed to a session id the core
  cannot serve as it was, but whose conversation it holds and can hand to a successor,
  is answered `404` with code **`session_continuable`**. Today it is a plain
  `session_not_found`; a `404` is what every bridge already reads as "not reachable,
  start another", so an older bridge behaves as it does now.
- **A successor says what it continues.** `CreateSessionRequest.continues` is the
  session id the new session takes over. The core adopts **exactly that** session's
  conversation, never "the latest of the workspace". `CreateSessionResponse.continued`
  is `true` when it did.
- **No half answer.** If the core cannot adopt (the conversation is gone, it belongs to
  another workspace or harness, it was already taken), it answers `409`
  **`continuation_lost`** *before* anything runs: no session, no turn. The caller may
  repeat the create without `continues` (nothing took effect).
- A core without the feature never sends `session_continuable` and ignores
  `continues`; no capability flag is needed because the code on the probe is the
  signal.

### 2.2 The bridge

- In `ensureSession`, a stale session whose probe answers `session_continuable` is
  **not** announced and **not** handed over. The old binding is dropped, and a new
  session is created with `continues = <old id>`, in the same workspace, with a fresh
  profile (that is why it is a new session).
- The new session carries the old one's state: the room-context cursor
  (`PushSeenSeq`), the session start (`StartedMs`, which `channelReset` compares with a
  deleted message), and the guidance facts. Its first prompt is a **reused** session's
  (what others said in the channel since its last turn), not a fresh one's handover.
- `continuation_lost`: the bridge falls back to today's path (one notice, handover,
  create without `continues`). A create that answers without `continued: true` after
  `continues` was sent is a core contract violation: the bridge logs it at error level
  and posts the notice (the agent may have lost its memory), never silently.
- The job's diagnostics carry `continues: <old id>` so a person reading a job sees the
  chain, and `bnbloc smoke` can follow it.
- A restored session after a *bridge* restart goes through the same probe
  (`restoreSession` binds the record; the next turn's `ensureSession` probes it), so a
  desktop upgrade that restarts both is covered.

### 2.3 The release gate

`bnbloc smoke` counts one conversation when each later session id was reported
`continues` the previous one; it fails only when sessions are not a chain. The `session`
line says "continued across N restarts" when it was.

### 2.4 What does not change

A core that never reports `session_continuable` (the ACP core, a manifest core)
behaves as today: new session, notice, handover. The summary, the notice text and the
unreachable-session path stay for every case where the conversation really is lost.

## 3. Design review

| Lens | Answer |
|---|---|
| Actors | The member (stops seeing a wrong notice); other channels of the room (stop being exposed to each other's conversation: adoption is by exact id); a borrower's session (same path, same rules); the core (decides what it can continue); an older bridge or core on either side (§6). |
| Time and lifecycle | The restart is the case. A restart *during* a turn fails that turn as today (the turn is lost, the conversation is adopted by the next one). Two restarts in a row: the second probe is for the successor's id, which the core knows as an orphan again. A successor created and never used before another restart is itself continued. The old id is forgotten by the bridge once the successor is bound. |
| Scale and cost | Removes a summarizer call and a summary's tokens per active session per restart. One extra field and one probe code; no new request. |
| Failure and recovery | Each failure falls back to today's visible path: `continuation_lost` → notice + handover + fresh create; a violation (`continued` false) → error log + notice; a probe that fails for another reason → as today. A fallback that fires on every restart would show as the `continuation_lost` count in logs (§5). |
| Boundaries and privacy | Adoption by exact id closes the cross-channel exposure. The core must also refuse an id of another workspace or harness (`continuation_lost`), so a bridge bug or a caller cannot adopt a conversation from outside its workspace. No secret is on the wire: ids only. |
| Abuse and untrusted input | `continues` comes from the bridge's own binding, never from room text or an agent. A core refuses an id it does not hold and one already taken (a second create naming the same id), so two sessions never share a CLI conversation. |
| One owner | The core owns which conversation is adoptable; the bridge owns whether to tell the room. The adoption rule moves from "workspace + harness" to "the named session" in the core only. The notice and handover stay in `noticeNewSession`/`drivePrompt`, one place each. |
| Defaults and fallbacks | Absent `continues` = a new conversation, as today. An unknown code on the probe = not reachable. "Continuable" is positive evidence from the core, never inferred from an absence. |
| External contracts | Core Protocol v1 gains two error codes and two fields. Conformance: a core that reports continuable must pass a continue/lost/taken check in `core/conformance`. The real core is exercised by the smoke with `--between` (a core restart). |
| Load on users and agents | None for agents (no new tool, no new prompt text; a continued session's prompt is shorter than a handover). Members see no notice that was wrong. |
| Release and compatibility | Additive on both sides. New bridge + old core: no continuable code, today's behaviour. Old bridge + new core: a `404` on the probe, today's behaviour, except that the core's *create* without `continues` must keep today's adoption ("latest of workspace and harness") for that bridge; a core drops that fallback once its bridges are all new. Rollout `optin` for the core first. |
| Observability | Log at info `bnbloc.bridge.session_continued` (old id, new id, workspace) and `bnbloc.core.continuation_lost`; the job diagnostics carry `continues`. |
| AI capability | None added. |
| Component or platform | The protocol and the bridge's use of it are the platform's; the adoption itself is the core's, in its own repository. |

**Pre-mortem.** It shipped and later failed. Most likely:

1. *The agent lost its memory and nobody was told.* The core said `continuable`, the
   conversation was gone. Mitigation: `continuation_lost` before anything runs; the
   smoke's `memory` line is the check across a real restart.
2. *The new session did not get what a fresh one would have.* Mitigation: the guidance
   facts, the profile and the context cursor carry over; a test compares the first
   prompt of a continued session with a reused one's.
3. *Two channels' sessions both continue the same conversation* (the old rule, or a
   bug). Mitigation: exact id plus "already taken" refusal; a test with two channels
   of one workspace across a restart.
4. *An old core ignores `continues` yet answered continuable* (impossible by
   contract, possible by bug): the `continued` check logs and notices.
5. *The release gate went green while the room was misinformed.* The smoke reads
   `continues` from the job, and a separate check reads the room for a new-session
   notice after `--between` for a core that reported continuation.

## 4. Build order

1. Protocol and `core`: codes, fields, sentinels, the client's mapping, tests.
2. Bridge: probe → continue, carry-over, fallback, diagnostics; tests with a fake core
   (continue, lost, taken, two channels, old core, violation).
3. The core that keeps conversations: report `session_continuable`, adopt by exact id,
   `continuation_lost`, `continued`.
4. `bnbloc smoke`: follow `continues`.
5. Docs: `docs/architecture/core-providers.md`, the core's README.
6. Release: the core first (`optin`), then the desktop; the smoke across a core
   restart on the operator's own desktop passes its `memory` and `session` lines and
   the room shows no new-session notice.

## 5. Open questions

- Should the bridge keep the old id's job history for the chain beyond diagnostics
  (a "conversation id" that survives)? Not needed to stop the notice; a later record
  if members ask where a conversation started.
- A conversation older than the core's retention of orphans: the core's bound
  (`continuation_lost` after it) is the core's to state; the bridge only reports.
