# Set up a room by talking, then publish it (2026-10-20)

Status: proposed. Owner: the room tools (`services/bridge/roomtools`), the
agent door (`services/gateway/agent_door.go`), the room package capture
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
  organization) as it does other kinds.

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
2. The outcome-review sample re-made by talking in a room, published from
   there (its repository keeps the exported package).
