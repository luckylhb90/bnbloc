# Rooms as components, and a room that reviews outcomes (2026-10-20)

Status: slice 1 built (the room kind, save and install, templates folded
in; §5, §6); slices 2 and 3 proposed. Owner: the content component
contract (a new kind), the schedule runs' sources, the room's own
content.

A room's whole setup (its expert, skills, prompts, tools, apps, channels and
scheduled AI tasks) can be saved as a component and installed as a new room.
The first room built this way reviews the organization's conversations every
day, judges whether each reached its goal, and sends what failed and what
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

### 2.2 The outcome review room (built in a room, shipped as content)

- **Every day** (a scheduled AI task), it reads the organization's
  conversations of the day within its consent, and for each judges:
  - what the member wanted (the goal, in their words),
  - reached, partly or not, with the evidence (the turn that shows it),
  - when not: why (no access, a file that did not arrive, a capability
    missing, the agent's mistake, the member gave up), and **what would
    have helped**: a component to install, a source to connect, an agent
    or person to bring in, a bug to fix.
- Judgments go into the room's own app data (a table app), so the room can
  filter, comment and track them; nothing is stored in the platform.
- **The digest**: a message in the room and a notification to the people
  the room names: the misses first, grouped by cause, with the fixes
  proposed. A fix becomes a task in the room in one click; a person
  decides.
- It is a room package shipped with the platform's built-in content, and
  anyone may change its prompt, causes or digest in their copy.

### 2.3 Reading other rooms: a consented source

- A room package may ask for a **source**: a kind of organization record
  its scheduled tasks may read. The first is `runs`: agent conversations
  of the organization (request and reply text, the agent, the outcome
  status, the room and time).
- Consent is the organization admin's, per installed room: what it reads
  (all rooms, rooms that opt in, or none of private rooms), for which
  purpose (the package's stated purpose is shown). It can be withdrawn at
  any time.
- Transparency: members of a reviewed room see, in its info, that the
  organization reviews conversation outcomes and which room does.
- The platform adds one schedule-run tool, `read_records(source, since,
  until)`, answering only what the consent allows, counted against the
  run's budget and audited. It knows sources, never goals or reviews.

### 2.4 AI

The review is AI throughout: the goal, the judgment, the cause, the fix.
AI proposes and people decide: a fix is a proposal, a task is created by a
person, a schedule runs only once the owner turns it on.

## 3. Design review

| Lens | Answer |
|---|---|
| Component or platform | The review is content (a room package). The platform adds a kind, an install flow and a consented source, none of which names goals or reviews. |
| One owner | Room templates fold into the room kind; one consent owner for sources; judgments live in the room's app data. |
| Boundaries and privacy | Reading other rooms needs the admin's consent per installed room, shown to the members it covers. The review is about whether the product served people, not about ranking them: the digest groups by cause and room; the requester's name is shown only to the organization's admins. |
| Load on agents | No new agent tool; `read_records` is a schedule-run tool. |
| Load on users | Install one room; turn its schedule on. |
| Cost | One model call per conversation judged, in batches, within the organization's assist budget; the room can sample. |
| Release safety | Additive kind and tool; templates move to the kind with no data kept (pre-launch). |

## 4. Pre-mortem

1. **People feel watched.** Consent is the admin's and visible to members;
   the purpose is the product's, and the digest leads with causes, not
   names.
2. **Wrong judgments.** Each carries its evidence; a person marks it wrong
   in the table, and the room learns from its own marks (they are in its
   context next time).
3. **Cost runs away.** Batches, a daily cap the room sets, sampling, and
   the organization's budget.
4. **A room package asks for more than it needs.** A source is shown with
   its purpose at install; the admin grants or not.

## 5. Build order (slices of §2)

1. The room kind: save a room as a component, install it as a room;
   templates folded in. **Built** (§6; commits 435ed025, 74124c84).
2. The `runs` source with consent, transparency and `read_records`.
3. The outcome review room as built-in content; the digest and fixes as
   tasks.

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
- **Design review notes.** Actors: the saver must be the room's owner or
  an admin; the installer owns the new room and its paused schedules; the
  package's author is never added. Boundaries: no people, messages, app
  data or grants travel in a package. Failure: install is all or nothing
  for the room, reported per part for what the installer may not use.
  Cost: one assist call per "Fill with AI"; install does a bounded number
  of backend calls (20 channels, 20 schedules, 10 apps, 32 requirements).

