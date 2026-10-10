# Design — Room Participants: addressable agents + visible borrowing

Status: phases 1–3 implemented. Defines how humans and agents coexist in a
Room, how a message reaches an agent, and how borrowing another member's agent
becomes a first-class, visible capability rather than a hidden authorization
step. Phase 4 (agent↔agent assist) remains future.

Supersedes the ad-hoc bridge rule "claim only the owner's chat and drive one
fixed agent". Builds on existing pieces: the composer target selector, the
shareagent grant/access/turn backend, presence, and the bridge.

## 1. Problem

Today an agent reply only happens when the room's single bridge sees a chat
**authored by its own owner** and drives **one fixed target** (`local:codex`).
Consequences:

- A second member (jack) gets no agent reply — his messages are never claimed.
- A user with several local agents (codex + claude) cannot bring both into one
  Room.
- Every owner message triggers the agent, so humans cannot just talk to each
  other without the agent interjecting.
- Borrowing is a separate modal disconnected from the actual conversation, so
  the product's headline capability is invisible in normal use.

Root cause: the agent is modeled as "an implicit responder to the owner", not as
an addressable participant in the Room.

## 2. Model: agents are Room participants

A Room contains two kinds of participants:

- **People** — enterprise members (maya, jack).
- **Agents** — each contributed by the member who owns it, e.g. `maya/codex`,
  `maya/claude`, `jack/codex`. An agent runs on its owner's machine, driven by
  that owner's bridge and credential. Agents are never shared identities; two
  members each bring their own `codex`.

An agent participant has a stable handle in the Room:

```
<ownerId>/<provider>      e.g. maya/codex, jack/claude
```

Presence includes agents: the roster shows who — human and agent — is currently
in the Room, so borrowing and addressing have something concrete to point at.

## 3. Addressing: who a message goes to (no @ ceremony)

Sending is governed by an explicit **recipient selector** in the composer, not
by sprinkling `@` through the text.

- The composer already has a target dropdown (`codex ▾`). It becomes a
  **recipient selector** listing, grouped:
  - **Your agents** (default selection): `codex`, `claude` — zero friction, the
    common case. jack picks his `codex`, types, gets a reply. No `@`.
  - **Room** (talk to people only): messages go to the room feed and drive **no**
    agent. This is how humans chat without the agent interjecting.
  - **Others' agents** (borrowable): `maya/codex …` — selecting one enters the
    borrow path (§5), shown explicitly, never silent.
- `@name` in text is an **optional, additive** directional cue for a one-off
  aside (e.g. mention a person, or redirect a single message to another agent
  you already have access to). It is never required and never the primary path.
  If the selector says "my codex" and the text has no actionable `@agent`, it
  goes to my codex.

Precedence for one message: an explicit in-text `@<agent>` the sender is allowed
to use > the composer recipient selector > room default. Ambiguity resolves to
the selector, never to "broadcast to every agent".

Default recipient per Room is remembered per user (last used), starting at the
user's own default agent.

## 4. Claiming: which bridge drives the turn

The bridge stops filtering by author. New rule:

