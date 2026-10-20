# A room's whole way of working, captured by AI (2026-10-20)

Status: proposed. Owner: the room package capture
(`services/gateway/room_components.go`, `component-draft`), the room
package kind (`internal/contentpkg/room.go`), the room-saving assist.

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
   files in the package; tools as `requires.mcp`.
2. Agent schedules picked at install; "Try as a newcomer".
3. QA acceptance: one member sets a room up by talking and publishes it;
   another installs it and sends the same request.
