# Rooms that work together: any flow, built by talking, shown as a live workshop (2026-10-26)

Status: proposed (2026-10-26). Nothing is built. The decisions in §7 are open
and wait for the owner (AGENTS.md "Decision gate"); no code is written before
they are answered. Owner: the event bus (`services/realtime/events.go`, the one
dispatcher; `internal/schedule` the one match), the room tools
(`services/bridge/roomtools`), the AI task tools (`services/gateway/schedule_run.go`),
the agent door (`services/gateway/agent_door.go`). Builds on
`docs/prd/2026-10-21-room-events.md` (one envelope, subscriptions are a task's
trigger) and `docs/prd/2026-10-12-experts-and-teams.md` §7 (agents dispatch
agents). Two companion records follow: channels and threads, and organization
groups, roles and layered settings.

A member describes, in a conversation, any flow of work they run or want to
run: a content pipeline (topic, draft, edit, design, approve, publish), a
purchase request, onboarding a hire, a weekly report, a support escalation, a
software change. The agent builds it as **stations** (rooms, or channels of
one room) that hand work to each other, and then, on request, a **live
workshop page** that shows the work moving between the stations. Each station
is set up as today (its expert, skills, channels, scheduled and event tasks);
what is new is that **a room can hear another room**, the channels of one room
can hear each other, a file can travel with the work, and every event knows
what caused it. A step can be done by an agent, a person, an app or an outside
system. Nothing in the platform names any step, domain or flow: the platform
carries events with consent, tells agents how busy the room's agents are, asks
people to approve, and feeds a page what happened. The steps, their order, the
tracker, the retries and the look of the page are the member's, in words.

## 1. Why

- The owner, 2026-10-26: "我们需要一个 orchestra 能力 … 用户可以通过对话，创建出
  下面一套流水线：需求发布房间 → 任务分配房间，自动派任务给空闲 agent → 测试房间 …
  → 发布房间 … 人类审批". And: "核心就是不同 room 可以协作，room 下定义不同
  channel 也可以继续协作". And: choreography is fine as long as the goal is
  reached; the work items are whatever the member confirms in the conversation
  ("用户可以用任何组件，平台暂时不需要配").
- The owner, the same day, after the first draft leaned on software: "我要的是
  一套通用的自动化流水线，实际并不仅仅针对编码场景。用户有任何固定或者想搭建的
  流程，我都希望可以灵活创建出来。而且创建后，我还希望可以生成一个车间流转的
  特效页面". So the test of this design is a flow from any domain, and a page
  that shows it live.
- The first example the owner gave (a sample, §2.9, one of several, not a
  feature):

  ```
  requirements room ──▶ dispatch room (gives each task to a free agent)
       ▲                   │ work started ──▶ test room (prepares tests for it)
       │                   ▼
       │        developer agent: self-test, open a change
       │                   │ ready for review ──▶ code review room
       │                   │      changes requested ◀──┤ (back to the same agent and thread)
       │                   │                approved ──┘
       │                   ▼
       │        deploy to the test environment
       │                   │ deployed ──▶ test room tests it ──▶ test report
       └──── report ◀──────┘   fail: back to the same agent; pass: a person verifies
                               verified ──▶ release room ──▶ a person approves ──▶ deploy
  ```

  The owner added the review room the same day: a step can pass forward or
  send the work back. In this design a branch is **which event type the step
  sends** (`review.approved` or `review.changes_requested`, the member's
  names), and a way back is one more link; the platform gains nothing for it.
  Going back must reach **the same** agent and thread: the step's event
  carries their keys (the agent's handle, the channel id, the change's id) in
  its data, from the job event that started it (§2.2), so nothing is found
  again by a search (lens 7b).

### What exists (checked against the code, 2026-10-26)