> A bridge claims a chat when the message's **recipient** is an agent it serves
> (its owner's `<provider>`), regardless of who sent it — subject to
> authorization (§5).

- The recipient is carried on the chat event (a `recipient` field:
  `{ownerId, provider}` or `room`), set from the composer selector.
- A bridge serves its owner's agents. It may serve **multiple** targets; it
  keeps one agent session per `(roomId, provider)` and reflows each
  independently, so `maya/codex` and `maya/claude` are separate conversations in
  the same Room.
- `recipient == room` → no bridge claims it; it is pure human chat.
- Replies are attributed to the specific agent handle (`maya/codex`), so the
  feed shows which agent answered, and multiple agents can interleave.

This one change (author-filter → recipient-match) is what lets jack get a reply,
lets one user run several agents, and stops human chat from triggering agents.

## 5. Borrowing: a visible, first-class capability

Borrowing is **not** hidden behind `@`. It is the explicit, surfaced act of
using someone else's agent, and it stays a headline feature.

### 5.1 When it triggers

- Recipient is **your own** agent → run directly, no authorization.
- Recipient is **another member's** agent → this is a borrow. The UI makes it
  obvious before and during use.

### 5.2 Surfaced in the product

- **Discoverable**: the recipient selector's "Others' agents" group lists agents
  in the Room, each tagged **Borrow** with a shared-agent glyph. Choosing one is
  a deliberate, labeled action.
- **Owner-authorized, then remembered until expiry**: borrowing another
  member's agent requires that agent's **owner** to have authorized it for the
  room (the owner's borrow-authorization panel), not a borrower self-grant. A
  borrower selecting a not-yet-authorized agent is refused with a clear reason;
  once the owner authorizes, subsequent turns in that Room do not re-prompt —
  until the authorization **expires**. Every authorization carries a TTL
  (`expiresAtMs`); an expired one reads as absent. Default TTL is bounded (24h)
  and owner-shortenable. The access snapshot (owner, owner-online, model range,
  quota, concurrency, credential-never-leaves-owner guarantee) remains available
  for accountability.
- **Model choice follows the owner**: the composer's model picker for a
  borrowed agent shows that OWNER's catalog (as their bridge advertises it,
  `GET /v1/rooms/{roomId}/agent-catalogs`, keyed by owner/core/provider),
  narrowed to the authorization's `allowedModels` when the owner restricted
  them — never the borrower's own catalog for the same harness.
- **Visible while active**: a borrowed message and its reply carry a **Borrowed
  from <owner>** chip in the feed; the reply avatar marks it as a borrowed
  agent. The room roster shows a "borrowing" indicator. The user always knows a
  borrow is happening — the opposite of a silent authorization layer.
- **Accountable**: every borrowed turn shows quota consumed / remaining from the
  snapshot; denials show the stable reason (`owner_offline`, `quota_exhausted`,
  …). Owners see borrow activity as first-class Room events.

### 5.3 Authorization mechanics (owner pre-authorization)

Borrowing is gated by an **owner pre-authorization**, not a borrower self-grant.
The authorization is created by the agent's **owner** (the authenticated caller,
never a request-body field), room-wide for one of their providers, and is
TTL-bounded. It is stored centrally in the shareagent authority under a
deterministic id (`roomauth\x00<room>\x00<owner>\x00<provider>`), so it can be
looked up by `(room, owner, provider)`, refreshed, or revoked idempotently.

- **Enforced at the driver**: a chat whose author is not the agent's owner is a
  borrow. The bridge (the sole agent driver; in production the recipient
  owner's own bridge, i.e. the owner's trust domain) consults the shareagent
  authorization before driving and **fails closed** — a missing/expired/revoked
  authorization drives nothing and posts a visible
  `owner_not_authorized` denial to the room.
- **Checked on send**: the borrower's composer also consults the authorization
  before sending, so an unauthorized borrow surfaces immediately instead of
  being sent into a fail-closed drive.
- **Authorization TTL**: every authorization carries `ExpiresAtMs` (bounded
  default 24h, capped 7d, owner-shortenable). `now >= ExpiresAtMs` reads as
  not authorized, so access lapses automatically and must be re-authorized.
- **Owner control surface**: the owner authorizes/revokes each of their
  providers for the room from the borrow-authorization panel.
- Credentials never transit; only model/capability/turn ride to the owner
  daemon. Unchanged from the shareagent contract.
- **(Later) real-time approval**: an owner approving each borrow request on the
  spot (vs. pre-authorization) is deferred; the pre-authorization grant is the
  current model.

So borrowing is the **authorization + attribution layer** for "use an agent that
isn't yours", and it is deliberately visible. Addressing (§3) chooses the
recipient; borrowing governs and surfaces the cross-member case.

### 5.4 Quota requests (borrow billing, 2026-10-08 F3)

A room authorization may carry a run quota (`runQuota`). When a borrower has
used it up (or wants more), they ask the owner instead of the owner
re-authorizing from scratch. Same fail-closed identity rule as §5.3: the
requester and the deciding owner are always the authenticated caller
(`X-Bnbloc-User`), never a body field.

- `GET .../shared-agents/{target}/authorization?owner=` additively reports
  `runQuota`, `runsUsed`, `quotaExhausted` for a run-limited authorization;
  the borrower's composer refuses an exhausted borrow and offers
  **Request more**.
- `POST /v1/borrow/quota-requests` `{roomId, owner, provider, requestedRuns?,
  note}` — `provider` is the grant discriminator (`<core>:<provider>`). The
  requester must be a room member other than the owner, and the owner must
  hold a live, run-limited authorization (409 otherwise). At most one pending
  request per (room, owner, provider, requester): a duplicate is 409 with the
  open request.
