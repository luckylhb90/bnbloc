# Never a dead end: when an agent can't, propose what would help (2026-10-20)

Status: slice 1 built, slice 2 proposed. Owner: the assist seam (a gate
on an assist, a `capabilities` context), one built-in assist, the reply's
card in the room.

Status (2026-10-20): built (slice 1, §5.1). The reference is
`docs/architecture/ai-assist.md` ("The gate", the capabilities context,
"Find a way"). Decided while building:
- **The gate** is `gate: {question, threshold}` on the assist kind (a
  one-line question of at most 500 characters; a threshold above 0 and at
  most 1), checked by `contentpkg`. A run with `gated: true` resolves the
  task's context, asks the question with the run's user message (the
  fenced blocks) as the decision input through the gateway's one decision
  call (`callDecision`, which `runDecision` now uses too), and answers
  `{skipped: true, reason: "below" | "no-gate"}` without a chat model; a
  declined question counts as below. A gated run of a gateless task is a
  400. The gate's call is audited as a decision of the task
  (`find-a-way@1.0.0`); the run spends the assist route's rate and budget.
- **The turn.** The request names the reply (`replySeq`, its room
  sequence); the room context then holds that reply and the request its
  payload's `sourceSeq` names (`roomlog.Entry.SourceSeq`), each cut to
  4000 characters, in a `<turn>` block. Building it found the room's
  recent messages were the page's oldest, newest first
  (docs/bugs/2026-10-20-assist-recent-messages-oldest.md); fixed.
- **Capabilities** list the executable components the member catalog
  lists (`memberComponents.listed`, now the one rule `GET /v1/components`
  and this context share), the organization's platform agents
  (`core/provider`), the room's agents by handle (realtime's roster,
  `roomRoster`, which a schedule run's `list_agents` now reads too) and
  how many other people the room has; at most 40 components and 20 agents,
  a list past its bound saying how many it left out. `hasOwnAgent` is the
  client's word: an agent of the member's own (not a platform one) among
  the room's agents.
- **`find-a-way`** (1.0.0): gate "Does the reply leave what the member
  asked for undone, or say the agent cannot do it?" at 0.7; `fixes` is an
  `items` field of `{action, target, why}`: the existing field shapes
  hold it, no extension. A web test holds the package's action and cause
  lists to `deadEnd.ts`'s.
- **The card** runs gated when the member's own turn (not a dispatched
  one) reaches `replied` live in the channel they look at, once per reply
  (a session memo; a gate's "skipped" is remembered too, so the menu's
  entry then runs it ungated). The menu entry is an icon on the reply's
  action row. Install confirms on the card, then the member catalog's
  install, then the component's settings when it needs any (there is no
  separate install dialog: the Components page installs from its card).
  Own agent picks the member's own agent and puts the request in the
  composer, or opens the Agents page (the desktop download in a browser,
  the local agents in the desktop app).
- **Report** had no path for a member to reach the admins: it is
  `POST /v1/rooms/{roomId}/report` (a member who may post; at most 1000
  characters; the same note about the same reply once) through the one
  notification emitter as a new type, `room_report`, to the room
  organization's org admins, who can turn it off (`roomReports`, a new
  preferences column, on by default). The member sees on the fix whether
  it was queued for anyone.
- Not built in this slice: counting dismissals per package (§4.1).

