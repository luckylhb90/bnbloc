# Set up a room by talking, then publish it (2026-10-20)

Status: slice 1 built (§6: welcome and quick prompts on the room,
`room_setup` replacing `schedules`, `save_component` for this room,
`publish_component` as a card the member clicks). Owner: the room tools
(`services/bridge/roomtools`), the agent door
(`services/gateway/agent_door.go`), the room package capture
(`services/gateway/room_components.go`).

A member says in a room what the room should do ("every morning, review
yesterday's conversations that did not work out"). The agent sets the
room up: its description, welcome and quick prompts, the expert and packs
it uses, its channels, its scheduled tasks, the sources it asks for. When
the member likes it, they say "publish this room" and the agent saves it
as a room component and shares it. Every change is the member's: the
agent asks first, acts as them, and only where they may act.

## 1. Why

- The owner, 2026-10-20: "用户自己在房间里对话完成相关的配置，然后可以发布出去".
  The outcome-review sample was written as a JSON file and uploaded through
  the API: a developer's path, not a member's.
- What exists: an agent creates and deletes a room's AI tasks and notices
  through the `schedules` room tool (the agent door: as the requester, on
  the routes people use, audited); it saves and shares experts, packs and
  works with `save_component` / `publish_component`. Everything else about a
  room's setup is set in dialogs only, and a room's welcome and quick
  prompts are not stored on the room at all (only in a package, shown to
  its installer once from browser storage).

## 2. What it is (the end state)

### 2.1 A room holds its whole setup

- A room stores its **welcome** and **quick prompts** (the room service, the
  same limits as a room package's). Every member sees them in the room; the
  room's settings edit them; "Save as component" captures them and an
  install sets them. The installer-only browser copy goes.

### 2.2 The agent sets the room up, as the member

- The `schedules` room tool becomes **`room_setup`** (the same count of
  tools): `get` (the room's setup as its owner sees it: description,
  welcome, quick prompts, expert, packs, channels, schedules, sources),
  `set` (description, welcome, quick prompts), `use` / `unuse` (an expert,
  skill or prompt pack from the member's Library), `add_channel`,
  `add_schedule` / `remove_schedule` (today's `schedules` create/delete),
  `ask_source` (a source with its purpose; the admin still decides).
- Through the agent door only: the agent acts as the member who asked, on
  the routes people already use, so the member's own role decides (a
  member who may not change the room cannot have an agent change it).
  Each change is audited and shown in the room as a line ("maya's agent
  turned on the reviewer expert"), which a person can undo in settings.
- The tool's brief says: propose the change in a sentence, change only after
  the member agrees. No other rule.

### 2.3 Publishing the room from the conversation

- `save_component` takes `kind: room`: it captures this room through the
  same owner as "Save as component" (`component-draft`), with the name,
  description, category and tags the agent writes (it is the AI), into the
  member's Library as a draft version.
- `publish_component` shares it (organization or, for a super admin, every
  organization) as it does other kinds. Sharing beyond its author is the
  member's own click (a review, 2026-10-20: text an agent read must never
  publish their work): for every kind, the tool puts a card in the room
  that only the member sees, and their click on Publish shares it (§6).

### 2.4 AI

The feature is AI throughout: the agent turns the member's words into the
room's setup and its listing. The dialogs stay as the manual path.

## 3. Design review

| Lens | Answer |
|---|---|
| Component or platform | Platform: a room's welcome and quick prompts (a room property), the agent door's routes. No use case is named; a review room is what a member builds with it. |
| One owner | Setup changes go through the routes the dialogs use; capture through `component-draft`; one tool replaces `schedules`. |
| Boundaries and privacy | The agent has the requester's rights in this room only, never more; sources still need the admin; audited; shown in the room. |
| Load on agents | One tool replaces one (`schedules` → `room_setup`); its brief stays short, details on demand (`room_help`). |
| Load on users | Say what the room is for; agree to each change; say "publish". |
| Release and compatibility | Pre-launch: `schedules` is replaced, not kept; the welcome/quick prompts browser copy is deleted. |
| AI capability | The whole feature. |

## 4. Pre-mortem

1. **The agent changes the room without asking.** The brief says ask
   first; each change shows in the room with who asked; settings undo it;
   the member's own role bounds it.
2. **A borrowed agent configures someone's room.** It acts as the member
   who asked it, so only what that member may change.
3. **Publishing leaks something.** Capture is the same as the dialog's (no
   members, no person-posting schedules); publishing is the member's
   explicit request, scope as for other kinds.

## 5. Build order

1. Welcome and quick prompts on the room; `room_setup` replacing
   `schedules`; `save_component` / `publish_component` for rooms.
   **Built** (§6).
2. The outcome-review sample re-made by talking in a room, published from
   there (its repository keeps the exported package).

## 6. Slice 1 as built

The references are docs/architecture/room-tools.md (`room_setup`,
`save_component`, `publish_component`) and
docs/architecture/content-components.md ("Room packages"). What the slice
decided, against the code:

- **A room's welcome and quick prompts** are two nullable columns of
  `rooms` (`welcome`, `quick_prompts_json`; migration
  `2026_10_room_welcome` only adds), read by `GetRoom` (not the listings)
  and answered in `wire.RoomDetail` to every member. A quick prompt of a
  room is a title and its text (`wire.RoomQuickPrompt`). The room service
  takes them on `POST /v1/rooms` (an install sets them) and on
  `PATCH /v1/rooms/{id}/welcome` (`wire.RoomWelcomeRequest`, an absent
  field kept), the owner's or an admin's, like the room's experts and
  packs. Their bounds are a room package's: `contentpkg.CheckRoomStart`
  (12 quick prompts, a title of 80 and a text of 4000 characters, a
  welcome of 4000) runs the same `checkStart` as the expert and room
  package validators, and `contentpkg.CheckRoomDescription` (500) now
  bounds a room's own description too (it was the column's alone).
- **Every member sees them** while the room is empty: `RoomStartCard`
  reads the room detail (`roomStartOf`); a chip puts its text into the
  message box. The room's settings edit them (`RoomWelcomeSection`, the
  owner and admins; others read them there).
