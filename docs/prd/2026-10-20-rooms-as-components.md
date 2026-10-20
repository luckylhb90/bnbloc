# Rooms as components, and a room that reviews outcomes (2026-10-20)

Status: slice 1 built (the room kind, save and install, templates folded
in; §5, §6); slice 2 built (the `runs` source: consent, transparency,
`read_records`; §7), with each record's turn (the run's last reply, the
request's file names) added on 2026-10-20 (§7, "The record's turn"),
and the grant pinned to a private room's owner and watched (members,
ownership, changed tasks) plus the AI check of a package's text before
it is saved, from the review of 2026-10-20 (§7, "A grant pinned to a
person"; §6, "Checked before it leaves the room"); slice 3 withdrawn as a platform slice: the platform ships no review room,
a member builds one on slices 1 and 2, and a sample is published to the
Library from its own repository (§2.2, §5). Owner: the content component
contract (a new kind), the schedule runs' sources, the room's own
content.

A room's whole setup (its expert, skills, prompts, tools, apps, channels and
scheduled AI tasks) can be saved as a component and installed as a new room.
A room built this way can review the organization's conversations every
day, judge whether each reached its goal, and send what failed and what
would have helped. A member builds it in a room; nothing about it is in the
platform.

## 1. Why

- QA 2026-10-20: a member's two rooms both failed their goals (data the
  agent could not reach, a sample file that never arrived, an app the
  agent could not see). Nobody noticed until the owner read the rooms by
  hand. Every miss is a product lesson, and today they are found by luck.
- The owner's direction: the review is not a platform feature; a member
  builds it in a room, and a room can itself be a component.
