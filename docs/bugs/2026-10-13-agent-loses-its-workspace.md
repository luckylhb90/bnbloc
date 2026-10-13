# An agent loses its workspace when its session changes (2026-10-13)

Status: fixed: recorded in ff1c3b4, fixed in 05ef0b6.

## Symptom

Seen on a QA deployment, in a room, with an agent on an ACP-adapter core
(provider `opencode`):

1. A member asked the agent, wearing the built-in `app-builder` expert, to
   build an interactive app. The agent built it in its workspace and the
   bridge published the site at the end of the turn.
2. Later the member sent a plain follow-up with **no expert** ("the page
   doesn't load"). The same agent answered that:
   - its workspace was empty apart from `.codegraph` and `.room`: the app's
     source was gone;
   - the `interactive-apps` skill was not loaded;
   - it had no `read_artifact` or any other room tool.

   So it could neither inspect nor fix its own app.

## Repro

1. Drive an agent on a core that creates a workspace per `POST
   /workspaces`, with an expert; have it write `index.html`.
2. Send a follow-up without the expert (or restart the bridge, or let the
   core drop the session so the bridge rebinds it).
3. The follow-up runs in a new, empty workspace.

## Causes, verified

### A. A new session got a new workspace: the cause of the lost source

`ensureSession` (`services/bridge/bridge.go`) bound a drive slot (room,
core, provider, execution kind) to a *session and its workspace* as one
unit. Every path that dropped the session dropped the workspace with it,
because the fresh-session branch always called `createWorkspace`:

- a turn whose binding sum differs (an expert on one turn, none on the
  next: the QA case) unbinds the slot and creates a new session in a new
  workspace;
- a stale session (the core restarted or lost it) is rebound the same way;
- a bridge restart starts with an empty binding table (bindings lived only
  in memory), so every agent's next turn ran in a new workspace;
- a deliverable's artifact id was derived from the workspace AND session
  ids, so even with a kept workspace, an edit in a later session would have
  published a new deliverable instead of a new version.

The agent's files stayed behind in the old workspace on the core. Evidence:
`TestContentChangeKeepsTheWorkspace` and
`TestRebindAndRestartKeepTheWorkspace` fail on the old code (two and three
workspaces created).

### B. Room tools only with content: not the cause in the bridge

The bridge attaches the room tools to every session created on a core that
advertises `agentProfile`, with or without an expert: `sessionGuidance`
builds a profile even for a turn wearing nothing (the session guidance, the
contributors' servers) and calls the contributors on it. The new session
after the content change got the tools (`TestContentChangeKeepsTheWorkspace`
asserts it for both sessions; removing the contribution for a session
without content makes it fail).

What the QA agent saw is consistent with its core not offering
`agentProfile` when the drive asked: no `AGENTS.md` in the fresh workspace
(the ACP adapter writes the guidance there for every profile session by
default), no room tools, and the `interactive-apps` skill only ever written
by the bridge's fallback into the first workspace's `.agents/skills/`. Two
ways a core does not offer it: an adapter package that predates
`agentProfile` (the ACP adapter gained it on 2026-10-12), which is a QA
deployment matter (check the core's `GET /core/v1/discover`), and a
discovery that failed when the drive snapshotted the core's capabilities,
which the bridge read as "offers nothing optional" and then ran a whole
session without profile, room tools or records. The second was a real
bridge gap and is fixed below; the QA workspace held `.room/`, so the
snapshot there did read the discovery, which points at the first.

### C. Something else clearing a workspace: not the cause

- The sandbox fence's `resetProtectedPaths` removes only protected paths
  (agent config, `.room/`, host-executed project config) from a sandbox
  workdir, never project files, and the ACP adapter does not advertise
  `sandboxExec`, so the QA drive was a host drive.
- `resetAgentConfig` no longer exists (it became `resetProtectedPaths`).
- The ACP adapter does not mint a workspace per session: `POST
  /workspaces` creates one, and sessions are created in the workspace the
  client names. The bridge called `POST /workspaces` for every session.

## Fix

One owner per rule:

- **Workspace continuity** (`services/bridge/workspaces.go`,
  `agentWorkspaces`): the one owner of which workspace a drive slot uses.
  Created for the slot's first session and bound before that session
  starts; every later session (another binding sum, a stale rebind, a core
  endpoint change, a bridge restart) is created in it. Replaced only when
  the core answers 404 on the session create (`core.NotFound`: it no
  longer holds the workspace); a bound workspace is never deleted on a
  failed create, and a 404 that the new workspace answers too (a missing
  target) keeps the old binding. Bindings persist in
  `BNBLOC_BRIDGE_STATE_DIR/workspaces.json` (owner-only, written
  atomically; an unreadable file is moved aside and reported); the desktop
  sets it to the member's `member_dir`/bridge. Unset, they live in memory.
  A gone room drops its bindings. No Core Protocol change was needed: the
  protocol already creates sessions in a named workspace; the contract that
  a workspace outlives its sessions is documented in
  `docs/architecture/core-providers.md`.
- **Deliverable identity** (`artifactIDForFile`): room, owner, core,
  provider and path, so an edit in any of the agent's sessions publishes a
  new version of the same deliverable.
- **Room tools on every session**: unchanged (already true), now pinned by
  a test. A core whose capabilities cannot be read
  (`core.CapabilitiesKnown`, the protocol client's discovery) fails the
  drive `core_unavailable` before anything reaches it, and the job is
  retried, instead of a degraded session that never gets the tools.
- **Deliverable source in the workspace** (`roomlog.CopyDeliverables`): the
  pull pass copies each listed deliverable's latest version read-only
  under `.room/deliverables/<id>/` (a site as its file tree through the
  pinned bundle cache, a document or file as one file), before the records
  that name it. Only a changed version is written (`roomlog.CopyState`,
  kept with the workspace, not the session); a version over
  `MaxDeliverableCopyBytes` (8 MiB) is not copied and `artifacts.md` says
  so with its size; a pass writes at most `MaxDeliverableCopyPassBytes`
  (32 MiB) and the rest follow. `artifacts.md` names each copy; the session
  guidance says a copy is read-only and to copy it into the agent's own
  files to change it. `.room/` is bnbloc-owned
  (`TestBnblocOwnedPathAgreesEverywhere` pins `.room/deliverables/…`): never
  published, synced or packed.

## Known limits

- A workspace keeps the agent config an earlier session wore. On an
  `agentProfile` core the core rewrites the instructions file per session,
  and the documented contract asks it to replace the skills directory
  wholesale; the bridge's own fallback for a core without `agentProfile`
  writes `.agents/skills/<name>/` and cannot delete (Core Protocol has no
  file delete). A sandbox workdir is reset by the bridge before every
  fresh session, as before.
- A site version that drops a file leaves the old file in its copy
  (nothing under `.room/` is deleted).

## Adapter follow-up (bnbloc-acp)

- `materializeProfile` replaces only the skills the profile names; skills
  of an earlier session in the same workspace stay. Clear the profile's
  skills directory before writing (the documented contract).
- Nothing else: the adapter keeps workspaces on disk across restarts and
  answers `404 workspace_not_found` for one it does not hold.

## Tests

`services/bridge/workspaces_test.go` (continuity across a content change,
a stale rebind and a bridge restart; a gone workspace replaced; room tools
on a session without content; unknown capabilities retried; deliverable
copies written once and refreshed on a new version),
`internal/roomlog/copies_test.go` (site, inline, blob and oversize copies;
unchanged versions read and written nothing; the pass bound), the sandbox
fence tests (a fresh session on a clean tree in the same workspace), and
`TestPromptBudgets` (guidance 668/700, team lead 750/750).