- **Capture and install carry them.** `component-draft` reads the room's
  welcome, quick prompts and its own asks for sources (granted or not);
  the save dialog starts from them. An install creates the room with the
  package's welcome and quick prompts; a quick prompt naming a pack's
  prompt (`pack/prompt-id`) takes that prompt's text, resolved for the
  installer (`contentuse.ResolvePromptRef`), and one whose pack they may
  not use is left out with a line. `rememberRoomStart` and the
  `bnbloc.roomStart` browser copy are deleted; the install reply no longer
  carries a welcome. What the install left out (`skipped`) is no room
  state, so the install dialog says it once before the room opens; the
  paused schedules and the waiting sources are the room's own state (its
  Schedules, the note under its name).
- **`room_setup` replaces `schedules`** (one tool for one): `get`, `set`
  (description, welcome, quick prompts), `use`/`unuse` (an expert, skill
  or prompt pack from the requester's Library by name, `roomlog.ReadLibrary`;
  an expert becomes the room's expert), `add_channel`, `add_schedule` /
  `remove_schedule` (the old create/delete, an AI task or a notice only),
  `ask_source` (a source and its purpose). Its brief: "This room's setup
  (description, welcome, quick prompts, expert, packs, channels,
  schedules, sources), changed as the person who asked you. Propose each
  change in a sentence; make it only after they agree." The rest is
  `room_help`'s.
- **Through the agent door, as the requester.** The door's routes grow by
  exactly the dialogs' ones: `GET`/`PATCH /v1/rooms/{id}` (a patch of the
  description only; any other field is refused at the door),
  `PATCH …/welcome`, `GET`/`POST …/channels`, beside the schedules and
  groups. The gateway's own routes resolve the same grant
  (`contentGrant`): `/v1/content-uses`, `GET …/sources` (now granted too)
  and the new `POST /v1/rooms/{id}/sources/{source}` (the owner or admins:
  the room's own ask, recorded by `askRoomSource`, the one writer of an
  ask that a package's install uses too; an ask made in the room names no
  package, so the console says "Asked in the room by …"). The requester's
  role decides: a plain member's agent is refused the welcome, the
  description, the room's packs and a source ask with the route's own
  words, and may add a channel as the member may.
- **Audited and shown.** Each door change spends the requester's cost
  budget and is audited once the backend answered, allowed or refused
  (`wire.AuditKindRoomSetup`, which replaces `AuditKindSchedule`; the
  description, welcome, a channel, a schedule, never the text); a granted
  content use (`use`/`unuse`) is audited the same way; a source ask as
  `source`. The tool shows each change in the room as a notice line
  (`wire.NoticeRoomSetup`, "maya's agent set the welcome") nested under
  the request; a person undoes it in the room's settings. No schedule
  creation by an agent posted anything before; the line is new and every
  change has one.
