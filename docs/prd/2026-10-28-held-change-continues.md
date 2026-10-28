# A held change continues: Allow resumes the agent, and a room can allow a system (2026-10-28)

Status: decided 2026-10-28 (the owner chose A + P; the room's allowance lasts
until the person stops it).

## 1. Why

Seen 2026-10-28, in a room that publishes six services in turn through a
connector that sends from the member's computer:

1. The agent's change (a `POST`) was **held**: the connector waits for the
   person's click (`egress.go`, `class == Write && g.Reach != Write`).
2. The person clicked **Allow**. For a connector that sends from members'
   computers that only stored an **allowance** (one request, one hour,
   `held.go`) and told the person to "ask your agent again": the agent's turn
   had ended when the change was held, and nothing woke it.
3. The person typed "send them all, don't ask me". The agent read that as
   consent and retried, but the allowance is made only by the card; the next
   service is another request (another URL), so it was held again: one card
   per service, six in all.

A fourth was seen the same day: the same request produced a stack of
identical cards. The card's dedupe key (`cardKey`) includes the Review
action's `id=<held id>`, and `hold` made a new random id at every attempt, so
each retry of one request was "another card". (This is read from the code and
the screenshot; the room's own records were not read.)

Four faults, one cause each:

- **A. Allow does not continue the work.** The person decided and then had to
  say so again, in words, to the agent.
- **B. The grant is too small.** An allowance matches one request exactly
  (method, URL, body). A task that makes several changes asks several times.
- **D. A held change is not idempotent.** The same request (same member,
  room, system, method, URL and body) held again made a new held change and so
  a new card.
- **C. No step between "this once" and "this system for ever".** The card has
  one-time Allow only; "allow without asking from now on" (the connection's
  reach) is on the Review page and is for good, which is more than "this task".

## 2. What it is

### 2.1 Allow continues the agent (A)

When a held change is allowed from its card, the platform posts a message in
the room, **as the person**, to the agent whose job was held, telling it the
change is allowed and to run it again. The message carries a visible
**automatic** mark (the one a scheduled prompt carries), so nobody mistakes it
for something the person typed.

- The target is the **held job's own agent** (its owner, core and provider
  read from the job the change was held under). Never the room's first agent,
  never a lookup by name (AGENTS.md, "refer to a thing by its identity").
- The service (connections) gives the words; the platform decides nothing
  about their meaning (`CardReply.Continue`, below).
- It is posted only when the allowance exists (the change was allowed), once
  per card decision (an idempotent client id from the held change's id), and
  only if the held job is still the person's own and in the room the card
  names.

### 2.2 Allow in this room (P)

The held-change card has three actions: **Allow**, **Allow in this room**,
**Don't allow** (Review becomes a link in its text). "Allow in this room"
makes changes in that system go without asking **in this room, for the
person's own agents, until the person stops it**. It is the same decision as
Allow, with a wider reach:

- Stored where the person's other settings for that system are (their member
  scope in the connections component), keyed by organization, room and system.
- A held change is not made if the person's allowance for the room exists;
  the check is the same place the one-time allowance is spent (`egress.go`).
- It applies to **the person's own drive** only. A borrowed agent (someone
  else's computer) and a digital worker's changes (a worker's credentials are
  not the member's) are never covered by it: those still ask.
- It is **visible**: the room's connections page lists it ("Changes in Flow CI
  are allowed in this room") with **Stop**, which deletes it. The audit row of
  every change made under it says so (`room allowance`).
- Disconnecting the system drops it with the held changes and one-time
  allowances (`dropWaiting`), so a reconnect asks again.

### 2.3 One held change per request

`hold` is idempotent: the held change's id is derived from the request (the
same hash as the allowance's key: organization, system, room, method, URL,
body), so the same request held again finds the one already waiting, keeps its
id and its creation time, and the card it produces has the same dedupe key.
Within one job a retry posts no second card; across jobs a new turn may show
the card again, with the same id: deciding either decides both (the second
says it is no longer waiting).

### 2.4 Contract (additive; `component`)

- `CardReply.Continue string`: text for the platform to post as the member,
  in the card's room, to the agent of the job the card belongs to. Empty = post
  nothing. At most `MaxCardContinue` (500) bytes.
- `MaxCardActions` 3 → 4: a held change's card needs its three decisions and a
  way to look. (Review stays as a card action, so the card has four.)
- The platform learns nothing about connections: a card's reply may ask for
  a message, any component may.

The platform side of `Continue` (gateway, `handleComponentCardCall`):

1. Needs the card's room (the call names it). The job comes from the
   service's reply (`ContinueJob`): connections read it from its own held
   change, which recorded the job it was held under, so the web passes none.