When an agent's reply leaves the member's request undone ("I can't reach
that system", "I don't have the file", "I can't see your app"), the room
tells the member at once what would get it done: a component to install,
another agent to ask, the file to attach, their own agent to use, the
request said another way, or the gap reported to their admins. AI proposes,
the member decides; nothing changes until they click.

## 1. Why

- QA 2026-10-20: a member's two rooms both failed their goals. The agent
  said, in its own words, that it could not reach the data, did not have
  the sample, could not see the app. Each time the member was left with a
  polite "I can't" and no next step, tried again the same way, and gave up.
  Every one of those misses had a way out the platform knew about: a
  browser component that opens the real app, the member's own agent with
  their files and sign-ins (the agent they used was a borrowed one,
  sandboxed on someone else's computer), a file attached again, a
  colleague who owns the data.
- The owner's goal: bnbloc solves the member's problem, whatever it is, or
  says exactly what would; never "nothing can be done".
- What exists falls short: the agent knows only its own tools, not what the
  member could install or whom they could ask; the assist seam proposes
  only when the member asks for it; the decision model (cheap, calibrated
  yes/no) has no web caller yet.

## 2. What it is (the end state)

### 2.1 Noticing: a gate on an assist

- An assist package may declare a **`gate`**: one predicate question for the
  decision model and a threshold. `POST /v1/assist/{task}` with
  `"gated": true` asks the gate first, with the same context the assist
  reads; below the threshold it answers `{"skipped": true}` and no chat
  model runs. Generic: any assist that should run on its own (after a
  turn, after an upload) uses it; the platform names no question.
- No decision model for the caller: a gated call answers `skipped` with
  the reason `no-gate`; the manual entry (§2.3) still works.
- Cost and audit are the decision model's and the assist's, as today (the
  member's budget, a row without content).

### 2.2 Proposing: the `find-a-way` assist

- Built-in content, `bnbloc.assist.package/v1`, editable like any assist.
  Gate: "Does the reply leave what the member asked for undone, or say
  the agent cannot do it?" Context: the turn (`room` with the request and
  the reply) and the new **`capabilities`** context.
- **`capabilities`** (one owner in the gateway's assist context): what the
  member could use and does not here — the components listed to them
  (name, kind, one line, installed or not; from the catalog of
  `GET /v1/components`), the agents they can reach in the room (platform
  agents, the room's agents, whether they have an agent of their own on
  this device: the client says so, as a flag), and whether the room has
  other members. Names and one-liners only; no settings, no people's
  names outside the room.
- Output: `cause` (one of: `access`, `missing-file`, `capability`,
  `unclear`, `agent-error`, `other`), `summary` (one sentence in the
  member's language: what stopped it), `fixes` (up to three, each
  `{action, target, why}`).
- **Actions** are a closed list owned in one place (the web's
  `deadEnd.ts`, validated there against the context the assist was given:
  a target that is not in it is dropped):
  - `install`: a component from the catalog → its install dialog;
  - `ask-agent`: an agent the member can reach → the same request sent to
    it in this room;
  - `attach`: a file the turn needed → the attach picker, with the hint;
  - `own-agent`: use their own agent (their files, sign-ins and network)
    → the desktop download and local-agent setup, or switching to it when
    it is there;
  - `rephrase`: the request said another way → the composer prefilled
    (the `enhance-prompt` assist's text);
  - `report`: the gap, to the organization's admins, as a notification
    with the room link (the member reviews the text first).

### 2.3 Showing it

- When a turn the member asked for settles in their open room, their
  client calls `find-a-way` gated. A proposal shows as a card under the
  reply, **for that member only** (not posted to the room): the summary,
  then the fixes as buttons; "Dismiss" hides it.
- Manual: every agent reply's menu has "Didn't work? Find a way", the same
  assist without the gate. With no assist model, the menu shows the fixed
  fallback: attach a file, ask another agent, rephrase.
- Once per turn: a card is never computed twice for the same reply.

### 2.4 AI

The whole feature is AI: noticing (the gate), the cause, the fixes. The
buttons are the manual part, and the fallback with the model off.

## 3. Design review

| Lens | Answer |
|---|---|
| Component or platform | Content: the assist (gate question, prompt, fields) is a built-in package anyone may change. Platform: the gate (generic) and the `capabilities` context, which only the platform can answer. |
| One owner | The gate in the assist route; `capabilities` in the assist context; the action list in `deadEnd.ts`. |
| Boundaries and privacy | Runs as the requester, on their own turn, in their budget; the card is theirs only. The context holds component and agent names, never settings or people outside the room. Request and reply are fenced as data. |
| Load on agents | None: no new tool, no new rule; the agent says "I can't" as it does today. |
| Load on users | A card appears only when the gate says the request was left undone; one click to act. |
| Cost | One decision call per settled turn the member asked for (cheap); the chat model only past the gate. |
| AI capability | Notices, explains and proposes; the member decides every action. |
| Release safety | Additive: a field on the assist kind, a context, a card. No decision model: nothing runs on its own. |

## 4. Pre-mortem

1. **Cards on good replies.** The gate's threshold is the package's;
   dismissals are counted per package so the owner can tune it.
2. **Proposing what cannot work.** Targets outside the context are
   dropped; an install still asks for its consent and settings.
3. **Cost at scale.** Only turns the member asked for, once each, gated by
   the cheap model.
4. **A misleading reply turns the fix against the member** (prompt
   injection in the reply). The reply is fenced; actions are a closed list
   with targets from the platform's context; each one is a click.

## 5. Build order (slices of §2)

1. The gate on assists, the `capabilities` context, `find-a-way`, the card
   and the manual entry, with the six actions.
2. The outcome review room (`docs/prd/2026-10-20-rooms-as-components.md`)
   reads the cards' causes and what the member did with them as part of
   the `runs` source. Not yet: the `runs` source (that PRD's §7) carries
   each run's outcome and failure sentence, but the card's cause and the
   member's choice are not persisted anywhere (the card's result is a
   session memo in the web), so they are not in its records.
