# A room's whole way of working, captured by AI (2026-10-20)

Status: slice 1 built (§6: the room-saving assist's "depends on" and
instructions over the room's conversation; `INSTRUCTIONS.md`, apps made in
the room and ticked files in the package; the tools its runs had as
`requires.mcp`). Slice 2: agent schedules built (§7: a request to an
agent travels without its agent and waits for the installer's pick; an
agent sets one up through `room_setup`); "Try as a newcomer" not yet.
Owner: the room package capture
(`services/gateway/room_components.go` and `room_capture.go`,
`component-draft`), the room package kind (`internal/contentpkg/room.go`
and `room_carry.go`), the room-saving assist (`describe-room`).

Saving a room as a component should carry how the room is actually used,
not only its settings: what the conversation taught the agent, the apps
and files made there, the tools it relied on, the agents it drives on a
clock. AI reads the room and says what its way of working depends on;
the member decides what goes into the package.

## 1. Why

- The owner, 2026-10-20: is it certain that a member's complete way of
  using a room can be published for others? Today it is not. Capture
  (`component-draft`) takes the settings: description, channels, recurring
  AI tasks and notices, the room expert, skill and prompt packs, apps from
  works, source asks (and welcome and quick prompts:
  `docs/prd/2026-10-20-room-setup-by-conversation.md`). It leaves out:
  1. what the conversation established (the requests that worked, the
     member's corrections, agreed formats and steps);
  2. apps made in the room ("publish it as a work to include it");
  3. the room's files (reference documents, templates, sample data);
  4. the tools the room's agents used (an installer does not learn that
     the room needs the browser or media tools);
  5. schedules that drive an agent.
- The owner: AI can solve much of this ("有AI驱动，可能能解决很多难题").

## 2. What it is (the end state)

### 2.1 AI reads the room at save

- "Save as component" (the dialog, or `save_component` with `kind: room`
  from the conversation) first runs the room-saving assist over the room:
  its conversation (bounded, newest first, through the one history
  owner), its files (names, types, sizes), its apps, its schedules and the
  tools its runs used.
- It answers, in one reply:
  - **depends on**: what the room's way of working relies on, each with
    whether the package can carry it, and why not when it cannot;
  - **instructions**: what the conversation established, written as the
    room's instructions (who the agent is here, the steps and formats that
    worked, the member's corrections), for the member to edit;
  - the listing (name, description, category, tags, welcome, quick
    prompts) and the sensitive-content findings it already gives.
- The dialog shows "depends on" as a checklist (included / left out with
  the reason) and the instructions as an editable draft. Nothing is saved
  until the member saves.

### 2.2 What a room package can carry (capabilities, not use cases)

- **Instructions**: the package's `INSTRUCTIONS.md`; install makes them the
  new room's instructions (the room expert's role: decide by the code
  whether that is a room-owned expert or a room property, one owner).