| Needed | What the code does today | Gap |
|---|---|---|
| A room reacts to something | Event tasks: `wire.RoomSchedule` with an `event` recurrence (`internal/wire/schedules.go:114`), run by the scheduler as their creator; an AI task has `post_message`, `notify`, `app_data`, `ask_agent`, `list_agents` (`services/gateway/schedule_run.go:94`) | none |
| A room hears **another** room | Refused: a room subscription matches only `bnbloc/room/<its own id>` (`internal/schedule/event.go:186` `sourceMatches`); the only cross-room path is read-only records with an admin's consent (`services/gateway/room_sources.go`) | links, cross-room subscriptions |
| One channel hears another | Room events carry no channel; the envelope has no channel attribute (`internal/wire/events.go:199`) | a channel on the event, a channel filter |
| "The agent finished / failed" | Room event types are a closed list of three (`internal/wire/events.go:168`: file added, member joined, app data changed); job outcomes are feed events only (`agent_job_updated`) | job events (only the platform sees them) |
| An agent or AI task says "deployed" | A member can (`POST /v1/rooms/{room}/events`, `bnbloc events send`); an agent or AI task cannot | `send` for agents and AI tasks |
| Give a task to a free agent | `list_room_agents` returns `online` only (`services/bridge/roomtools/agents.go:16`); `ask_agent` posts in the parent's channel (`services/realtime/dispatch.go`); nothing says how busy an agent is | load in the listing; dispatch into a channel |
| A person approves | Approvals exist only per domain (publish review, quota, joins, borrow, room sources) | one approval primitive |
| A silent agent is noticed | Reply stall is a sliding activity window (`services/bridge/bridge.go:2410`); a turn the core reports busy with no output runs on with no signal; job retries only where no work began (`internal/store/bridge_jobs.go:187`) | a "quiet" signal (D6) |
| Loops between rooms are bounded | No self-trigger stops a room's run from triggering **its own** room only (`internal/schedule/event.go:172`); A → B → A is bounded by rate limits alone | a loop rule (D5) |
| A file travels with the work | A room's files are that room's; another room reads them only through room sources (records, admin consent), not a file | files along a link (D9) |
| Follow one piece of work across stations | An event carries no cause; the dispatcher knows only the room a run came from (`causeRoom`) | `chain` and `cause` on every event |
| A page shows the flow live | An app reads its own data and its change feed, `me`, `ai`, `notify` (`~app/…`, `docs/prd/2026-10-12-interactive-apps.md` §4.1); it cannot read the room's events | a read of the room's events for its apps (D10) |
| Set it all up by talking | `room_setup` (`services/bridge/roomtools/room_setup.go:46`) sets up **this** room; the agent door serves this room's routes only | create rooms, propose links |

## 2. What it is (the end state)

### 2.1 Addresses

An event is about a room and, where it has one, a channel of it. Threads are
channels (the channels record), so `room`, `room/channel` cover every address.
The people chat is never an address: agents never see it, and nothing it holds
becomes an event (`store.ChannelAdmitsAgents`).

- The envelope gains one CloudEvents extension attribute, `channel` (the
  channel id the event happened in, when it has one), validated by
  `wire.ValidEvent` (one owner).
- A trigger's filter gains `channel` (equals). A task with no channel filter
  takes the event from any channel, as today.

A **station** is an address and whoever does its step. Every kind of doer
already emits events, so a flow mixes them freely:

| Doer | How its step ends | Exists |
|---|---|---|
| An agent | its job ends (`room.job.*`, §2.2) or it sends an event (`signal send`) | job events and `send`: new |
| A person | they send an event from the composer or CLI, or decide an approval | member events: yes; approvals: new (§2.5) |
| An app (a form, a board) | its data changes (`room.app.data.changed`) | yes |
| An outside system | it posts the room's webhook | yes |
| A clock | a scheduled task runs | yes |

### 2.1a Every event knows what caused it

- The envelope gains two more extension attributes, set by the platform only
  (a sender's values are replaced): `cause`, the id of the event whose run
  produced this one, and `chain`, the id of the first event of that line of
  work (an event with no cause starts a chain: its own id).
- The cause travels the way `causeRoom` does today: a run carries the event
  that started it; a job dispatched or posted by that run carries it on; an
  event a job or run sends, and the job events of that job, name it.
- One chain is one line of work across every station, its splits (one request
  fanned out to five agents: five branches of one chain) and its ways back. It
  is how a page follows a piece of work (§2.6), how a loop is seen (D5), and how
  "why did this happen" is answered (§4). The member's own ids (an order, a
  ticket, a document) travel in `data`, as the flow's words say.

### 2.2 Events a room sends

Three producers, all on the existing envelope and dispatcher:

1. **Job events (platform; only it sees them).** When a job of the room ends,
   realtime emits, with source `bnbloc/room/<id>` and the job's channel:
   `room.job.replied` and `room.job.failed`. `data`: the job id, the agent
   (owner, core, provider), the requester, the dispatching job if any, the
   failure code (`job_failure.go`), the deliverable ids, and the reply text
   (D7). `subject`: the agent's handle. `room.job.quiet` (D6) when a running
   turn has shown no step for the core's idle window while still reported busy;
   it is a signal, the turn is not stopped.
2. **Sent by an agent or an AI task.** One `send` action (D8): a type (not a
   reserved prefix), an optional subject, data. It is sent **as the job's
   requester** (`bnbloc/member/<requester>`, the same source a member's own
   event has), carrying the job as its cause, audited like a member's event.
   An agent sends to its own room only; reaching another room is a link (§2.3),
   decided by people, never by the agent.
3. **Sent by a member**: as today.

### 2.3 Links: one room hears another

- A **link** says: events of these types from room A (optionally one channel
  of A) may reach the tasks of room B. Key: (tenant, A, B, link id); stored
  with the type patterns, the channel, who granted it and when.
- **Consent is A's**, because A's events (their subjects, their data, a reply
  text) leave A for B's members (D1). A link is made by an owner or admin of A;
  a member of B who is not one asks, and A's owners and admins get a card (the
  same request pattern as room sources). Either side can remove a link; A's
  admins and B's admins see it in the room's settings.
- **Subscribing stays B's**: a task in B subscribes with source `bnbloc/room/<A>`
  (the `room` source names another room), and is accepted only when a link from
  A to B covers its types (and channel). The dispatcher re-checks the link at
  delivery: a removed link delivers nothing, and the task says why in its row.
- A link carries events, never instructions: a task reads the event fenced as
  data naming its source room (`schedule.RunText`).
- **A room never acts inside another room.** It only sends events; the room
  that receives one acts through its own task, as that task's creator. A send
  back is therefore an event to the earlier room (over a link the other way),
  whose own task asks the same agent in the same channel, by the keys the
  event carries. This one rule is what keeps every step's authority, budget
  and audit in the room that does it.
- **Within one room** no link is needed: same members, same records. A task in
  channel X takes the room's events from channel Y with the channel filter. A
  small flow lives in one room, a station per channel; a large one spans rooms;
  the mechanism is the same.

### 2.3a A file travels with the work

Outside software, the work is usually a file (a draft, a contract, a design, a
report). An event names the files it hands over: `files`, a list of the
sending room's deliverable ids (and versions) in the envelope, set by the
sender (`signal send`, a member's event) or by the platform (a job event's
deliverables).

- Along a link, the receiving room's run and the agent it asks can read
  **exactly those versions** (D9), as a read of the sending room's file pinned
  to the event: the link's consent covers it, nothing else in A is opened.
- The agent sees them as it sees its own room's files: copied into its session
  on demand (`.room/` from the event, the existing deliverable copies), no new
  tool. Its work product is a deliverable of its own room, which the next
  event hands on.

### 2.4 Who is free

- `list_room_agents` (room agents) and `list_agents` (AI tasks, the usual
  dispatcher of an event-triggered station) add, per agent, `running` and
  `queued` from one owner: its jobs now
  running and waiting (D2 decides whether these count the agent's work in other
  rooms). The dispatching agent (or the AI task) chooses an agent **by its
  handle**, as today; the platform picks nobody. "A free agent" is the
  dispatcher's judgement, in the words of its room.
- `ask_agent` gains `channel`: the id of a channel of the room the task goes
  to (D3). Without it the dispatch stays in the parent's channel, as today.
  This is how one channel hands work to another, and how a dispatcher gives
  each task its own channel (or thread).
- Dispatch limits stay (`wire.MaxDispatchDepth`, `MaxDispatchFanOut`): they
  bound one agent asking another within a step. A station's run started by an
  event dispatches from depth 0 (its event's `chain` still links it to the
  work before), so a long flow is not cut by a step-level bound; the flow's own
  bound is D5.

### 2.5 A person approves

- An **approval** is a request to named people (D4) in a room: a title and a
  body (untrusted, fenced, shown as text), the request's job, the approvers.
  It is shown as a card in the room's channel and as a notification to each
  approver. Only an approver's own click decides it: approve or reject, with
  an optional note.
- The decision is a platform event in that room: `room.approval.decided`
  (`data`: the approval id, the decision, who decided, the note, the request's
  job). The room's tasks subscribe to it like any event; a link carries it to
  another room like any event.