- What exists falls short:
  - Room templates (`internal/wire/schedules.go` `RoomTemplateBody`) keep a
    description, channels, prompt packs, suggested agents, members and
    schedules, in their own table, outside the Library. They leave out the
    room expert, skill packs, tools and apps.
  - AI schedules run on a clock with `post_message`, `notify`, `app_data`
    and `ask_agent`, but read only their own room's last 30 messages.
  - No consent model lets one room read others (the withdrawn room-memory
    PRD: such reading is the organization admin's opt-in, off by default).
  - Nothing records whether a conversation reached its goal.

## 2. What it is (the end state)

### 2.1 A room is a component

- A content kind `bnbloc.room.package/v1` holds a room's setup:
  description, welcome and quick prompts, channels, the room expert and the
  skill and prompt packs it turns on (`requires`), the MCP tools it needs
  (`requires.mcp`), its apps (work snapshots), its scheduled AI tasks (their
  prompts and recurrence) and the **sources it asks for** (§2.3).
- **Save as component**: a room's owner saves the room from its menu; the
  agent can propose it ("this room could be a component others use").
  The package lands in their Library like any content.
- **Install** creates a room the installer owns, turns on its packs,
  installs its apps and creates its schedules disabled; the room proposes
  turning them on, and the installer decides. A source it asks for waits
  for its consent (§2.3).
- Room templates become this kind (one owner): their table, routes and
  views go, pre-launch, no compatibility.

### 2.2 An outcome review room (a member builds it; the platform ships none)

- **The platform ships no review room.** How a review room judges, what
  it keeps, its causes, columns, schedule, digest or follow-up are its
  owner's decisions, made in their room with AI; the platform names none
  of them (no built-in room package, expert, app, cause list or task
  mechanism for it).
- **A member builds it on the base capabilities**: the room package kind
  (§2.1), the `runs` source with the organization admin's consent and the
  schedule-run tool `read_records` (§2.3), and what any room has: an AI
  schedule whose prompt is the review (what to judge, the causes, the
  digest, whom to notify), an app an agent makes on request (say, a table
  of judgments in the room's own app data), the schedule tools
  `app_data` (reads only: list, find, get), `post_message`, `notify` and
  `ask_agent` (a room agent, whose room tool `app_data` also writes).
- What such a room can do, as its owner chooses: every day, read the
  day's conversations within its consent; for each, the goal in the
  member's words, reached or not with the evidence, why (a record carries
  the request, its files by name, the agent's last reply and the outcome,
  §7), and what would have helped; post a digest and notify the people it
  names; keep the judgments in its app through a room agent it asks; a
  person marks a wrong judgment there, and the next run reads the marks
  (`app_data` find). A schedule run cannot write app data itself today:
  a run's `app_data` reads only (schedule_run.go).
- **A sample** of such a room is offered as a room package made only with
  these capabilities, kept in its own repository and published to the
  Library like any member's content: anyone copies it and changes its
  prompt, its schedule or anything else. It is never compiled into the
  platform or its built-in content. Published 2026-10-20:
  `github.com/luckylhb90/bnbloc-sample-outcome-review` (`outcome-review`
  0.1.0: a welcome, two quick prompts, one daily AI task in plain words,
  the `runs` source with its purpose), shared with every organization.

### 2.3 Reading other rooms: a consented source

- A room package may ask for a **source**, with its **purpose**: a kind
  of organization record its scheduled tasks may read. The first is `runs`: agent conversations
  of the organization (request and reply text, the request's files by
  name, the agent, the outcome status, the room and time).
- Consent is the organization admin's, per installed room: what it reads
  (all rooms, rooms that opt in, or none of private rooms), for which
  purpose (the package's stated purpose is shown). It can be withdrawn at
  any time.
- **What a reading room reads, its members read**: a grant is only for a
  private room (invite only), and the admin sees its members (names,
  roles) before granting. Made shared (anyone could join it from the
  plaza), its grant is withdrawn; a member who joins after the grant is
  told to the admins, naming the room and the person (the grant holds).
- **A grant is pinned to a person**: "this room, read by <its owner>".
  Only that person's scheduled tasks read it, while they own the room; a
  transfer of the room sends the grant back to waiting. A room admin's
  task reads nothing.
- **The tasks the admin consented to stay consented**: a task of that
  person created or changed after the grant is told to the admins with an
  AI explanation of the change against the purpose; the console shows it
  until an admin acknowledges it or grants again. It notifies, never
  blocks.
- Transparency: members of a reviewed room see, in its info, that the
  organization reviews conversation outcomes, which room does and whose
  tasks read.
- The platform adds one schedule-run tool, `read_records(source, since,
  until)`, answering only what the consent allows, counted against the
  run's budget and audited. It knows sources, never goals or reviews.

### 2.4 AI

A review room's AI is its own: its schedule's prompt judges the goal, the
outcome, the cause and the fix. The platform's AI here is the grant
dialog's `explain-source-use`, the same explanation of a task changed
after the grant (§7), and `describe-room`'s check of a package's text
before it is saved (§6). AI proposes and people decide: a schedule runs
only once its owner turns it on, a source is read only once the admin
grants it, a changed task is told and not blocked, a package is saved
when its member says so.

## 3. Design review

| Lens | Answer |
|---|---|
| Component or platform | A review is a member's content (a room and its package). The platform adds a kind, an install flow, a consented source and `read_records`, none of which names goals or reviews; it ships no review room (a sample lives in its own repository, published to the Library). |
| One owner | Room templates fold into the room kind; one consent owner for sources; whatever a review room keeps lives in its own app data. |
| Boundaries and privacy | Reading other rooms needs the admin's consent per installed room, shown to the members it covers. The review is about whether the product served people, not about ranking them: the digest groups by cause and room; the requester's name is shown only to the organization's admins. What the reading room reads its members read: a grant is for a private room only, its members are shown at the grant and each new one is told; a grant is pinned to the owner it was granted to. A saved package is checked by AI for secrets, internal links, names and private data before it leaves the room. |
| Load on agents | No new agent tool; `read_records` is a schedule-run tool. |
| Load on users | Install one room; turn its schedule on. |
| Cost | One model call per conversation judged, in batches, within the organization's assist budget; the room can sample. |
| Release safety | Additive kind and tool; templates move to the kind with no data kept (pre-launch). |

## 4. Pre-mortem

1. **People feel watched.** Consent is the admin's and visible to members;
   the purpose is the product's, and the digest leads with causes, not
   names.
2. **Wrong judgments.** A record carries the request, its files and the
   agent's last reply, so a judgment can cite its evidence; a room that
   keeps judgments in a table app lets a person mark one wrong, and its
   prompt can read the marks next time (the room's choice, not the
   platform's).
3. **Cost runs away.** `read_records` is bounded per call and per run
   (§7); the rest is the room's prompt (batches, a cap, sampling) within
   the organization's budget.
4. **A room package asks for more than it needs.** A source is shown with
   its purpose at install; the admin grants or not.
5. **The grant outgrows its consent.** The owner shares the room, makes
   a friend a room admin, hands the room over or rewrites the task after
   the grant: shared withdraws, only the pinned owner's tasks read, a
   transfer pauses, a new member and a changed task are told to the
   admins with what changed (§7).

## 5. Build order (slices of §2)

1. The room kind: save a room as a component, install it as a room;
   templates folded in. **Built** (§6; commits 435ed025, 74124c84).
2. The `runs` source with consent, transparency and `read_records`.
   **Built** (§7; commits f8cc0fbc, 06ef1194).
3. ~~The outcome review room as built-in content; the digest and fixes as
   tasks.~~ **Withdrawn** (2026-10-20, the owner: the platform provides
   base capabilities only). A member builds a review room themselves (a
   room, its AI schedule's prompt, any app they ask an agent to make) on
   slices 1 and 2; the platform ships none, nor a cause list, columns, a
   digest or a task mechanism for one. What judging needed from the
   platform was the record's turn: `read_records` now gives each record
   the run's last reply and its request's file names (§7, "The record's
   turn"). A sample room package, made only with these capabilities, is
   kept in its own repository and published to the Library like any
   member's content (§2.2).

## 6. Slice 1 as built

The reference is docs/architecture/content-components.md, "Room
packages". What the slice decided, against the code:

- **The manifest** (`contentpkg/room.go`, one validator):
  `component.json` alone (and an optional `README.md`) with the room's
  `description` (the envelope's, within a room's 500 characters: it
  becomes the room's), `welcome` and `quickPrompts` (an expert's rules),
  `channels`, `schedules`, `requires.expert` / `skills` / `prompts` /
  `mcp`, `apps` (works by `name@range`) and `sources`
  (`wire.RoomSources`, today `runs`). Its bounds are rows of
  `contentpkg.Limits()` (so the builder's format skill lists them).
- **Schedules a package holds** are AI tasks and notices to the room's
  members that recur. A schedule that posts as a person or drives an
  agent, a notice to named people or a group (a package names no people)
  and a one-time run are refused by the validator and left out of a draft
  with a line saying so; a notice's link (it points into the saved room)
  is dropped, said too. The text and recurrence are checked by the
  schedule rules' owners (`schedule.CheckText`, extracted from realtime's
  route so both ask one function; `schedule.Validate`).
- **Apps** are captured only when they came from a work: the copy's
  `app-copy` use names the work. An app made in the room is left out with
  "publish it as a work to include it". Install copies each through
  `copyApp`, the copy "Use this app" makes (extracted, one owner).
- **Save** reads the room through the backends' routes as its owner or
  admin (`GET /v1/rooms/{roomId}/component-draft`) and saves through the
  content form: no second save path. A part the saver cannot see (another
  member's private pack pinned in the room) is left out, since nobody else
  could install it. "Fill with AI" is the built-in `describe-room` assist.
- **Install** (`POST /v1/content-components/{id}/create-room`) resolves
  every reference for the installer (`contentuse.Resolve`, the existing
  `name@range` rule, now exported) and reports what they may not use;
  schedules are created disabled through realtime's own route; a failure
  once the room exists deletes it. The welcome and quick prompts reach the
  installer on the room's first visit (`RoomStartCard`, from the install
  reply), like the template follow-ups did; a durable room welcome is not
  in this slice.
- **Sources** are declared in the save dialog and shown at install and on
  the new room's card as asked for, never granted (slice 2).
- **Templates folded in.** `services/room/templates.go`,
  `template_packs.go`, `internal/store/room_templates.go`, the
  `/v1/room-templates*` routes and gateway routing, the web's template
  tab, dialogs, start card and their tests are deleted; the Library's tab
  is "Rooms", the room header's button "Save as component", Plaza's "From
  a room component…". Suggested agents and default members are not
  carried: a package names no people (members invite them).
- **Release safety.** A migration only adds (AGENTS.md): this release
  stops reading and writing `room_templates`; it stays in `schema.sql`
  (its columns allowed as retiring in `writeOnlyColumnAllowed`) and a
  release after the one that ships this drops it (`retiredTables`). No data is kept.
- **Checked before it leaves the room** (added 2026-10-20, review). A
  package's text (display name, description, welcome, quick prompts,
  scheduled tasks, channel names, sources' purposes) can carry what its
  room should keep: a token, an internal link, a colleague's name, a
  customer's details. The one `describe-room` assist (1.1.0) also answers
  `findings`, items of `field` (the text's key in its `<text>`'s
  `package`), `quote` and `why` (`secret`, `internal-link`,
  `personal-name` or `private-data`, then a sentence). The save dialog
  runs it on Save when the text changed since the last check (and with
  "Fill with AI"), lists what it found with "Fix" (focus the field to
  edit it; a scheduled task or channel the dialog does not edit is left
  out, a quick prompt removed) and "Ignore", and the button reads "Save
  anyway": saving stays the member's decision; an AI failure is said and
  the next Save saves unchecked. The helpers are pure
  (`web/src/lib/roomReview.ts`, node-tested; the kinds held to the
  package's item description by a test).
- **Design review notes.** Actors: the saver must be the room's owner or
  an admin; the installer owns the new room and its paused schedules; the
  package's author is never added. Boundaries: no people, messages, app
  data or grants travel in a package. Failure: install is all or nothing
  for the room, reported per part for what the installer may not use.
  Cost: one assist call per "Fill with AI"; install does a bounded number
  of backend calls (20 channels, 20 schedules, 10 apps, 32 requirements).

## 7. Slice 2 as built

The reference is docs/architecture/content-components.md, "Room
packages" ("Sources are asked for with a purpose"). What the slice
decided, against the code:

- **An ask carries its purpose.** A package's `sources` are
  `[{name, purpose}]` (`wire.RoomSourceAsk`; the purpose required, at most
  `wire.MaxRoomSourcePurpose` characters, a row of `contentpkg.Limits()`),
  replacing slice 1's list of names; the save dialog asks for the purpose
  when a source is ticked. Installing records the ask for the new room
  (`room_source_grants`: the package's id, name and version, the purpose,
  who installed it and a random alias key), waiting; the install reply
  and the first-visit card say it waits for the admin.
- **The consent's one owner is `services/gateway/room_sources.go`.** The
  organization admin grants an ask a scope or withdraws it (Organization
  → Room sources; `PUT`/`DELETE /v1/tenant/room-sources/{roomId}/{source}`,
  `manage_org`), audited as `wire.AuditKindSource`. A withdrawal empties
  the scope: the ask stays (the admin can grant it again; its alias key is
  stable for the ask's life) and the next read refuses. Scopes:
  - `all`: every room of the organization;
  - `none-private`: every room whose visibility is shared. "Private" here
    is the room's own notion (`store.VisibilityPrivate`, invite only); a
    room of unknown visibility counts as private;
  - `opted-in`: rooms whose owner opted in (room settings → Organization
    reviews; `PUT /v1/rooms/{roomId}/sources/{source}/opt-in`, the owner
    only, audited). The opt-in is the room's, per source, for every
    `opted-in` grant of the organization: the header tells its members
    which rooms read it.
  `sourceCover.covers` is the one rule; the reading room's own records are
  never covered (its members read them already).
- **Transparency.** `GET /v1/rooms/{roomId}/sources` (any member, a
  visitor too) answers the room's asks and where each stands, the rooms
  whose grants cover it, and its opt-ins. The room header shows a note
  beside the platform agents' one: "Outcomes reviewed" opens "This
  organization reviews conversation outcomes in <room name>", linked when
  the member is a member of the reviewing room (only its name otherwise);
  a reading room shows "Waiting for your organization admin" (or that it
  reads), its purpose, and "Ask admin" for its owner or admins.
- **Ask admin** goes through the admins' one notification path,
  `notifyOrgAdmins` (extracted from the room report, `wire.NotificationRoomReport`:
  an admin can turn it off), linking to the console, at most once a day
  per room and source; a granted ask answers 409.
- **`read_records(source, since, until, next)`** is a schedule-run tool
  (`services/gateway/schedule_records.go`), never an agent tool. It is
  offered to a room that asks for a source (so a run whose ask waits can
  say so in its summary) and answers only:
  - within the grant at the moment of the call (no grant, waiting or
    withdrawn: refused);
  - for a schedule of the person the grant is pinned to, while they own
    the room (below, "A grant pinned to a person"; a room admin's or a
    member's own schedule in it reads nothing);
  - finished runs (the terminal statuses) in `[since, until)`: RFC 3339
    times or dates in the schedule's timezone, by default the 24 hours
    before the slot, never past the slot, at most 31 days;
  - per record: the run id, time, room name and id, requester, agent
    (`owner/provider`), whether it was borrowed or dispatched by another
    agent, the status and, for a failure, its member-facing sentence
    (`AgentJobFailureReason.Text`), the request cut to 1500 bytes, its
    files by name and the run's last reply cut to 2000 bytes, read from
    the room (below, "The record's turn"; slice 2 first shipped the run
    record's 280-character preview of the first reply, too little to
    judge a goal by);
  - people (the requester and the agent's owner) by name only when the
    schedule's owner is an organization admin (`readerSeesNames`, PRD
    §3), else as `member-<hmac>` of the ask's alias key: stable across
    reads of one grant, different across grants;
  - at most 12 records and 20 KiB a page (`next` continues without
    repeats; a page always holds its first record), scanning at most 5
    store pages a call, 4 calls a run (beside the run's 8 model steps);
    the page is fenced as data and stays within one tool answer
    (`maxScheduleToolText`, 24 KiB);
  - each call audited (the schedule, the source, the range, the count,
    the scope and whether names were shown; never a record), refusals
    as `deny`.
- **AI.** The grant dialog runs the built-in assist `explain-source-use`
  (json: `summary`, `uses`, `mismatch`; through the assist route, the
  admin's budget; no new model client) over the purpose and the room
  owner's scheduled AI tasks (its input built by the gateway,
  `explainSourceInput`, the one builder; slice 2 first read the package
  version's tasks), and flags a task beyond the purpose. The admin
  decides.
- **One owner each, collapsed while building.** A run's reply and usage
  summary in its diagnostics is read only by `store.RunDiag` (activity's
  run records and its team plan's cost sample used private copies); a
  run page's cursor is `store.AgentRunCursor.String` /
  `ParseAgentRunCursor` (activity's and read_records'); the terminal
  statuses a query names are `store.TerminalJobStatuses()`.
  `TestRoomSourceConsentSingleOwner` keeps grants, scopes and the
  gateway's run reads in their owners.
- **Release safety.** The migration `2026_10_room_sources` only adds two
  tables (both deleted with their room, `roomOwnedTables`); the previous
  release's gateway never reads them. A room package saved by slice 1
  with `"sources": ["runs"]` no longer validates (pre-launch: no
  compatibility); its author saves a new version with a purpose.

**The record's turn (added 2026-10-20).** A 280-character preview of a
run's first reply is too little to judge whether a goal was reached, and
"a file that did not arrive" is a cause a review must see. Each record
now carries the run's last reply and its request's files by name:

- **One read of messages.** The run record holds the request's room
  sequence (`MessageSeq`) and channel, not the reply's sequence (its
  diagnostics keep the first reply's message id and preview). So the turn
  is read from the room: `readTurn` (`schedule_records.go`) reads one
  history page of the request's channel from the request on (200 room
  sequences, realtime's largest page: `before` = the request's sequence
  + 200) through the gateway's one history read, `roomHistory` (the
  assist's `assistHistory`, renamed, which the room context and the turn
  of `find-a-way` read), and the one row decoder (`roomlog.DecodeRow`).
  The request's row names its files (at most 10 names of 120 bytes, "and
  N more"; never their content); the reply is the last message of the
  run's agent (`owner/provider`) whose `sourceSeq` is the request, cut to
  2000 bytes.
- **A consented read, not a membership.** The reading schedule's owner is
  no member of the rooms a grant covers, and realtime's history route
  answers members only. Realtime adds
  `GET /internal/v1/rooms/{roomId}/history` for the gateway alone
  (`svcauth.RequireService`), the same handler as the members' route
  (`writeHistory`) without the membership: it reads as an agent does, so
  the people chat answers 403, and the store scopes it to the signed
  tenant. The gateway calls it only from `read_records`, after
  `sourceCover.covers` decided the room (a `historyReader` with
  `consented: true` appears only in `schedule_records.go`, and `/history`
  only in `roomHistory`: `TestRoomSourceConsentSingleOwner`).
- **What the read does not reach is said.** A reply deleted, later than
  the page or unreadable (a realtime failure, logged) leaves the run
  record's preview with `replyNote` saying so; fewer replies found than
  the run posted says "the last of N replies read here; the run posted
  M"; a request no longer in the room counts its files ("2 files (names
  not read: …)").
- **Bounds.** A record is at most about 4 KiB of text, so a page is at
  most 12 records and 20 KiB (was 25 and 18 KiB), within one tool answer
  (24 KiB); a page always holds its first record, so `next` always moves
  on. One history page per record read: at most 12 a call, 48 a run.
- **Design review.** Actors: only a schedule that may read the grant;
  the gateway reads as itself, never as a member. Boundaries: only rows
  of a covered room's run channel, never the people chat, never another
  tenant; the audit still holds no record. Scale and cost: one bounded
  history page per record. Failure: a history failure degrades a record
  to its preview with a note, never fails the read. One owner:
  `roomHistory` and realtime's `writeHistory`; no second message read.
  Release: additive (an internal route the previous gateway never calls;
  the previous realtime answers the new gateway's call 404, which reads
  as "no longer holds" and leaves the preview). Load on agents: none
  (schedule-run tool only). AI: the reply is what a review room's prompt
  judges by.

**A grant pinned to a person (added 2026-10-20, review).** Slice 2's
grant was the room's, so three things outgrew the admin's consent:
`read_records` output lands in the reading room, so every member of it
reads other rooms' conversations (and a shared room is anyone's to
join); the room's owner could make anyone a room admin, whose schedules
then read; and the owner could rewrite a task after the admin consented
to its words. The consent's one owner (`room_sources.go`) now keeps:

- **A private room only.** `PUT /v1/tenant/room-sources/{roomId}/{source}`
  refuses (409) a shared reading room; the console's ask
  (`GET /v1/tenant/room-sources/{roomId}/{source}`, `wire.RoomSourceDetail`)
  shows whether it is shared, its owner, its members (names, roles) and
  its owner's AI tasks now.
- **Read by a person.** The decision names the grantee
  (`wire.RoomSourceDecision.grantee`), the room's owner as the admin saw
  them (409 when that is no longer the owner); the grant keeps it
  (`room_source_grants.grantee`) with what the admin consented to
  (`watch_json`: the room's members, the grantee's AI tasks' words).
  `read_records` reads only for a schedule whose owner is the grantee;
  the console, the room's note and the covered rooms' note name them.
- **The watch** (`watchGrant`): a granted ask is checked against its room
  (from the room store, `RoomMembers`, and realtime's schedules as the
  grantee) when a change the edge proxies may affect it succeeds (the
  room's PATCH, a transfer, a join, an added member, an invite's
  redemption, a schedule's create or edit: `apiPath.changesSourceRoom`,
  the proxy's `afterSourceRoomChange`), before every `read_records` call
  (`heldGrant`, so a change made where the edge did not see it still
  holds before a read) and when the console opens the ask. Then:
  - the room shared: the grant is withdrawn (audited `withdraw`, the
    admins told through `notifyOrgAdmins`);
  - its owner not the grantee: the grant is paused, back to waiting
    (audited `pause`, told); the admin grants it again to the new owner;
  - a member the admins were not told of: told, naming the room and the
    person. **Notify only, the grant holds**: a private room's members
    are invited by its owner, the admin can withdraw at once, and pausing
    on every invitation would stop a review room each time a colleague
    joins it;
  - a grantee's AI task new or changed since the consent: kept as the
    grant's drift (before, after) at once, then, past the answer
    (`tellDriftLater` through `goDetached`, the gateway's one way to work
    after a response, bounded by the assist's call limit, so a save never
    waits on the model), explained by `explain-source-use` (the
    built-in, never a same-named one a member saved; on the grantee's
    model, budget and rate: its input carries each changed task's
    `before`; the explanation is kept on the drift it was made for) and
    told with the words before and after and the explanation, the
    mismatch first, or that the explanation is unavailable when it could
    not be made. It notifies, never blocks; the
    console marks the ask "changed since granted" and shows the diff
    until an admin acknowledges
    (`POST /v1/tenant/room-sources/{roomId}/{source}/ack`, audited) or
    grants again (a grant consents to the tasks as they are). A task
    changed back, or deleted, leaves the drift; one change is told once.
  The watch changes a grant only while it is still the same grantee's
  (`SetRoomSourceWatch`), and its notices are deduplicated per event.
- **Release safety.** The migration `2026_10_room_source_watch` adds two
  columns (`grantee` with a default, `watch_json` nullable); a grant
  made before it has no grantee, so it reads nothing until the admin
  grants it again (pre-launch: no backfill).
- **Design review.** Actors: the admin consents to a room, a person and
  their tasks; a room admin reads nothing; the members who read the
  output are shown, and new ones told. Time and lifecycle: shared
  withdraws, transfer pauses, a changed task is told once and shown
  until acknowledged. Scale and cost: the watch is one grant read per
  change the edge proxies on a room (a member list and a schedule list
  more only when granted), one model call per changed task on the
  grantee's budget. Failure: a watch failure is logged and the next
  read watches again before reading; a missing explanation is said in
  the notice. One owner: the consent (`room_sources.go`, archtest: only
  it changes a grant, `read_records` reads through `heldGrant`), the
  admins' notices (`notifyOrgAdmins`), the assist's one input builder
  (`explainSourceInput`) and one JSON run (`completeJSONTask`). AI: the
  explanation proposes, the admin decides; nothing is blocked by it.

**Not in this slice.**

- The find-a-way card's cause and the member's choice
  (docs/prd/2026-10-20-never-a-dead-end.md §5.2) are not persisted: the
  card's result lives in the member's session (a memo in the web), and a
  "report" is a notification not linked to its run. `read_records`
  carries the run's outcome, failure sentence, request, files and last
  reply; a review room judges the cause from those itself.

**Design review (slice 2).**

| Lens | Answer |
|---|---|
| Actors | The installer asks (recorded at install); the org admin grants (to a private room, read by its owner), withdraws, acknowledges changed tasks and sees names; the room's owner opts it in; the reading room's owner or admins ask the admin; only the grantee's schedules read (a room admin's or a member's own reads nothing); the reading room's members read the output and are shown to the admin (new ones told); members (visitors too) of covered rooms see who reviews them and whose tasks read; a non-member of a room reads nothing of its sources. |
| Time and lifecycle | A withdrawal holds at the next read (the grant is watched at each call); the room made shared withdraws it, a transfer pauses it, a new member and a changed task are told once (the change shown until acknowledged); a deleted room loses its asks and opt-ins; an alias is stable for an ask's life, a re-install is a new ask. |
| Scale and cost | Bounded per call and per run (records, bytes, store pages, reads); the run's model steps stay 8; the console lists at most 500 asks. |
| Failure | A store failure answers the model "could not be read" and is not audited as a read; the ask route reports whether an admin was notified. |
| Boundaries and privacy | Only finished runs of covered rooms, never the reading room's own, never the people chat (no agent runs there); names only for an org admin's schedule; the audit never holds a record. |
| Abuse | Records are fenced as data; the ask to admins is deduplicated per day and spends the cost budget; the explanation flags tasks beyond the purpose, at the grant and for each task changed after it (on the grantee's AI budget, the built-in assist only); an owner cannot widen the audience (shared withdraws, members are told) or the readers (room admins read nothing, a transfer pauses). |
| One owner | The consent (`room_sources.go`, archtest), the admins' notification (`notifyOrgAdmins`), the run diagnostics (`store.RunDiag`), the cursor. |
| Defaults | A new ask waits; the dialog proposes `opted-in`, the narrowest scope; an unknown visibility is private. |
| Load on agents | None: `read_records` is a schedule-run tool. |
| Load on users | One note in the header; one dialog for the admin; one switch for an owner. |
| Release | Additive tables, then additive columns (`grantee`, `watch_json`); no data kept from slice 1, a grant without a grantee pauses (pre-launch). |
| Observability | Every grant, withdrawal, pause, acknowledgment, opt-in and read is in the organization's audit log ("Room sources"). |
| AI capability | `explain-source-use` summarizes and flags, at the grant and for a task changed after it; the admin decides, nothing is blocked. |
| Component or platform | The platform keeps only the consent, the read and the audit; the review itself stays content (slice 3). |

**Pre-mortem (slice 2).** 1. A member builds a reading room and reads
everyone's conversations: the admin must grant it, the members of covered
rooms see it, every read is audited, names stay hidden unless the schedule
is an admin's. 2. A package states a harmless purpose and its tasks do
more: the AI explanation flags it, the admin sees every task. 3. The
review leaks a private room: `none-private` and `opted-in` exist, and the
dialog proposes the narrowest.