- **`save_component` kind `room`** captures this room through
  `component-draft` (the dialog's capture, read with the job's grant: the
  room's owner or admins) and saves the content form with the listing the
  agent writes (name, displayName, description, category, tags; the
  draft's where it gives none) into the requester's Library, a new
  component or, with `id`, a new version of their own. Sources are the
  room's own asks (`ask_source` changes them): the tool takes no sources
  of its own, so the room is the one place they are set.
- **Publishing is the member's click** (the review's addition, for every
  kind): `publish_component` with `org` reads the component (another
  author's is refused) and posts a notice row of kind `publish-proposal`
  whose `payload.proposal` (`wire.PublishProposal`) names the component,
  its version and the organization; the web shows it, as a card with
  Publish, only to the requester (`feedItemFromEvent` drops it for anyone
  else), and the click calls the publish route as them; the result stays
  on the card; the card names its version and the click publishes that
  one. One rule, one place: a job's grant never widens who sees content
  (`errJobWidens`, checked in `publishToOrg`, the one way to the
  organization), so the gateway refuses a job's `org` publish (403) and a
  new version saved through a job's grant (`save_component` with `id`, any
  kind) of a component the organization uses stays a version in the
  requester's Library: the save's reply says `orgProposed` and the tool
  posts the same card for that version, whose click goes through the
  publish route under the unchanged approval rules. A member's own save
  still resubmits as before. `personal` (withdrawing) still goes through
  with the grant.
- **Load on agents.** One tool for one; the session prefix grew by about
  110 tokens (room_setup's wider schema and `save_component`'s room kind,
  with briefs cut to pay part of it): the budget in
  `services/bridge/prompt_test.go` is 3060, said there. The guidance's
  one line about timed work names `room_setup`'s `add_schedule`.
- **Release.** The migration only adds nullable columns the previous
  release never reads. A room made before has no welcome. The old
  browser copy of an install reply is left in members' storage, unread.

**Not in this slice.**

- An agent removes no source ask and no channel; the room's settings and
  the Organization console do.
- For an ask made in the room, the console shows no scheduled tasks to
  judge the purpose against (a package's ask shows its version's): the
  room's own tasks are a later step, as the slice 2 note on the current
  tasks says.
- Other members see a change to the welcome when they reload the room:
  no room event carries a room's details (its name, description or
  welcome) today, and clients reread them only on load, so none is
  emitted for this either; adding one is its own change.

**Design review (slice 1).**

| Lens | Answer |
|---|---|
| Actors | The requester's role decides every change (owner: all; admin: welcome, packs, sources; member: channels, their schedules); the agent's owner gains nothing; a borrowed agent acts as the person who asked. |
| Time and lifecycle | A change is the room's at once; a schedule is the requester's and leaves with them as before; a card stays in the room's history and publishes the latest version when clicked. |
| Scale and cost | Each change spends the requester's cost budget; `get` reads six routes; bounds are the package's. |
| Failure | A refusal comes back in the route's words and shows no line; a line that cannot be posted leaves the change made and audited. |
| Boundaries and privacy | Only the requester's rights in this room; capture holds no people or messages; publishing is the member's click. |
| Abuse | Text people wrote comes back fenced; the door admits the description only on a room patch, an AI task or notice only on a schedule; a publish or a saved version through a job never reaches the organization. |
| One owner | `checkStart` for the bounds; `roomDraft` for capture; `askRoomSource` for asks; `auditAgentSetup` for the audit; `publishToOrg` for publishing, where `errJobWidens` keeps every job's publish and saved version from the organization. |
| Defaults | A room has no welcome until someone sets one; an expert is pinned at the version the requester sees. |
| Load on users and agents | One tool replaces one; one card instead of an unseen publish. |
| Release | Additive columns; pre-launch, `schedules` and the browser copy go. |
| Observability | `room-setup` audit rows, allowed and refused; the line in the room. |
| AI capability | The agent turns words into the setup and the listing; people decide each change and the publication. |
| Component or platform | Platform: a room property, the door's routes, a tool; no use case is named. |
