# An agent misreads another agent's work as missing (2026-10-12)

Status: fixed (see Landing); pending deploy.

## Repro (seen on QA)

A room has two agents of the same member, on different cores:

- agent A: core `acme`, provider `opencode`, whose targets advertise
  `mcpTransports` and so get the room tools;
- agent B: core `nimbus`, whose targets advertise no `mcpTransports`. It gets
  no room tools, but its core writes workspace files, so it gets `.room/`.

1. The member asks agent A to create an expert. A drafts
   `drafts/weekly-report-assistant/` in **its own** workspace, calls the room
   tool `save_component`, and replies "saved to your Library". That is true:
   the expert is in the member's Library, and the bridge published the draft
   files as room deliverables.
2. The member asks agent B "why wasn't it saved?". B answers confidently and
   wrongly:
   - it looks for the drafts in **its own** workspace, finds only `.room/`,
     and says the files were "never written to disk";
   - it reads `.room/artifacts.md`, which lists the draft files as
     deliverables, and calls it "an optimistic list";
   - it says `save_component` writes to "agent A's own platform library", not
     one it can see.

## Root cause

The room context bnbloc hands an agent does not describe the room model,
and an agent without room tools has no read-only view of the facts it needs:

- Nothing says each agent works in its own workspace. `ComposePointer` and
  `.room/INDEX.md` describe history files only.
- `.room/artifacts.md` (`services/bridge/roomctx.go`, `roomArtifactsMd`)
  lists title, kind, version and `createdBy`, which is the publishing
  bridge's owner (a person), never the agent that produced the file, its
  core, or that the file lives in bnbloc, not in this workspace.
- Nothing says what "the Library" is: bnbloc's Library of the member who
  asked (experts, skill packs, prompt packs, MCP tools), shared by every
  agent and core in the room. An agent without tools cannot see it at all,
  and the room tools' `list_components` lists experts and skill packs only.
- An agent with room tools cannot read another agent's deliverable either:
  no tool returns a deliverable's content.

## Fix

Every point applies to every core, with or without room tools, one owner per
rule:

1. The room model in a few lines (own workspaces, deliverables stored by
   bnbloc and named by their producer, the Library is bnbloc's and shared,
   how to verify with or without room tools). Owner: `roomlog`
   (`SessionGuidance`). It is static, so it is stated **once per session**,
   never per turn: the bridge's `sessionGuidance` puts it at the head of the
   agent profile a session is created with on a core advertising
   `agentProfile` (the seam that carries an expert's instructions, outside
   the binding sum like the session contributors' parts), else at the head
   of the session's first prompt (the expert fallback's prefix).
   `.room/README.md` keeps the same text in the workspace, written only when
   it differs from what the session's workspace holds.
2. `.room/artifacts.md` names each deliverable's producer as
   `owner/provider` and core (from the version's source job, the run record),
   its latest version and id, and says plainly it is not in this workspace.
3. `.room/library.md`: a bounded, read-only snapshot of the requester's
   visible Library (name, kind, version, scope, updated time), from the
   member catalog route acting as the job's requester (`contentuse.Visible`,
   `extpipe.MemberVisibility`: nothing re-derives visibility).
4. Room tool `read_artifact(artifactId, path?, version?)`: a deliverable's
   text, bounded and fenced as untrusted, read with the room's own membership
   check; `list_components` covers the whole Library (prompt packs and MCP
   tools too).
5. `.room/` stays a bnbloc-owned path (`core.BnblocOwnedPath`).

Out of repo: the core whose targets advertise no `mcpTransports` should
advertise them, so its agents get the room tools.

## Landing

- f3811e2 gateway: `GET /v1/components` with a job grant is the requester's
  Library (content and MCP tools; `publishedAtMs`).
- 7f42349 `roomlog`: `.room/README.md`, producers in `.room/artifacts.md`,
  `.room/library.md`; the bridge writes them every turn.
- ca597a0 room tools: `read_artifact`; `list_components` lists the whole
  Library.
- 821ace8 ci: the web stage's cleanup trap (found while verifying).

Tests: `TestReadmeGolden`, `TestArtifactsGoldenNamesProducers`,
`TestLibraryGolden`, `TestDeliverablesCacheAndFailure` (roomlog);
`TestDriveClaimPullWritesRoomFilesAndPointerPrompt` (bridge);
`TestGrantedCatalogIsTheRequestersLibrary` (gateway: another member's
private component never appears); `TestReadArtifact*` against the real room
service (non-member, another room, size cap); `TestListComponents…`.

### Follow-up: static guidance once per session (2026-10-13)

7f42349 rewrote `.room/README.md` every turn and added a README line with a
behavioural clause ("read it before saying another agent's file or
component is missing") to every pull turn's prompt (`ComposePointer`):
static guidance riding every turn. Now (01b2c3f):

- the room model is session guidance (fix point 1): in the profile of an
  `agentProfile` core's session, else in the session's first prompt only;
  the push model's `Compose` no longer inlines it;
- `ComposePointer` is a plain file list again (INDEX, recent, artifacts,
  library), one short line over the prompt before the room model, pinned by
  `testdata/golden/pointer.txt`; `.room/INDEX.md` points at `README.md`;
- `.room/README.md` is written when its content changes
  (`CtxState.ReadmeSum`, `roomlog.ReadmeUpdate`): once per session;
  `artifacts.md` and `library.md` stay per turn (their data changes).

Per-turn pull prompt framing (`ComposePointer("maya/codex", "MSG", nil)`,
chars/4): 143 tokens before 7f42349, 207 at 5cd8f08, 156 now (626 chars).
The room model itself (about 254 tokens) is paid once per session, and the
1.5 KB README write once per session instead of every turn.

Tests: `TestComposePointerGolden`, `TestReadmeUpdateOnlyWhenChanged`,
`TestComposeCarriesNoSessionGuidance` (roomlog);
`TestRoomModelRidesTheProfile`, `TestRoomModelInFirstPromptOnlyWithoutProfile`
(pull and push), `TestDriveClaimPullWritesReadmeOnlyWhenChanged` (bridge).