2. Reads the job (`GetAgentRun`). It must be in that room, the member must be
   its requester, and it must have **ended** (a card is posted when it
   finishes with the change held; a job still running reads its own result).
   Otherwise nothing is posted and the card says what it did not do.
3. Posts through realtime's chat as the member, with the job's agent as the
   recipient and channel, the automatic mark, and the client id
   `continue-<job>-<decision>`: a second click posts nothing twice.

## 3. Decisions

1. A + P chosen by the owner (2026-10-28); the room's allowance lasts until
   the person stops it.
2. The automatic message is the person's own (the author is the member), with
   the automatic mark. Agreed by the owner ("自动发倒是可以").
3. **Assumptions to confirm:** (a) the room allowance covers the person's own
   drive only (a borrowed agent's and a digital worker's changes still ask);
   (b) it is per room and per system, not per channel; (c) a card decision made
   after the held job is gone posts no message and says so.

## 4. Placement

| Part | Where | Why |
|---|---|---|
| Posting the member's "continue" message in the room, to the held job's agent, with the automatic mark | platform (gateway + realtime) | the attested member and room, the chat's gates (membership, channel setup, borrow rules), events only the platform sees |
| What the message says, when to ask for it | component (connections) | the reply's text and whether to continue are the service's |
| The room allowance: what it covers, how it is stored, listed and stopped; its audit | component (connections) | a system's policy for its changes is the component's; the platform holds no setting |
| `CardReply.Continue`, `job` on a card call, `MaxCardActions` | platform contract (`component`) | contracts with their conformance tests |

Name test: the platform names no system, held change or connector.
Fork: an organization's own service may use `Continue` for its cards.
Replace: another service that holds actions can answer `Continue` too.

## 5. Design review

| Lens | Answer |
|---|---|
| 1 Actors | The member decides; the agent only sees a message from them marked automatic. The message acts with the member's authority and is bounded by what a typed message could do: PostChat checks membership, the channel setup and borrow rules. A borrowed agent's job is not "the member's own": the card of a borrowed drive posts no message (the job's drive is checked by connections: its card is only made for the requester). |
| 2 Time | The one-time allowance stays 1 hour. The room allowance has no end, by decision; it is listed with Stop and dropped on disconnect, on leaving the room (the row is the member's, keyed by room), and the audit shows each use. |
| 3 Scale | One message per decision; one stored row per (member, room, system). |
| 4 Failure | Posting fails (the room is archived, the agent is offline, the channel refuses): the allowance still stands and the card says "Allowed. Ask your agent again" as today. The job is gone or running: no message, same text. A second click: the client id makes it one post. |
| 5 Privacy | The message holds no change body; the card text already did. Only the requester sees the card. |
| 6 Abuse | An agent cannot make a card, it asks and the service builds it. The message's text is the service's, bounded (500 bytes); it cannot name another agent (the target is the held job's). A prompt-injected agent that makes many held changes gets one card each; the room allowance exists only because the member clicked it, and Stop ends it. |
| 7 One owner | One decision owner (`decide`) for the card and the page; one allowance check in `egress.go` for one-time and room allowances; one post path (realtime's PostChat) for the message. |
| 7b Refer by identity | The target is the held job's recorded owner, core and provider; the job id travels with the card call. No "first agent", no name match. |
| 8 Defaults | Allow in this room is a button, never a default. A card without `Continue` posts nothing. |
| 9 External contracts | Additive fields; an older service never sets `Continue` and an older gateway ignores it (the card shows the text, as today). |
| 10 Load on agents | One short message per decision, replacing the typed one. |
| 11 Release | Contract and gateway first, then connections (it starts to ask for the message and the room allowance), then web (the badge; no change needed to show the extra card button). An older web shows the card as before, with the extra action rendered like the others. |
| 12 Observability | Audit rows: `change allow` with `room allowance` detail; `continue` posted or not (and why). |
| 13 AI capability | The agent receives the continue as a normal turn; no new tool. |
| 14 Component or platform | Section 4. |

Pre-mortem:

- *The message goes to the wrong agent*: the job (not the roster) names it; a
  test with two agents in the room asserts the recipient.
- *Allow all becomes a standing hole*: it is per room and system, own drive
  only, visible, stoppable, audited; a worker and a borrowed agent are out.
- *An old card's click posts to a new job*: the client id and the job's end
  check; a card for a job that did not end posts nothing.
- *The room allowance is invisible after weeks*: the room's connections page
  lists it first, with Stop.
- *Two replicas post twice*: the post is idempotent by client id (PostChat).
