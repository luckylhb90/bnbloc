# Context usage: show how full the agent's conversation is (2026-10-24)

Status: designed and being built (2026-10-24). Owners: this repository (the web
client's meter and the reply-metrics contract, `docs/architecture/core-providers.md`
"Reply metrics"), the ACP core (`bnbloc-acp`, its own repository), and each core
that can report it (a core's own repository). No Core Protocol type, bridge or
gateway change: the reply's free-form `metrics` map already travels whole.

## 1. Why

A member sees what a reply cost (tokens in and out, cache, time) after the
fact, but never how much of the model's context the conversation already holds.
A long task fills it silently; the member learns only when the agent starts
forgetting, compacts on its own, or fails with a context error and the core
starts a fresh session (core providers guide, "a turn that outgrew the model's
context"). Agents already know the number (ACP `usage_update` reports `used` and
`size`; Claude Code reports every model call's usage and the window), the
product throws it away.

## 2. What it is

**The number.** Two display-only keys in a reply's `metrics`:

| Key | Meaning |
|---|---|
| `contextTokens` | Tokens the model read for the **last request of the turn**: input plus cache read plus cache creation. Not the turn's sum (a turn of many requests re-reads the context each time; the sum overstates it). Absent or 0 = not reported. |
| `contextWindow` | The model's context window in tokens, from the authority the core trusts (never guessed from a model's name). Absent or 0 = unknown. |

**The meter.** In the composer, beside the model picker: a small bar and a
percentage for the agent the next message goes to, from that agent's newest
reply in this channel that reports both numbers. Its tooltip says what it is:
"Context after the last reply: 118k of 1M tokens (12%) · model". It is hidden
when either number is unknown (never "0%"), turns amber from 70% and red from
90%, and never shows more than 100%. It says "after the last reply": it does not
move while the agent works.

**Producers.** A core reports what its agent gives it:

| Source | `contextTokens` | `contextWindow` |
|---|---|---|
| ACP agent (the ACP core) | `usage_update.used` (latest of the turn) | `usage_update.size` |
| Claude Code (stream-json) | last assistant message's `input + cache_read + cache_creation` (not `result.usage`, which sums the turn) | `result.modelUsage[model].contextWindow`, or the core's authority |
| OpenCode (JSON events) | last `step_finish`'s `input + cache.read + cache.write` | the core's authority |
| Codex (`exec --json`) | **not reported yet**: `turn.completed.usage` may sum a multi-request turn; unverified | the core's authority |

A core that cannot report leaves the keys out; nothing is estimated.

## 3. Not in this step

- Re-evaluating the meter for a model the member just switched to (the window is
  the one the last reply reported).
- Acting on a full context: a "start fresh with a summary" offer, warnings in the
  agent's prompt. The assist seam could propose it later; AI proposes, people
  decide.
- Codex, and the agents that report no usage.
- The number inside a reply's footer (the footer keeps showing the turn's own
  counts).

## 4. Placement table

| Part | Placement | Why | What an organization would swap |
|---|---|---|---|
| The two metrics keys and their meaning | **Platform contract** (the reply metrics section of the core providers guide, one reader in the web client) | A contract with a conformance vector: every core and the client must read the same thing. Named reason: contracts | Nothing; a core fills the keys or leaves them out |
| Reading each agent's usage into the keys | **Component**: each core, in its own repository | Facts about one agent's output format; the platform names no agent | The core |
| The meter in the composer | **Platform** (web client) | It is the product's own surface for the room's conversation; it names no agent and no model | Nothing: it reads the contract |
| The window of a model | **Component data** (the core's authority, e.g. its model catalog) | A fact about the outside world | The core's source |

Name test: no agent, model or provider named in platform code. Fork test: an
organization with another core fills the same two keys. Replace: any core.

## 5. Design review

1. **Actors.** The member addressing an agent sees that agent's context; a
   borrowed agent's replies already show their token counts to the room in the
   reply footer, so the meter adds no new disclosure. Two agents in a channel:
   the meter follows the agent selected as the recipient (matched by agent
   identity), never the newest reply of anyone.
2. **Time and lifecycle.** "After the last reply" is the contract. A fresh
   session (the core's restart after an overflow, a new session) reports a small
   number on its next reply and the meter follows; before that reply it still
   shows the old one (stale, labelled by its tooltip). History reloaded from the
   server shows the same value as live.
3. **Scale.** One pass over the loaded feed rows, memoised on the feed and the
   recipient; no request. The metrics map is already stored with each reply.
4. **Failure.** Keys absent, non-numeric, negative or NaN: ignored (same reader
   rules as the other counts). A window smaller than the tokens shows 100%, never
   more.
5. **Boundaries.** Two numbers; no text, no model output.
6. **Abuse.** A core is trusted for its own numbers, as for cost; a hostile core
   can only make its own meter wrong. The reader clamps and ignores bad input.
7. **One rule, one owner.** One reader (`readTurnMetrics`); one definition of the
   keys (the guide); each core owns its agent's mapping. The window is never
   inferred in the client.
8. **Defaults.** Unknown means hidden. The meter never falls back to a guessed
   window or to the turn's summed input.
9. **External contracts.** Verified against the real agents on 2026-10-24: ACP
   `usage_update` (OpenCode, `used`, `size`, per prompt), Claude Code stream-json
   (per-call usage on each assistant message; `result.usage` is the sum:
   13,693 + 117,553 + 19,420 over three calls against a last call of
   26 + 61,325 + 14,323), OpenCode JSON `step_finish`. Not verified: Codex's
   `turn.completed` over a multi-request turn (left out); an ACP agent other than
   OpenCode; whether another agent's `size` is its real limit.
10. **Load on users and agents.** One small element in the composer, hidden unless
    reported; nothing is added to any prompt, tool or setting.
11. **Release.** Additive keys: an old client ignores them, an old core omits
    them. Core, ACP core and client ship in any order; the meter appears when both
    ends have.
12. **Observability.** The existing reply footer tooltip can show the same two
    numbers; a core that reports nothing leaves the meter hidden, which a member
    can report. A conformance vector pins the reader.
13. **AI capability.** Next, not now: with the number available the assist seam can
    offer "continue in a fresh session with a summary" at 85%.
14. **Placement.** Section 4.

### Pre-mortem

"A month on, the meter lies." (a) A core reports the turn's summed input as
`contextTokens`: it reads 300% and is clamped to 100%, loudly wrong, found at
once; the contract and a per-core test pin "last request". (b) A window that is
not the real limit (a name-guessed one): the meter says 12% while the provider
refuses at 200k. Guard: windows come from the core's catalog, the guide forbids
guessing, and OpenCode's `size` was checked to equal that catalog. (c) Two agents
of one owner share a channel and the meter follows the wrong one: matched by the
full agent identity, with a test. (d) The meter is read as "the conversation will
fail at 100%": cores compact or restart before that; the tooltip says what the
number is, not what will happen.

## 6. Acceptance

- Reader: both keys read with the same clamping rules as the other counts; vector
  cases for a normal reply, absent keys, negative, string, window below tokens.
- Meter: hidden with either key unknown; amber from 70%, red from 90%, capped at
  100%; follows the selected recipient; shows the newest reporting reply even when
  a later reply of the same agent reports nothing.
- ACP core: a turn's last message carries `contextTokens` and `contextWindow` from
  the latest `usage_update`; unit test with the fake agent.
- A core's mapping (Claude Code, OpenCode in the local core): the last request, not
  the sum; a test from real captured output.
- Checked against the real agents end to end (the repository's local verification
  script asserts the keys on a real turn).

## 7. Landing

- (commits to be listed here)