- An approval has no expiry; it stays open until decided or withdrawn by its
  requester. Reminders are the room's own tasks.
- Requested by an agent or an AI task through the same tool as `send` (D8), or
  by a member from the composer.

### 2.5a People correct the work and change the flow, any time

The owner, 2026-10-26: "自动化过程中，人类是随时可以纠偏的，随时可以改进流程".
A flow is never a closed machine: at every moment a person can see a piece of
work, step into it, redirect it, hold it, and change any station, and the
flow learns from what people corrected.

**Correct one piece of work** (by its `chain`, never by a search):

| A person wants to | How | Exists |
|---|---|---|
| Tell the agent working on it something | Open the station's channel or thread from the item (Events view or the workshop page, by the run's address) and write; the message joins the running turn or queues behind it | steering: yes; the link from an item to its thread: new |
| Stop the step and redo it differently | "Stop the current task and do this now" in that thread | yes |
| Send it back, skip a step, send it to another station | Send an event that **continues the chain** (`cause`: an event of that chain the person can see, D12), from the composer, the Events view, or by asking the room's agent | member events: yes; continuing a chain: new |
| Hold it, then let it go | **Hold** the chain in a room: that room's tasks take none of its events until released; held events wait, in order, and are delivered on release (D11) | new |
| Decide at a point the flow did not plan | Ask for an approval there, or the station's words say "ask a person when unsure" | approvals: §2.5 |

**Change the flow while it runs:**

- Any station is changed as any room is today: by talking (`room_setup`) or in
  settings, each change audited, shown as a line and undoable. A link is added,
  removed or paused (all its events wait, as a hold does) by the people of
  §2.3.
- Work in flight is never rewritten: a running turn finishes on the words it
  started with (a person steers it if it must change now); the **next** run of
  a station uses its new words; events waiting for a removed link are not
  delivered and are listed, with the link, in the Events view, where a person
  can send any of them on by hand (continuing its chain). Nothing is moved,
  retried or dropped silently.
