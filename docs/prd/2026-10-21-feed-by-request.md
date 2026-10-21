# The room feed by time or by request (2026-10-21)

Status: built (2026-10-21), as designed in §3. Owner: the room feed
(`web/src/views/room/MessageFeed.tsx`, `feedTurns.ts`,
`FeedRequestBlock.tsx`), member preferences (`GET/PUT /v1/my/preferences`,
the room service, `user_profiles.feed_layout`).

Decided while building:
- The switch (`FeedLayoutSwitch`, the UI kit's `Segment` in a new compact
  size) sits in a slim header above the room feed, shown once the feed has
  rows; the people chat keeps By time and has no switch.
- A request's own progress lines are steps beside its delegated work, not
  inside it (By time keeps every lead notice in the delegated block, as
  before).
- A settled job's chip sits at the foot only when no notice there already
  says how it ended (the failure notice is the server's sentence about the
  same failure).
- The head's latest step takes a second line on a narrow column; the count
  shows from a small width.
- By time moves back only notices that report how a job ended (a failure,
  a refusal, a timeout, an undelivered attachment, any new kind by
  default); progress lines, room-setup lines, publish cards and
  context-reset lines keep their arrival place (owner's decision: By time
  means time).
- The "New" divider is one rule for both views (`unreadDividerAt` over the
  render order: the start of the unread run that ends the feed), replacing
  By time's late-notice exception, which it covers.
- Checked in a browser against the 2026-10-21 room as a fixture (the local
  gateway, the room's history and job list served from the fixture): By
  time unchanged; By request three blocks (the late failure in #1's
  block, the live run with five steps and its latest step, the finished
  run with its answer), jack's and maya's chat lines after them; the head
  opens with Enter (`aria-expanded`), the open block is remembered in
  sessionStorage, the choice is saved with `PUT /v1/my/preferences`.

A member chooses how a room's conversation reads: **by time** (today: every
message and agent step in arrival order) or **by request** (each request
is one block: its agent, status, elapsed time and latest step in the
block's head; its steps inside, collapsed; its answer at the block's foot).

## 1. Why

- QA 2026-10-21: two agents ran the end-to-end tests in one room at once;
  their steps and replies interleaved and were hard to follow. The owner
  reviewed a demo (`bnbloc-demos/grouped-turns.html`) and asked for both
  views with a switch: "两种效果都可以支持，用户自己选择怎么展示".

## 2. What it is

- A switch in the feed's header: By time / By request. The choice is the
  member's (stored with their account, so every device follows it);
  default By time.
- By request:
  - a block per request (a person's message to an agent, a scheduled or
    event-triggered run, a delegated request), in the order requests were
    made; a block's head shows the agent, live status and elapsed time,
    the latest step, and the step count; clicking opens its steps;
  - the answer (final reply, deliverables, cards such as find-a-way or a
    publish card) sits at the block's foot, always visible;
  - messages that answer no request (people talking, room lines) stay in
    time order between blocks;
  - a late notice belongs to its request's block (the rule built
    2026-10-21 for By time holds here too).
- One layout owner (`feedTurns.ts`) builds both views from the same rows;
  unread and "jump to latest" work in both.

## 3. Design

### 3.1 The preference

- `GET /v1/my/preferences` → `wire.MemberPreferences {feedLayout}`;
  `PUT /v1/my/preferences {feedLayout}` stores it and answers the stored
  value. `feedLayout` is the union `FeedLayout`: `"time"` (default) or
  `"request"`; anything else is a 400.
- The room service serves it beside the member's profile (it owns
  `user_profiles`, the one row per person that follows them on every
  device and organization): a column `feed_layout` (default `'time'`),
  read and written through `UserProfileStore` (`MemberPreferences`,
  `SetFeedLayout`), memory and MySQL under the one contract suite. A row
  holding only a name or an email reads as the default.
- The gateway routes it to the room service, as a read-level action (a
  read-only viewer chooses how they read too), not rate limited.
- The web reads it through one cached query (`useFeedLayout`,
  `lib/memberPreferences.ts`); the switch shows the new choice at once and
  saves it; a failed save puts the stored choice back and says so (toast).

### 3.2 The layout (one owner: `feedTurns.ts`)

