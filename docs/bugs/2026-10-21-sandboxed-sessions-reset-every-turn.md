# A sandboxed agent's session was dropped and its files reset after every turn (2026-10-21)

Seen on a test deployment, in a release room where one member (here
`maya`) works with her own Claude Code agent through a Core Protocol core
(here `acme`), desktop sandbox mode `all`.

## What happened

1. 19:44–20:06 the agent released six services one after another. It ran
   on the host that hour, where it had installed the release CLIs and
   signed them in. One session (`s-c81…`) carried the whole conversation.
2. From 20:08 the agent ran in the member's own sandbox VM. Each of the next
   four turns started a **new session** (`s-eae…`, `s-2d7…`, `s-45d…`,
   `s-fa3…`) in the same workspace, with no notice in the room.
3. The member asked to publish the room (as a room component). The agent,
   in a session that had never seen the conversation, read "publish" as
   "deploy the services" and reported the release tools missing ("not
   installed", "this workspace is not a git repository"). It answered an
   earlier question "from the chat history only".

Evidence on the member's computer:

- The core's session store held 4 sessions for the workspace, each with one
  turn. The bridge's `workspaces.json` kept the workspace's fence baseline
  (non-empty) but no session binding for it.
- The sandbox workdir's protected paths fingerprinted as **empty**:
  `CLAUDE.md`, `.room/` and `.claude/skills/` were gone and only the
  agent's `drafts/` remained. Only `resetProtectedPaths` removes them, and
  only a fence breach does so at the end of a turn.
- 12 of the member's 15 sandboxed Claude Code workspaces looked the same.
  The 3 intact ones were from 2026-10-14 (before the core's change below)
  and one long single-turn run.
- One of the four turns made no tool call at all, so no guest command
  wrote anything, and its session was still dropped.

## Root cause

The sandbox turn fence (`services/bridge/sandbox_fence.go`) treats every
change to the protected paths after its baseline as a guest's write. Two
writes that are not a guest's landed after the baseline:

1. **The core wrote the instructions file after answering the create.**
   Since 2026-10-16 the core keeps a session's instructions in the
   workspace's `CLAUDE.md`, rewritten at the start of every turn (a resumed
   conversation reads it again; core-providers.md "A workspace outlives its
   sessions"). It wrote it inside the turn's goroutine, after a model
   catalog read and the sandbox preparation, while the create was already
   answered. The bridge took the fresh session's baseline as soon as the
   create returned, almost always before the file existed. At the turn's end
   `CLAUDE.md` was "added": breach, sessions dropped, files reset. The next
   turn was fresh again, so the loop never ended.
2. **The bridge took a follow-up turn's baseline before sending its input.**
   A core that rewrites the instructions file on input (two channel
   sessions of one workspace wear different text, since each one's guidance
   names its channel) changed it after the baseline, so every channel switch
   read as a breach.

The contract never said when a core's workspace files must be in place. The
fence's comment assumed "everything there when create returns is bnbloc's
and the core's", and the core's change in another repository broke that
silently.

## Fix

- **The contract** (`docs/architecture/core-providers.md`): a core writes a
  turn's workspace files before it answers the create or input that starts
  the turn, and writes none of the protected paths while the turn runs.
- **The core** writes the profile's files (`Profile.WriteFiles`)
  synchronously in `startTurn`, before every caller answers. The turn no
  longer writes them. Test: `TestProfileFilesAreInPlaceWhenTheTurnIsAcknowledged`
  holds the catalog read as a slow discovery does. It fails with the fix
  reverted (`CLAUDE.md: no such file` at the create).
- **The bridge** takes a follow-up turn's baseline once the core has answered
  its input (failed or not), the same rule as a fresh session's
  (`runTurn`). Test: `TestSandboxFence_aCoresOwnTurnFilesAreNoBreach`, two
  channel sessions alternating while the core rewrites `CLAUDE.md` on every
  input. With the fix reverted it gives 4 sessions and 1 input instead of
  2 and 3.
- **Never silent.** A breach logs the changed paths
  (`fenceChanges`, `changes="CLAUDE.md added"`), and the next session's
  notice says why it is new ("its workspace's protected files changed
  during its last turn"). Tests: `TestSandboxFence_aBreachIsNeverSilent`
  (fails with the notice removed) and `TestFenceChangesNameThePaths`.
- **The smoke asserts the property.** `bnbloc smoke --scenario long-task`
  has a `session` line: every reply came from one agent session. Its
  `memory` line alone passed for an agent in a new session every turn,
  because the room's history holds the codeword. Test: the "a new session
  every turn" case of `TestSmokeLongTaskNamesWhatBroke`.

## Why it was not caught

- **Lens 4 (failure and recovery):** the reset was built as a rare safety
  path and posted nothing. Nobody asked what would show it firing on every
  turn. Lens 4 now asks that.
- **Lens 9 (external contracts):** the timing the fence relied on lived in
  the bridge's comment, not in the core contract. The core's change in
  another repository could not see it. Lens 9 now asks where a relied-on
  timing is written and checked.
- **Lens 12 (observability):** the release check's memory question passed
  in a fresh session, since it was a proxy for the property. Lens 12 now
  asks for the property; the smoke's `session` line checks it.
- LESSONS.md: "Make every self-healing path loud, and assert the property,
  not a proxy".

## Related, from the same room (not fixed here)

- **The agent's environment changed without a word.** The same room ran on
  the shared sandbox machine (11:00), on the host (19:44) and on the
  member's own machine (20:08). A drive slot is keyed by its execution kind
  (`driveKey`), so each change meant a new workspace and session, and the
  tools installed in the other place were gone. Mode `all` with the sandbox
  not ready runs an own drive on the host (`decideSandbox`), unannounced.
- **"Publish this room" was read as "deploy".** In a release room whose
  expert deploys on request, the agent never considered saving the room as
  a room component (`components` save, kind `room`).

Landed in: the bridge, smoke and docs in the commit that adds this record; the core's fix in its own repository.