- **Apps made in the room**: their current version as a site snapshot in
  the package (the works' site rules and limits); install copies them as
  the use-app path does.
- **Files**: the files the member ticks, within the package's size limit,
  restored into the new room as its files.
- **Tools**: the MCP tools the room's runs used become `requires.mcp`;
  install says which the installer has not added yet, with Add (the
  never-a-dead-end install action).
- **Agent schedules**: a schedule that asks an agent keeps its text and
  recurrence; at install it waits disabled until the installer picks one
  of their agents for it.

### 2.3 Is it complete? The check

- After saving, "Try as a newcomer": the platform installs the package as
  a fresh room of the saver's (marked trial, deleted on close), and the
  saver sends one of the room's real requests there. The assist compares
  the answer with the original room's and says what is missing. The saver
  fixes the package or accepts.

## 3. Design review

| Lens | Answer |
|---|---|
| Component or platform | Platform: what a room package can carry and the capture. The use (a review room, a support room) stays the member's content. |
| One owner | Capture through `component-draft`; one room-saving assist; history through the one history owner; apps through the use-app copy. |
| Boundaries and privacy | The conversation is read as the saver, who is a member; only what they tick leaves the room; the sensitive-content findings run over the instructions too; files carry what the saver can read. |
| Scale and cost | One assist call per save (bounded history); the trial is one room and the requests the saver sends. |
| Load on users | One reviewed checklist instead of rebuilding the room by hand. |
| Load on agents | None new: instructions are the room's, as an expert's are today. |
| AI capability | The whole capture: dependencies, the instructions, the listing, the check. |
| Release and compatibility | Additive fields on the room kind; pre-launch. |

## 4. Pre-mortem

1. **The instructions leak private details from the conversation.** They
   are a draft the member edits, and the sensitive-content findings cover
   them.
2. **Files make packages large.** Ticked files only, within the package
   limit; the dialog shows the size.
3. **A captured app needs data the room had.** A snapshot carries the app,
   not its data; "depends on" says so.
4. **The trial misleads.** It is the saver's own check, one real request,
   with the assist's comparison as a proposal.

## 5. Build order

1. The assist's "depends on" and instructions; `INSTRUCTIONS.md`, apps and
   files in the package; tools as `requires.mcp`. **Built** (§6).
2. Agent schedules picked at install (**built**, §7); "Try as a newcomer"
   (not yet).
3. QA acceptance: one member sets a room up by talking and publishes it;
   another installs it and sends the same request.

## 6. Slice 1 as built

The reference is docs/architecture/content-components.md ("Room
packages") and docs/architecture/ai-assist.md (`describe-room`, the
`conversation` context). What the slice decided, against the code:

- **The assist reads the room's conversation through a new context,
  `conversation`** (`wire.AssistContextConversation`; a capability any
  assist may declare, not a room-saving use case). The gateway reads it
  as the member (`assistConversation`, `services/gateway/assist_conversation.go`):
  the room's default channel and up to 11 more of the room service's
  listing (which never lists the people chat), each channel's latest 80
  rows through the one history read (`roomHistory`) and the one row
  decoder (`roomlog.DecodeRow`), merged by room sequence, newest first.
  `assist.NewConversation` (`internal/assist/conversation.go`) keeps the
  newest within its budget: at most 80 lines, each cut to 1,500
  characters, 20,000 characters together (about 6,000 tokens beside the
  task's instructions and its 2,000-token reply), shown oldest first in a
  fenced `<conversation>` block that says how many older messages it left
  out. Its golden is `internal/assist/testdata/golden/room.txt`.
- **`describe-room` 1.2.0** declares `conversation` (no longer `room`:
  the setup it reads is `<text>`) and answers, beside the listing and the
  findings, `instructions` (text) and `dependsOn` (items of `what`,
  `kind` — `instructions`, `app`, `file`, `tool`, `schedule`, `agent`,
  `data` or `other` — `included` `yes`/`no` and `why`). `included` is the
  AI's word that the package should carry it; the dialog matches each one
  to the tick box it names and shows the box's real state
  (`web/src/lib/roomCapture.ts`). The findings cover the instructions too
  (`package.instructions`). Its static text stays in the package
  (`INSTRUCTIONS.md`, about 860 tokens: its own budget of 900 in
  `assists_test.go`, since it runs once per save and does three jobs);
  everything dynamic comes per call. `wire.MaxAssistText` is 16,000
  characters (it was 8,000) so a package's every text and its
  instructions fit the check.
- **The run records did not keep tools.** A run's diagnostics held its
  reply and usage only. The bridge now reports the MCP tool components
  attached to the turn's session (`wire.RunDiagTools`,
  `wire.AgentRunTools{mcp}`, after they are fitted to the target, on the
  running, reflowing and final reports; `jobContent.withTools`), and the
  run record shows them (`wire.AgentRun.Tools`). This is what the session
  had, not which tool calls the agent made: no record of tool calls
  exists, and the core's live activity is transient. The draft reads the
  room's latest 100 runs through activity's room route as the editor (the
  gateway's own run reads stay `read_records`'); a tool the editor may use
  becomes a `wire.RoomDraftTool` with the permission a package declares
  for it (`contentpkg.MCPPermission`, now the one rule `checkMCPReference`
  uses too); one they may not use, or run records that cannot be read,
  are said in `left`. Runs from before this bridge report no tools.
- **The package carries** (`internal/contentpkg/room_carry.go`):
  `INSTRUCTIONS.md` (an expert's bound, 64 KiB); `appSnapshots`
  (`{title, dir}`, each a site under `apps/<name>` with `index.html` or
  `dist/index.html`, no agent configuration); `files` (`{name, path,
  sizeBytes}`, each under `files/<name>`, at most `wire.MaxRoomFiles`
  = 20). The apps' sites and the files together stay within
  `wire.MaxRoomCarryBytes` (64 MiB, one site snapshot's cap; a test holds
  them equal), and works plus apps made here within `contentpkg.MaxRoomApps`
  (10). `RoomLimits` grows to that bound; the room kind is now a
  `Snapshot` kind (an app's scripts run only in the site frame, and a
  listing never opens its bundle). A new version keeps the edited
  version's apps and files the form names by folder and path
  (`fromForm` now receives the base version).
- **How instructions install (the decision).** A room's instructions
  live in one place, its room expert: a room property would have needed a
  new column, a route and a second way for instructions to reach agents
  beside the expert's. So a package carries `INSTRUCTIONS.md` or names
  `requires.expert`, never both (the validator refuses both; the dialog
  asks the member to untick the expert or clear the instructions; the
  agent's save leaves the expert out and says so). Install makes a
  private expert of the installer's from them (`installInstructions`:
  named after the new room, "<room> instructions"), turns it on in the
  room and makes it the room's expert through `setRoomExpert`, the one
  writer; the installer edits it in their Library like any expert. A
  failed install deletes it with the room.
- **Save.** A content save takes an `include` (`wire.RoomInclude`: the
  room, the apps made in it and the room files, by artifact id). The
  gateway reads each as the saver reads it through `currentDeliverable`
  (the one site/file snapshot reader the works use) into the form's
  `Captured` (`roomIncluded`, `services/gateway/room_capture.go`): only
  the room's owner or admins, a job's grant only for its own room, the
  counts and the 64 MiB bound checked before more is read. The bytes never
  pass through the client.
- **Install** (`installRoom`, one cleanup): after the packs, the
  instructions' expert; the works' apps (`copyApp`); the apps made in the
  source room, each its folder's site as `WorkSite` copies a work's
  (`contentpkg.RoomSite`), through `copySite` (a fresh, empty data space;
  no work, so no `app-copy` use); the files through `createRoomArtifact`
  (extracted from `copySite`: the gateway's one creation of a deliverable
  from bytes), typed by `store.ArtifactMIME` and
  `store.ArtifactKindForFile`. `requires.mcp` tools the installer may
  use but has not added are `toolsToAdd`; the install dialog offers Add
  for each (`RoomToolsToAdd`: the member catalog's install, then the
  tool's settings when it needs any). A failure once the room exists
  deletes the room (its channels, schedules, apps and files), uncounts the
  work copies and deletes the instructions' expert.
- **Capture.** `component-draft` offers `madeApps` (every site made in the
  room, an app or not, with its current version's size), `files` (the
  room's other deliverables, at most 50 offered, the rest said) and
  `tools`; "app made here: publish it as a work" is gone from `left`, and
  "the data of its apps" is said there. The dialog
  (`SaveRoomComponentDialog`, `RoomCaptureSections`) ticks the apps and
  tools and leaves the files for the saver to tick, shows each size and
  the total against the bound (refused past it before saving), the
  instructions as an editable draft and "depends on" as a checklist.
  "Fill with AI" fills the listing and the instructions and ticks what
  `dependsOn` says the package should carry. Nothing is saved until Save.
- **`save_component` kind `room`** takes `instructions` and `include:
  {apps, files}` (artifact ids the agent and the person agreed on), and
  requires the draft's tools; with instructions the room's expert is left
  out and the result says so. The session prefix grew by about 30 tokens
  (two parameter notes cut to pay part): the budget in
  `services/bridge/prompt_test.go` is 3090.

**Not in this slice.** Agent schedules picked at install and "Try as a
newcomer" (slice 2). A capture of the room's own instructions when the
room already has an expert merges nothing: the member chooses one. A room
file's version history, an app's data and its share links never travel.

**Design review (slice 1).**

| Lens | Answer |
|---|---|
| Actors | The saver is the room's owner or an admin (draft, include); the installer owns the new room, its expert and its copies; the source room's members and agents are never named. |
| Time and lifecycle | A version keeps its apps and files; the installer's expert outlives the room (their Library); runs before this bridge show no tools. |
| Scale and cost | One assist call per Fill or Save check, its input bounded (20,000-character conversation, 16,000-character text); the draft reads one artifact page, one detail per offered deliverable and one run page; a package is at most 64 MiB beyond its text. |
| Failure and recovery | A failed install deletes the room, its copies and the expert; unreadable run records leave the draft standing and say so. |
| Boundaries and privacy | Conversation read as the saver, never the people chat; only ticked apps and files leave the room, read as the saver; the findings check the instructions; no app data travels. |
| Abuse and untrusted input | Messages are fenced in their block; an include names only the saver's own room's deliverables; the validator refuses agent configuration in an app or as a file. |
| One owner | `roomHistory` and `DecodeRow` for the conversation; `currentDeliverable` for snapshots; `copySite`/`createRoomArtifact` for deliverables; `setRoomExpert` for the expert; `MCPPermission` for a tool's permission; `WorkSite` for a site's files. |
| Defaults and fallbacks | Apps and tools ticked, files not; without an assist model the dialog still saves; no instructions, no expert. |
| External contracts | Additive wire fields; the room kind's new fields are refused by every other kind. |
| Load on users and agents | One checklist and one draft to review; one more pair of `save_component` arguments (about 30 tokens). |
| Release and compatibility | Pre-launch; no migration (the run diagnostics are JSON); an older bridge reports no tools. |
| Observability | The install's audit row as before (it names the package and the room); the run records show the tools a session had. |
| AI capability | The whole capture: dependencies, instructions, listing, findings; the member decides every tick. |
| Component or platform | Platform capabilities only: a context, package parts and their install; no use case is named. |

## 7. Slice 2 as built: agent schedules

"Try as a newcomer" (§2.3) is not built yet. What the agent schedules
decided, against the code:

- **The state is the schedule's own data, no new field.** A request to
  an agent (recipient kind `agent`) that names no owner, provider or core
  waits for an agent: `schedule.WaitsForAgent` (`internal/schedule`) is
  the one rule of that state, and `NormalizeRecipient` accepts it (half a
  target is still refused). The web shows it with `waitsForAgent`
  (`web/src/lib/schedules.ts`): status "Waiting for an agent", recipient
  "an agent you pick", never a borrow. No migration: the recipient is
  JSON (`recipient_json`), and an empty owner was never stored before.
- **One place decides a schedule may be enabled or run.** realtime's
  `runnable` refuses a waiting schedule: `planNextRun` (create or patch
  with `enabled: true`, 400, and a patch that takes an enabled
  schedule's agent away), run now (409) and `postScheduled` (the one post
  of the scheduler's and run now's, also behind the scale-out route, where
  it pauses the schedule like an empty message; it cannot be reached
  while enabling is refused, and stops a post to nobody as its creator).
- **The package rule.** `contentpkg.RoomScheduleMisfit` (the one rule of
  which schedules a package holds) now admits kind `agent`; posting as a
  person (kind `room` or none) stays refused ("words they never wrote");
  a notice to people or a group and a one-time run as before.
  `contentpkg.PackageRecipient` is a fitting recipient as a package holds
  it (`{kind: "agent"}` and nothing more); the validator refuses a
  request that names any part of an agent. Capture (`draftSchedules`)
  keeps the text, recurrence, timezone and channel, strips the agent
  and says so in `left` ("the agent of N scheduled requests (whoever
  installs it picks one of their agents)").
- **Install** creates it paused through realtime's route like every
  package schedule (`installSchedules`, `PackageRecipient` again so a
  package can never install a named agent) and lists it in the reply's
  `waitingForAgent` (its text in a line). The install dialog says "Pick
  an agent for: <text>". In the room, `AgentPicksCard` (above the feed,
  only the member's own waiting schedules, `agentToPick`; one schedule
  list read per room open and per Schedules dialog close) lists the same
  with "Pick an agent", which opens the Schedules dialog on that
  schedule's editor (`editId`). Its picker is the one the dialog uses for
  any request to an agent (`agentRows`: the member's own targets and the
  room's agents), with "Choose an agent…" until one is chosen; saving
  sets the target (realtime's PATCH, the creator only); Resume turns it
  on. Pick and enable stay two steps, as the PRD says (the installer
  enables it), so a pick never starts runs by itself.
- **`room_setup` add_schedule takes `agent`** (a handle from
  `list_room_agents`, resolved by `resolveHandle` against the roster);
  `task` is then the request posted to that agent as the person each
  time. The door (`agentChangeAllowed` → `agentScheduleTarget`) admits a
  request only to one of the room's agents online now (realtime's roster
  through `roomRoster`, the one read: the agents the dialog offers the
  person) and never one without a target; another member's agent is a
  borrow, authorized at drive time as for the dialog's. The brief's text
  is unchanged; the new parameter is paid by three shorter parameter
  notes (`task`, `send`, `name`), so the session prefix stays at its
  3090-token budget. `get` names each request's agent, or "none yet".

**Not in this slice.** "Try as a newcomer". A schedule waiting for an
agent is the installer's only (its creator): nobody else picks for them.

**Design review (slice 2).**

| Lens | Answer |
|---|---|
| Actors | The saver's request loses its agent; the installer creates, picks for and turns on their own schedule; a member's agent schedules for the member, never as another; another member's agent runs a request only under its owner's borrow authorization. |
| Time and lifecycle | Installed paused and waiting; picking keeps it paused; Resume plans its next run. A waiting schedule never plans a run, so the scheduler never claims it. Its agent going offline later is the existing drive-time path. |
| Scale and cost | One schedule list read per room open (and per dialog close) for the card; a waiting schedule costs nothing until enabled; one roster read per agent-made request. |
| Failure and recovery | Enabling or running a waiting schedule is refused with what to do ("pick one of your agents for it, then turn it on"); a roster read that fails refuses the agent's request (502, logged), the person can still use the dialog. |
| Boundaries and privacy | A package carries no person or agent of the saver's; the installer's pick names only agents the dialog offers them. |
| Abuse and untrusted input | The door checks the target against the roster server-side (an agent cannot name an arbitrary owner); the text stays the schedule rule's (bounds, fenced when `get` reads it back). |
| One owner | `WaitsForAgent` (the state), `runnable` (enable and run), `RoomScheduleMisfit` and `PackageRecipient` (what a package holds), `roomRoster` (the room's agents), `agentRows` (the picker). |
| Defaults and fallbacks | Waiting until the installer picks; nothing guesses an agent for them. |
| External contracts | Additive: `RoomInstallReply.waitingForAgent`; an agent recipient may be empty; the door admits kind `agent`. |
| Load on users and agents | One "pick an agent" per request, from the install dialog's list or the room's card; the tool brief unchanged in length. |
| Release and compatibility | Pre-launch; no migration. An older realtime replica refuses an empty agent recipient, so an install there fails cleanly and deletes the room. |
| Observability | The agent's request is audited as `add-schedule` like the others; a refused one as denied. |
| AI capability | An agent sets a request to an agent up by talking (`room_setup`); the installer decides the agent. |
| Component or platform | Platform: what a package's schedule may carry and its install; no use case is named. |

**Pre-mortem (slice 2).** (1) An installer never notices the waiting
schedule: the install dialog lists it, the room shows the card until it
has an agent, the Schedules dialog marks it. (2) A pick starts runs
before the installer means to: picking keeps it paused. (3) An agent
schedules a request to someone's agent it may not drive: the door admits
only the room's agents, and the borrow is checked at drive time as for
the person's own. (4) A waiting schedule posts as its creator to nobody:
`runnable` refuses it on every path that enables or posts.