- By time is `layoutTurns`, unchanged.
- By request is `layoutRequests(feed)`, from the same rows and the same
  payload chain (`sourceSeq`, `dispatch.parentSeq`), so a live feed and one
  rebuilt from history give the same blocks:
  - A **request** is a root person's row addressed to an agent: not
    people-only, not an agent row, not a dispatch whose request is loaded.
    That covers a typed message, a scheduled or event-triggered post (the
    scheduler posts it as its owner, `scheduledId`) and a delegated request
    whose parent is not loaded (an older page).
  - Its **turn** is every row whose request it is (`sourceSeq` for agent
    rows, `dispatch.parentSeq` for dispatches), wherever it landed in time.
  - **Steps** (collapsed): progress lines (`notice: progress`), the
    delegated work (the dispatches of the `TurnBlock` By time shows,
    nested as there; the request's own progress lines stay beside it),
    and every reply batch before the last.
  - **Foot** (always visible): the last reply batch (the rows sharing the
    newest reply's sequence: one agent message can carry several parts),
    every other notice (a failure, a timeout, a setup change, a publish
    card: an outcome, never hidden), and the turn's deliverables.
  - Rows that answer no request (people-only lines, a reply whose request
    is not loaded, a person's message no agent was asked) stay single rows
    in time order between blocks.
  - A late notice is in its block's foot: the block is where its request
    is, so nothing more is needed (no "About:" line).
- The block's head is computed from the rows plus the room's job list:
  - the agent: the dispatch's addressee, else the turn's first agent row's
    label, else the request's job's agent;
  - the status (`requestStatus`): live (any job of the request not
    terminal: its status word, a pulsing dot), failed (a failed, timed out
    or cancelled job, or a `job-failed` notice), done (replied, or an
    answer without a known job), else none;
  - the elapsed time: while live, since the turn started (ticking); once
    settled, from start to finish (the job's times, else the request's and
    the answer's timestamps);
  - the latest step, while live: the newer of the turn's last step row and
    the job's current progress step (`JobProgress`), both the agent's own
    words; hidden once settled;
  - the step count: the rows inside the steps.
- The head is a button (`aria-expanded`) that opens the steps; a live
  job's stop sits beside it (not inside). The open state is remembered per
  block for the browser session (sessionStorage, per member and room).
- While a job is live its chip and progress strip (`AgentJobStatus`) sit
  inside the steps; once it settles they sit at the foot (a failure's
  reason stays visible).
- **Unread** (`unreadDividerAt`, now over the render order of either
  view): the "New" divider sits above the first row of the unread run
  that ends the feed. A new row that renders above a read row (a late
  notice moved back to its request; a reply in an earlier request's
  block) never splits the read part. Unsequenced rows (a send not yet
  acknowledged) are neutral.
- **Jump**: a row inside collapsed steps has no element of its own, so
  the block carries the sequences of its hidden rows (`data-step-seqs`)
  and a jump to one flashes the block. The scroller follows the newest
  content in both views (`useFeedScroll` observes the content's size).

## 4. Design review

| Lens | Answer |
|---|---|
| Actors | One member's choice, stored per person (`user_profiles` is global per user id, exact); another member's view never changes. Visitors and read-only viewers may choose too (read-level). The people chat has no requests and no switch. |
| Time and lifecycle | A turn's rows land over minutes; the block regroups on every row from the payload chain, so a late reply, notice or delegated reply joins its block whenever it lands, live or from history. A request whose rows are on an older page shows what is loaded; "Load earlier" fills it. Head times tick only while live (`useVisiblePoll`). |
| Scale and cost | The layout is linear in loaded rows (maps by sequence); one preference read per sign-in (cached query). No server work per message. |
| Failure and recovery | The preference read failing leaves By time (the default); a save failing restores the stored choice and says so. A job the list no longer holds (an old turn) shows no live status; its answer shows "Done". |
| Boundaries and privacy | The preference is the caller's own (no id in the path, the signed subject); `GET /v1/users/profiles` still answers only display names. No new data reaches agents. |
| Abuse and untrusted input | The server refuses any layout outside the union (empty included); the body is size-capped (`maxJSONBodyBytes`). |
| One owner | Both layouts and the divider in `feedTurns.ts`; the preference in one store method pair and one web hook; the job status words stay `agentJobStatusLabel`; the step label stays `stepLabel`. |
| Defaults and fallbacks | Default By time, server-side (column default and wire value), so a member who never chose and a failed read agree. Until the preference loads the feed shows By time; a stored By request then switches once. |
| External contracts | None: our own API; the union is generated into `wire.gen.ts`. |
| Load on users and agents | One switch, default unchanged; agents see nothing new. |
| Release and compatibility | Additive column with a default (a previous release never reads it); a new route; an older web ignores it. |
| Observability | The route's 5xx go through `obs.ServerError`; the switch's state is in the DOM (`data-feed-layout`). |
| AI capability | The block head's latest step is the agent's own words (its progress line or current step); no new model call. A summary per block was considered and left out: the answer already is one. |

**Pre-mortem.**
1. *The answer hides in the steps.* An agent that replies twice in one turn
   has its first reply folded. The last reply batch is always at the foot,
   and the step count says more is inside.
2. *A block shows the wrong agent's work.* Grouping reads only
   `sourceSeq`/`parentSeq`, the same chain By time trusts; rows without it
   stay single rows (tested).
3. *Unread disappears.* A new reply inside an earlier block gets no
   divider, by design (it would split the read part); the block's head
   shows the change of status, and the newest rows still get the divider.

## 5. Tests

- `feedRequests.test.ts`: the 2026-10-21 E2E room (two agents' turns
  interleaved, people talking in between, a late failure notice, a
  delegated request) in both views, live and rebuilt from history; the
  render order and sender runs; the request state, elapsed time, latest
  step and step count; the divider in both views; a delegated request
  whose parent is not loaded; people-only lines; a request without rows
  yet; two replies in one turn; a team lead's failure. `feedTurns.test.ts`
  keeps By time's cases (the divider now over the render order).
- `TestContractUserProfileStorePreferences` (memory and MySQL): the
  default without a row and for a name- or email-only row, the stored
  layout per exact id, the name and email untouched.
- Room service `TestMyPreferences`: default, stored, per person, 400 on an
  unknown or empty layout, 401 without a subject. Gateway
  `TestProfileRoutes`: routed to the room service, read-level, not rate
  limited.