- `GET /v1/borrow/quota-requests?role=owner|requester[&status=][&roomId=]` —
  the caller's side only.
- `POST /v1/borrow/quota-requests/{id}/approve` `{runs?}` (default: the
  requested amount; one of the two is required, 1..10000) and `.../decline`.
  Only the request's owner may decide (403 otherwise). Approving **raises** the
  authorization's run quota by `runs` and keeps its usage history (it never
  goes through the re-authorize upsert, which resets usage); a revoked or
  expired authorization cannot be raised (409; the request stays pending).
- The owner is notified of a new request (notification emitter), sees a badge
  on the in-room handshake icon, and decides from the authorization dialog or
  Team → Usage & billing.
- Usage reports over recorded borrow turns:
  `GET /v1/borrow/usage-report?as=owner|borrower` (caller's side) and
  `GET /v1/tenant/borrow/usage-report` (org admins), grouped by day/month,
  JSON or `format=csv`; `cost`, `inputTokens` and `outputTokens` appear only
  where the recorder supplied them. The owner's bridge reserves each borrowed
  run before driving it (`POST .../authorization/reserve`; the reservation
  counts against the run quota at once, so concurrent borrows cannot exceed
  it — R8) and settles it once the drive ends
  (`POST .../authorization/record` with the reservation id), carrying the
  usage the turn's replies reported (model, tokens, cost — each only when the
  core reported it). An unsettled reservation expires after 30 min and still
  counts as a run.

## 6. What each surface shows

- **Roster / presence**: people + agent participants (`maya/codex`,
  `jack/claude`), with online state; borrowable agents flagged.
- **Composer recipient selector**: [ Your agents · Room · Others' agents
  (Borrow) ], remembered per user.
- **Feed**: each agent reply labeled with its handle; borrowed turns carry a
  "Borrowed from <owner>" chip; human-only messages have no agent involvement.
- **Borrow sheet**: access snapshot + consent, on first cross-member use.

## 7. Scope / phasing

1. **Claim by recipient** (unblocks jack + multi-agent + quiet human chat):
   bridge claims by recipient-match not author; composer sends a `recipient`;
   bridge serves multiple targets with per-`(room,provider)` sessions; replies
   attributed per agent. Single-host test: a member's agents reuse their one
   local core.
2. **Agents in presence/roster**: register agent participants; recipient
   selector lists real in-room agents; feed labels replies by handle.
3. **Borrow on the send path**: selecting another's agent runs the borrow sheet
   (existing shareagent backend) with visible chips + accounting; optional
   per-Room pre-authorization.
4. **Agent↔agent assist**: resolved by
   `2026-09-11-agent-collaboration-boundary-decision.md` — bnbloc does not
   implement agent orchestration; it provides addressing + authorization +
   visibility, and orchestration stays agent-side. A later narrow, non-coupled
   extension point (parse/authorize/forward/show only, no orchestration) may be
   added under its own design.

## 8. Non-goals

- No shared/multiplexed single agent identity; each agent belongs to one owner.
- No mandatory `@` routing; `@` is an optional aside only.
- No credential transfer; borrowing keeps the shareagent guarantee.
- Agent-to-agent autonomous collaboration is deferred (phase 4).

## 9. Decisions captured

- Default is **no `@`**; the composer recipient selector decides delivery; `@`
  is optional directional sugar.
- **Default recipient is an agent** (the user's own default agent); "Room /
  people-only" is an explicit **opt-in** choice, never the default.
- Agents are **per-user**, not shared; a Room can hold many agents from many
  members.
- **Borrow stays a visible, first-class capability** — it is the authorization
  and attribution layer for using another member's agent, surfaced in the
  selector, a consent sheet, and feed chips, not hidden behind addressing.
- **Borrow requires owner pre-authorization, time-limited**: the agent's owner
  authorizes room-wide borrowing of a provider (owner = authenticated caller,
  never a body field); a borrower can never self-authorize. Enforced at the
  bridge (fail-closed, visible `owner_not_authorized` denial) and checked by the
  borrower's composer on send. Each authorization carries a TTL (`ExpiresAtMs`);
  within the window later turns don't re-prompt, after it access lapses and must
  be re-authorized. Real-time per-request owner approval is deferred.