- Pausing a whole flow is pausing its links (a room set's links at once).

**The flow learns from corrections (AI):** every correction is a record the
platform already keeps (a steer, a stop, a chain continued or held by a
person, a rejected approval, a station's words changed). An AI task the
member sets up (a sample, content) reads them, through the Events view's data
and the consented room sources, and proposes changes to the stations' words,
links or approvals ("the editor sent 7 of 10 drafts back for tone; add the
style guide to the writer"); a person accepts each one, and it is applied
through `room_setup` as that person. The platform adds no learning engine.

### 2.6 Seeing it

- **Links** in each room's settings: incoming and outgoing, their types and
  channel, counts, the last event, drops and refusals.
- **Events** in each room: the events the room sent and received (bounded by
  the organization's retention), each with the tasks it ran and their outcome,
  and the events dropped (rate) or refused (no link), with the reason. A member
  can follow an event from the room that sent it to the run it caused.
These two are plain settings views, for finding why something did not run.

### 2.6a The live workshop page (made by the agent, not the platform)

A member says "make me a page that shows this flow like a workshop" and the
agent builds it as an **interactive app** in a room the member chooses (a
control room linked from every station, or the flow's own room), as it builds
any app today: stations drawn the member's way (machines on a floor, a subway
map, a board), work moving along the links as it happens, a station's queue
and busy agents, an item that has gone quiet, an approval waiting for a person.
The member asks for changes by talking ("show the waiting time", "make the
stuck ones red") and shares it as any work.

What the platform adds is only the **data**, through the app's own
capability, read-only (D10):

- `GET ~app/room/events?since=&chain=` and its live feed: the events the app's
  room sent and received (its own and those its links carry), with `chain`,
  `cause`, `channel` and `files`, in the room's order;
- `GET ~app/room/links`: the room's links (rooms by name, types);
- `GET ~app/room/agents`: the room's agents with `running` and `queued` (§2.4).

The app keeps whatever it wants to remember in its own data. A sample
workshop app (its own repository) is a starting point the agent copies and
changes; the platform ships no page.

### 2.7 Built by talking

- `room_setup` gains `link` (propose a link from or to this room: a card to
  the people who must consent, §2.3) and `create_room` (a new room the member
  owns, through the agent door as the member, `rbac.CreateRoom` deciding), and
  `add_schedule` takes another room as an event source. Budgets: the actions
  fit by shortening, never by raising `TestPromptBudgets`.
- Each room is then refined by talking in that room, as today. A change that
  would stop a room's linked tasks (a type no longer sent, a task removed) is
  shown by `room_setup get` as the room's links, so the agent sees what depends
  on it before proposing the change.

### 2.8 Saved and shared

A **room set** is a content component (`bnbloc.roomset.package/v1`): room
packages (`bnbloc.room.package/v1`, unchanged) and the links between them.
Install creates the rooms the installer owns and the links between them (the
installer is the owner of both sides, so the consent is theirs, at install),
with every task disabled until the installer turns it on, as a room install
does today.

### 2.9 What stays the member's (content)

The steps, roles, order, branches, retries, what "done" and "stuck" mean, the
tracker of work items (an app, a sheet, an issue tracker through connections:
whatever the member confirms), the supervisor that watches for quiet work, the
workshop page's look, and every domain's practice (for software: a branch and a
worktree per task; for content: a style guide; for purchasing: the approval
thresholds) are the stations' experts, skills, tasks and apps. **Samples** in
their own repository, from more than one domain (a flow architect expert that
designs any flow from words; a content pipeline; a request-and-approve flow; a
software change with review; a workshop app), are published to the Library and
copied and changed by whoever uses them. The platform names none of it.

## 3. Placement

| Part | Place | Why (platform: the named reason) | What an organization would swap |
|---|---|---|---|
| Link: consent, check at delivery | Platform | Consent; isolation (events leave a room) | Who may grant (room roles, D1) |
| Cross-room subscription, channel filter | Platform | The one dispatcher and match (events) | Nothing: a contract |
| `channel` attribute on events | Platform | The envelope contract | Nothing |
| `chain` and `cause` on events | Platform | Events only the platform sees (what a run caused); set by the platform, never the sender | Nothing |
| Files named by an event, read along a link | Platform | Consent and isolation (a file leaves its room, pinned to the event) | Nothing |
| The app's read of its room's events, links, agents | Platform | The member-authenticated proxy (`~app`), the viewer's own access | Nothing |
| The live workshop page | Content (an app the agent makes; a sample app in its own repository) | A view of one flow, drawn the member's way | Everything |
| Job events, quiet signal | Platform | Events only the platform sees | Nothing |
| `send` for agents and AI tasks | Platform | Identity: sent as the attested requester | Nothing |
| Agent load in `list_room_agents` | Platform | Only the platform knows jobs' states | Nothing; the choice is the dispatcher's |
| `ask_agent` into a channel | Platform | The dispatch contract (authority: the requester) | Nothing |
| Approval and its event | Platform | Identity (who decided), consent, audit | Who is asked (the room's words) |
| Hold and release a chain, pause a link | Platform | The one dispatcher; consent (who may hold) | Nothing |
| A person's event continuing a chain | Platform | Identity: `chain` and `cause` stay the platform's, checked against what the person may see | Nothing |
| An item opens its station's thread | Platform (web, `~app`) | The run's address, as the viewer may see it | Nothing |
| Learning from corrections | Content (an AI task, a sample) | A use of the records; a person accepts each change | Everything |
| Links and Events settings views | Platform (web) | Observability of the platform's mechanism | Nothing |
| `room_setup` link, create_room | Platform | The agent door: acts as the member | Nothing |
| Room set kind | Platform | The content kind contract | Its contents |
| Steps, roles, order, retries, supervisor | Content | A use case | Everything |
| Work-item tracker | Content / any component | The member's choice in the conversation | Everything |
| A domain's practice (Git worktrees, a style guide, approval thresholds) | Content (skills, experts) | A practice, not a mechanism | Everything |
| Outside systems a step uses (deploy, CRM, mail, sheets) | Component (connections, MCP tools) | Outside systems | Everything |
| Flow architect, sample flows from several domains | Content (samples, own repository) | Examples | Everything |

Name test: the platform's new nouns are link, event, channel, chain, cause,
job, approval, room set. No step, station type, domain, requirement, test,
release, deploy, work item or workshop appears in platform code.

## 4. AI capability

- **Design any flow from words** (the flow architect expert, a sample): the
  agent asks what the work is, who or what does each step, where it can go
  back, and where a person must decide; it proposes stations (rooms or
  channels), links, tasks and approvals as one plan (a drawing the member reads
  before anything is made), the member confirms, the agent builds it through
  `room_setup` as the member.
- **Make the workshop page** (the agent, as any app): from the flow's links
  and a sample workshop app, drawn the way the member asks, changed by talking.
- **Draft a subscription from words** (an assist; the step 2 of the room-events
  record, built here): "run when the dispatch room's agent finishes in #build"
  becomes source, types, channel, filter, shown before it is saved.
- **Explain a run that did not happen, or follow a piece of work** (an assist
  over the Events view and `chain`): "why did the review room not start?",
  "where is the March report now?" answer from the log (no link, dropped by
  rate, filter mismatch, the task disabled, the chain's last station).
- **Choose a free agent**: the dispatcher's own judgement over `running` and
  `queued`; no platform rule.
- **Improve the flow from corrections** (an AI task, a sample): proposes
  changes from what people steered, held, sent back and rejected; each change
  is accepted by a person (§2.5a).
- **Correct by talking**: "hold the March report", "send order 1042 back to
  pricing" said to a room's agent becomes a hold or a chain-continuing event,
  proposed as a card the person confirms (the agent acts as them, D12).
- Off switch and fallback: with the assist model off, every view and setting
  works by hand; agents and tasks run on the requester's budgets.

## 5. Design review

| Lens | Answer |
|---|---|
| Actors | A's admins (consent), B's members (subscribe; see A's events' effects), the requester an agent sends as, approvers, a borrower's agent (sends as the borrower), visitors (cannot send, subscribe or approve: `Need Chat`). Two rooms in one tenant only; a link never crosses tenants. |
| Time and lifecycle | A station changed mid-flight: the running turn keeps its words, the next run takes the new (§2.5a). A held chain: its events wait, durable, delivered in order on release. A link removed mid-flight: checked at delivery, its waiting events listed, not lost. A room deleted: its links go. An approval with a departed approver: D4. A job event for a job retried after a lease loss: emitted once, at its end. A long pipeline across a release: events are durable rows; a run claimed before a restart is re-claimed once (existing lease). |
| Scale and cost | Every job end is an event: dispatch is a store query per event (existing `ListEventSchedules`); rate limits per source and task (existing) and per link (new, the same bucket). Events log bounded by retention. Each triggered AI task spends its creator's budget. |
| Failure and recovery | A refused or dropped event is shown in Events and on the task, never silent. A failed AI task keeps `FailedRuns`. No automatic retry of a run that may have acted (existing rule). |
| Boundaries and privacy | A link is A's consent to B's audience; data, reply text and the files an event names cross only along a link, pinned to that event's versions (D9); the people chat never emits; private rooms: D1. The workshop app reads only its own room's events, as the viewer may (D10); a shared page shows its audience nothing the room's members could not see, by D10's rule. |
| Abuse and untrusted input | Event data and approval text are fenced; an agent cannot make a link or approve; approvers are named by id; loops: D5; storms: rate limits with counted drops. |
| One owner | `wire.ValidEvent` (envelope, channel), `schedule.Takes` (match incl. channel and other-room source), one link check used by subscribe and deliver, `dispatchEvent` (delivery), one approval owner. |
| Identity (7b) | A link by its id; an agent by handle (owner, core, provider); a channel by id, never by name (names are not unique, `services/room/channels.go:148`); approvers by member id; a decision by approval id. Nothing is chosen by "first", "latest" or a name. |
| Defaults and fallbacks | No link: refused with the reason. Unknown channel: refused. Missing approver: D4. No free agent: the dispatcher's words decide (wait, ask a person). |
| External contracts | CloudEvents 1.0 extension attributes are lowercase alphanumerics (`channel`, `chain`, `cause`, `files` comply). Sample-specific: Git worktrees inside a sandboxed workspace **unverified**, checked before the software sample ships. |
| Load on users and agents | Agents: one tool with two actions (D8), paid by shorter briefs; `running`/`queued` are two fields. People: links and approvals are cards they already know. |
| Release and compatibility | Additive: new event types (older readers ignore unknown types), new columns and tables by migration. Pre-launch, no compat paths. |
| Observability | Links and Events views, a chain followed end to end; audit of link grants, sends, file reads along a link and approvals; per-link counters. |
| AI capability | §4. |
| Placement | §3. |

## 6. Pre-mortem

1. **Two rooms ping-pong all night and spend the budget.** A test fails, the
   developer room retries, the test room re-tests, forever. D5 decides the
   bound; the Events view and the per-link counters show it; the owner can
   pause a link.
2. **The pipeline silently stops.** A link was removed, a task disabled, an
   agent went offline, a turn went quiet. Every refusal and drop is in Events
   with its reason; `room.job.quiet` and `room.job.failed` reach the
   supervisor task; the assist explains a missing run.
3. **Private data leaks along a link.** A reply text with a secret reaches a
   wider room. Links are A's consent, visible in both rooms, revocable; D1
   decides how a visibility or membership change of B is handled; D7 decides
   whether reply text travels at all; D9 pins files to the event.
4. **Flows outside software break on what software never needed.** A file
   that must travel, a person who is a step, a form as a step. Step 0 runs
   samples from several domains before any slice is cut, and the stations
   table (§2.1) shows every kind of doer on an existing source.

## 7. Decisions (open: the owner answers before code)

**D1. Who consents to a link, and what the consent is pinned to.**
*Chosen:* an owner or admin of A grants; B's members subscribe. *Key:* the
link id. *Pinned:* to room B. *When B changes:* proposal: a link stays when
B's members change (B's members are B's decision), and is suspended (shown,
nothing delivered) when B goes from private to shared, until A re-grants.
*Two candidates:* two rooms named alike: the link names the room id.
*Alternatives:* consent of both sides' admins; pin to B's member list (every
join suspends). Recommended: the proposal.

**D2. What `running` and `queued` count.** *Chosen:* the agent's jobs in
**every** room, as two numbers, no room names. *Key:* the agent (owner, core,
provider). *Why:* an agent busy in another room is not free. *Privacy:* reveals
only that the agent is busy. *Alternative:* this room only (hides real load).
Recommended: every room.

**D3. `ask_agent` into a channel.** *Chosen:* `channel` takes a channel id of
the room; an unknown or deleted id is refused with the reason, nothing is
created. Creating a channel is `room_setup add_channel`, which returns its id.
*Two candidates:* two channels named `build`: the id decides. *Alternative:*
take a name and create it if missing (a search, refused by lens 7b).

**D4. Approvers.** *Chosen:* the request names one or more members by id; each
must be a human member of the room who may chat; the requester (the person the
agent acts for) may be named. Any one approver decides (first click wins; the
others see it decided). An approver who leaves the room is dropped from the
request; a request with none left is shown as needing approvers and decides
nothing. *Alternatives:* all must approve; the requester may not approve their
own request (the room's words can ask for someone else already).
Recommended: the proposal; "all must approve" as a per-request choice later if
asked for.

**D5. Loops between rooms.** No fixed hop count can tell a loop from a
pipeline that legitimately goes round (develop, test, fix, test). Proposal: no
hop limit; the existing per-source and per-task rates, plus a per-link rate;
each event carries its chain (the first event and the hop count); the Events
view shows a chain's hops; and a chain that runs a set number of automatic
hops without any person's message in its rooms **pauses and asks** the rooms'
owners to continue. The number is the link's setting, chosen by the member
(no platform default, so the member must give one when a link joins a cycle).
*Alternatives:* rates only; a platform default.

**D6. The quiet signal.** *Chosen:* `room.job.quiet` once per quiet stretch,
after the bridge's idle window (today 120 s, `services/bridge/bridge.go`) has
passed with the core reporting busy and no step, again only after a new step.
The turn is never stopped (no fixed limits). *Alternative:* a longer, member
set window per task. Recommended: emit on the existing window; the supervisor
task decides what quiet means.

**D7. Reply text in job events.** *Chosen:* `room.job.replied` carries the
reply text, bounded by the envelope (the smaller fields kept, the rest named
in `_omitted`, the existing rule). *Why:* the receiving room cannot read the
sending room's records. *Alternative:* ids only, and the receiving task asks a
person. Recommended: include, under D1's consent.

**D8. Where agents get `send` and `approval`.** *Chosen:* one new room tool
`signal` with actions `send` and `approval` (one sentence brief, rules in
`room_help`), paid by shortening other briefs; AI tasks get the same two as
one tool. *Alternative:* actions on `post_progress`. Recommended: `signal`.

**D9. Files along a link.** *Chosen:* an event names files by the sending
room's deliverable id **and version**; along a link the receiving room's run
and the agent it asks read exactly those versions, for as long as the link
stands; a later version is not read through an older event. A file deleted in
A is gone for B too (positive evidence: A's deletion). *Key:* (room A,
deliverable id, version). *Two candidates:* two files named `report.docx` in A:
the id decides. *Alternatives:* copy the files into B when the event arrives
(B keeps them after the link goes; A's deletion does not follow); no files,
the step re-uploads. Recommended: read by reference, pinned to the event.

**D10. What the workshop page may read, and who sees it.** *Chosen:* an app
reads its own room's events (sent and received), links and agents, as the
**viewer** may: a member sees what the room's members see; a person opening
a shared page sees the event types, stations, times, chains and counts, never
`data`, reply text or files, unless the app's owner turns "show details to
viewers of a share" on for that app. *Key:* the app's room and the viewer.
*Alternatives:* members only (the page cannot be shared); everything to every
viewer. Recommended: the proposal.

**D11. Holding a chain.** *Chosen:* a hold is per (room, chain): set by a
member of that room who may chat, shown on the item and in the Events view
with who held it; while held, that room's tasks take none of the chain's
events; they wait in arrival order and are delivered on release (a hold has
no expiry). Any member who may chat in that room releases it. A hold does not
stop a turn already running (stop it in its thread). *Key:* (room id, chain
id). *Two candidates:* two items with the same title: the chain id decides.
*Alternatives:* a hold over every room the chain reaches (needs membership of
all of them); drop the events instead of keeping them. Recommended: the
proposal.

**D12. A person's event that continues a chain.** *Chosen:* a member's event
(composer, CLI, the Events view, or a room agent acting as them after their
confirmation) may name `cause`: an event id **of the room it is sent to**
that the person may see; the platform sets `chain` from it. A cause from
another room, or one the person cannot see, is refused with the reason;
nothing is guessed. *Two candidates:* two chains for one customer: the event
id decides. *Alternative:* name the chain directly (any id a person types).
Recommended: the proposal.

## 8. Build order (each a slice of §2, nothing replaced later)

0. **Evidence first, no platform code.** Run three sample flows from
   different domains on QA with what exists (a content pipeline with an
   editor's send-back; a request-and-approve flow with a person as a step; the
   software change with review), stations wired by their tasks' webhook URLs (a
   step's agent posts the next station's signed webhook; secrets kept out of
   room text), a dozen items each. Record per step: did it finish without a
   person, what a person had to do, the cost, where context or a file was
   lost, where an agent could not reach an outside system. The slices below
   are cut by what this shows; the webhook wiring is thrown away, it is a
   measurement, not a stage of the product.
1. The `channel`, `chain` and `cause` attributes and the channel filter; job
   events; `send` for agents and AI tasks (`signal send`); links, cross-room
   subscriptions, the per-link rate, the loop rule; holds, link pauses and a
   person's chain-continuing event; the item → thread link in the Events view;
   Links and Events views; `room_setup link`; the subscription drafting and
   "why not / where is it" assists. People correcting the work ships in the
   first slice that automates it, never after.
2. Files along a link; agent load in `list_room_agents`; `ask_agent` into a
   channel.
3. Approvals (`signal approval`, the card, the event).
4. The app's read of its room's events, links and agents; the sample
   workshop app; `room_setup create_room`; the room set kind; the flow
   architect and the samples in their own repository, each run end to end on
   QA.

## 9. Verification

- Per lens, a test that fails if its answer is wrong: two rooms with two
  members (a link granted by one, used by the other); a link removed between
  send and delivery; two channels named alike (D3); two agents of one provider
  on two cores (`running` by key); an approver who leaves; a loop that pauses;
  two files of one name (D9); a shared workshop page opened by a non-member
  (D10); a sender that sets its own `chain` (replaced); a held chain whose
  events arrive, then a release (delivered once, in order); a station changed
  while a turn runs (the turn keeps its words, the next run takes the new);
  a link removed with events waiting (listed, sent on by hand, never lost).
- `scripts/two-member-check.sh` (rooms change); each sample flow run end to
  end on QA with real agents, the member's defaults, its workshop page open,
  reported with what was not run.
