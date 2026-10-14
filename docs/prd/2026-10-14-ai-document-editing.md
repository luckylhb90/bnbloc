# AI edits a document in place (2026-10-14)

Status: proposed. Builds on `docs/prd/2026-10-14-office-previewers.md`
(the file previews this edits), `docs/prd/2026-10-09-preview-workbench-and-previewer-components.md`
(the workbench and the previewer SDK) and
`docs/prd/2026-10-14-multi-member-rooms.md` (seats, borrowing, records).

## 1. Why

A member looking at a deck, a report or a sheet in the workbench wants to
say "make slide 4's title shorter" or "add a summary row" right there and
see the file change. Today they go back to the chat, describe which file
and where, and hope the agent finds it.

Several things go wrong today:

- The agent writes its result as a new file, or in a directory the
  platform never publishes.
- The agent cannot see a deliverable published during its own turn
  (`.room/` records refresh at turn start).
- The member gets a new artifact instead of a new version of the one they
  were looking at.

Desktop assistants now let a person open a document from their own disk,
ask an AI to change it, and keep working in the same file. Members expect
the same of bnbloc, for room files and for local files.

## 2. What exists (checked against the code)

- **Versions and undo.** An artifact is a line of immutable versions. A
  publish is a compare-and-swap on the current version:
  `PublishArtifactVersion(…, baseVersion)` refuses a base other than the
  current one (`ErrArtifactVersionConflict`), and stores
  `parentVersion = baseVersion`.
- **Following the latest.** A workbench tab that follows the latest version
  reloads when one arrives and shows an "Updated · vN" mark. Its version
  picker offers "Restore as new version", which publishes an older version's
  bytes on the current one (`ArtifactTabView.tsx`). Undo needs nothing new.
- **One stable id per agent file.** An agent's file has one id,
  `artifactIDForFile(room, seat, owner, rel)`. Rewriting the same path
  publishes the next version of the same artifact. The id includes the
  seat, so a borrower's `report.md` is a different artifact from the
  owner's (`services/bridge/artifacts.go`).
- **On-demand publishing.** `publishArtifactNow` publishes one file now:
  it creates or attaches the artifact, then publishes on the current
  version with `sourceJobId`/`sourceSeq`. The periodic and end-of-turn
  publisher skips:
  - dependency and hidden paths;
  - files under build-output directories (`dist`, `build`, `out`);
  - anything past `maxPublishFiles` (24) in a pass.
- **What the agent can read.** It has the room's newest deliverables under
  `.room/deliverables/<id>/` and attachments under `.room/attachments/`.
  `read_artifact` reads any version of a deliverable or an attachment.
- **The previewer frame protocol.**
  - Frame to host: `ready`, `toolbar.set`, `state.send`, `thumbnail`,
    `error`, `title`.
  - Host to frame: `init`, `file`, `toolbar`, `state`, `resize`.
  - There is no selection message.
  - Previewer manifests are strict-parsed (`DisallowUnknownFields`).
- **Attachments.** Chat attachments open in the workbench as their own
  tabs (`f:<id>`). They are immutable and have no versions.
- **The desktop shell.**
  - Tauri plugins: updater, deep-link, single-instance. There is no dialog
    or filesystem plugin and no file association.
  - The main window disables the native drop handler, so a dropped file
    reaches the web layer as an HTML5 `File` without its path.

## 3. Rules

1. **Where it lives.**
   - The workbench file tab gets an "Ask AI" panel beside the preview.
   - The panel is not a new chat. It shows the room conversation's
     messages about this file and their replies, posts into the room's
     current channel, and everyone in the room sees the request and the
     result.
   - The panel's composer is the room composer: the same agent picker
     (default the member's own agent), expert and model choices, and
     borrowing rules.
2. **A message can name a file.**
   - A chat message gains an optional `fileRef`:
     `{artifactId | attachmentId, version, selection?}`.
   - The message text also names the file in words ("deck.pptx v3,
     slide 4: …"), so the record reads naturally and an old bridge still
     understands the request.
3. **The platform puts the file in front of the agent.** For a message
   with a `fileRef`, the bridge places the referenced version's bytes in
   the agent's workspace and adds one line to the turn input:
   `Edit <path> (<name> v<N>[, <selection label>]); saving it there updates the room's file.`

   Which path:
   - **The agent's own file** at that version: its own path, already in
     the workspace.
   - **Any other artifact version, or an attachment:** materialized at
     `edits/<name>`, replaced if stale.

   The agent gets no new tool, rule or guidance paragraph.
4. **Saving publishes the next version of the same file.**
   - **Explicit publish.** When the turn ends (and at each periodic pass),
     a rewritten target path is published as a version of the referenced
     artifact.
     - It goes to that artifact even if the agent's own id rule
       (`artifactIDForFile`) would produce a different id.
     - It is published explicitly, regardless of the ignore rules and the
       per-pass cap.
     - Its producer is the editing job, as for any agent publish.
   - **Conflict.** The version store only publishes on the current
     version, so an edit of v3 published while v4 exists lands on v4
     (`parentVersion` 4). The new version records `editedFrom: 3`, a new
     version field. The panel then says "Edited from v3; v4 changed
     meanwhile — compare", and the compare tool opens v3 against the new
     version.
   - **Attachments.** An attachment is immutable: editing one publishes a
     new deliverable named after it ("deck (edited).pptx"), whose first
     version records `editedFrom` as the attachment id. The panel moves to
     the new deliverable's tab.
   - **No change.** A turn that leaves the file unchanged posts a calm
     notice: "No changes to deck.pptx."
   - **New files.** A turn that writes other files publishes them as
     today.
5. **The selection is context, not instruction.**
   - Previewers may send `preview.selection {label, detail}`. The new
     capability `selection` gates it.
     - **pptx:** the slide in view, plus the selected shape's text, if
       any.
     - **docx:** the selected text and its page.
     - **xlsx:** the selected range (`Revenue!B3:D5`) and its values.
   - The host shows the selection as a removable chip in the panel
     composer and sends it in `fileRef.selection`.
   - The turn input carries it fenced as the file's data (bounded to 2,000
     characters), never as the member's words.
6. **Suggestions are data.** A previewer package may declare up to 6
   `suggestions` ({label, prompt}), for example "Summarize the key points",
   "Write speaker notes", "Tighten the wording", "Check the structure".
   The panel shows them on an empty thread. Platform code holds no
   suggestion text.
7. **Undo is the version picker.** Every edit is a version, and "Restore
   as new version" undoes it. No separate undo stack.

## 4. Local documents

A member opens a document from their own disk in the desktop app, edits it
with AI in the same panel, and saves the result back.

1. **Entry points (desktop only).**
   - File → Open… (a native dialog).
   - Dragging a file onto the window outside a chat composer. A drop onto
     the composer still attaches.
   - The OS "Open With → bnbloc" for the formats a previewer claims.
   - `bnbloc-desktop <file>` from a shell.

   Each delivers an absolute path to the shell. The browser client has
   none of these; it keeps upload-as-attachment.
2. **Where it lands.**
   - By default: the member's own private room "My files", created on
     first use. It is invite-only and has only them, so nothing is shared
     until they share it.
   - Dropped into an open room: that room.
3. **The upload is stated up front.** The first time, the open says
   "Opening uploads a copy to <organization>'s bnbloc so your agent can
   work on it; the original stays where it is." The member can tick "don't
   ask again". The copy is an ordinary deliverable: org storage, retention,
   share rules.
4. **The path never leaves the machine.**
   - The desktop keeps `path ↔ (room, artifactId, opened version, file
     hash, mtime)` in its own state file (owner-only, `files.rs`).
   - The gateway sees a file name only.
   - Reopening the same path offers its existing artifact and uploads a
     new version only if the bytes changed.
5. **Save back.** A local-file tab shows "Save to <name>" with the chosen
   version (default the current one).
   - The shell checks that the local file still has the hash it was opened
     with. If it changed, it asks: overwrite, save as a copy beside it
     (`<name> (bnbloc v5).pptx`), or cancel.
   - The overwrite is atomic (temp file, then rename).
   - The replaced bytes stay recoverable: the version line already holds
     them, and the shell also moves the old file to the OS trash when the
     platform has one.
   - The shell never writes a path the member did not open.
6. **Local-only mode (later, M4).**
   - Applies when the member's agent runs on the same machine (a desktop
     core).
   - The agent edits a working copy in the file's own folder: a confined
     write grant to that one file (`hostConfine.allowWrite`).
   - Nothing is uploaded: the panel previews the local bytes, and versions
     are local snapshots.
   - It is off by default, and offered when the organization allows it.
7. **What the desktop needs.**
   - A dialog plugin (open dialog).
   - File reads and writes in Rust, through `files.rs`'s atomic writer;
     not a general filesystem plugin exposed to the web.
   - A native drop event that yields paths, on the window outside the
     composer. The web layer keeps HTML5 drops for attachments.
   - Bundle `fileAssociations` for the claimed extensions, plus the macOS
     open-file event in the single-instance handler.
   - Narrow commands: `open_local_file() -> {name, bytes, token}`,
     `save_local_file(token, bytes, overwrite)`. The web never sees a path,
     only an opaque token the shell maps back.

## 5. Design review

| Lens | Answer |
|---|---|
| Actors | Every member who can see the file can ask; each request runs on the asker's chosen agent (their own, or a borrowed one under the access-request rule). Two members editing one file produce two versions in arrival order; the second records `editedFrom` and gets the "changed meanwhile" note. A borrower's edit publishes to the referenced artifact explicitly, not to their seat's own id. Local files belong to one member's private room until shared. |
| Time and lifecycle | A long edit runs as any turn: progress strip, no fixed deadline. A tab open on v3 while v5 arrives follows the latest (existing). A `fileRef` to a deleted artifact or expired attachment fails the request with a sentence before the turn starts. A local path that moved or was deleted since opening: save-back offers "save as…". A desktop restart keeps the path map (state file). |
| Scale and cost | Materializing a version copies at most the room's file cap per request (one file). The selection detail is capped at 2,000 characters. The thread view filters the already-loaded room feed by `fileRef`; no new history query. The local path map holds one row per opened file, pruned when its artifact is deleted. |
| Failure and recovery | The agent fails, or writes nothing: no version, plus a notice. A publish conflict is rebased on the current version, recorded, never lost. The bridge cannot fetch the version: the request fails before the turn ("couldn't load deck.pptx v3"). Save-back fails: the local file is untouched (atomic rename) and the error is shown. An overflow or restart mid-turn: existing recovery; the edited file stays in the workspace and publishes on the next pass. |
| Boundaries and privacy | The panel thread is room conversation: visible to the room, as every agent request is. A local file is uploaded only after the stated consent and lands in a private room. The path stays on the machine. The selection is file data and travels only to the asker's agent. |
| Abuse and untrusted input | File content and selection text are untrusted: fenced in the turn input, never interpreted as the member's instruction. The `fileRef` is validated server-side: the room's own artifact or attachment, and a version that exists. The shell writes only the path the member opened, through a token it issued; the web cannot name a path. |
| One owner | The bridge's explicit target publish is the one way an edit becomes a version (no second publisher). `fileRef` validation lives in realtime chat post. The panel reuses the room composer and the feed model. Selection lives in the frame protocol (SDK + `pkgmeta` vocabularies, kept equal by the existing vocabulary test). Local path ↔ artifact lives only in the shell's state file. |
| Defaults and fallbacks | The default agent is the member's own (the composer's preference). No selection means the whole file. A previewer without `selection` shows no chip. No suggestions means an empty-state hint only. Save-back defaults to the current version, and on conflict to "save as a copy". |
| External contracts | The chat message gains `fileRef` (optional; old clients ignore it). Artifact versions gain `editedFrom` (optional). Previewer protocol: the `preview.selection` message and the `selection` capability. Previewer manifest: `suggestions` (strict parse: a pkgmeta schema change, so new packages with suggestions need the new gateway). Desktop: new Tauri commands and plugins, and file associations. |
| Load on users and agents | Members click the file and type; no file naming, no copying paths. Agents get one line naming a path, and the selection as fenced data, but no new tool, rule or guidance paragraph. |
| Release and compatibility | Order: gateway/realtime (`fileRef`, `editedFrom`, manifest field), then web, then the desktop (bridge + shell). An old bridge gets the message text, which names file and version; the agent can still read it with `read_artifact`, but the result publishes under the agent's own id (a new artifact), as today. The panel says "update the desktop app to save edits as versions" when the target bridge is older. Previewer packages publish their new version with `--rollout optin` first. |
| Observability | Metrics: `edit_requests` and `edit_versions_published` (outcome: version, no-change, conflict-rebased, failed); `local_open` and `local_save` (outcome: written, copy, conflict-cancel, error). Each published edit version's summary names the request ("edited by maya/claude: shorten slide 4 title"). |

**Pre-mortem.** It shipped and a month later it failed badly. The three
most likely reasons, and what this design does about each:

1. **"I asked to change the deck and got a second deck."** The agent
   wrote to a new path, a build-output directory, or a copy. The target
   path is named in the turn input and published explicitly, outside the
   ignore rules and the cap. A turn that leaves the target untouched while
   writing a same-type file says so ("deck.pptx unchanged; you made
   deck-v2.pptx"), and the member can "Use as the new version" (one click,
   a restore-style publish).
2. **"Two of us edited and one change vanished."** Strict CAS rebases the
   edit on the current version and records `editedFrom`, and the panel
   offers the compare. Nothing silently overwrites; both versions exist.
3. **"My local file got clobbered" (or "my file was uploaded without
   asking").** Save-back is hash-checked, atomic, and the old bytes stay
   recoverable. The upload is stated before the first open, and the room
   is private.

## 6. Verification

- **Realtime:** `fileRef` validation: own room only; existing version or
  attachment; bounded selection; refused for a deleted artifact.
- **Bridge:**
  - The target path is materialized for own and foreign versions and
    attachments.
  - The one-line turn input (golden).
  - The explicit publish goes to the referenced artifact from a borrower
    seat and from an `out/` path.
  - Conflict rebase records `editedFrom`.
  - No-change notice.
  - Attachment edit yields a new deliverable with `editedFrom`.
- **Previewer SDK/pkgmeta:**
  - `preview.selection` parse and bounds.
  - The capability and `suggestions` vocabularies, kept equal by the
    existing vocabulary test.
- **Web:**
  - The thread filter by `fileRef`.
  - The chip lifecycle.
  - The suggestion chips only on an empty thread.
  - The "update the desktop" notice for an old bridge.
- **Desktop (Rust):**
  - Token ↔ path map round trip.
  - Hash-conflict branch.
  - Atomic overwrite.
  - Copy naming.
  - A path not opened is refused.
- **Release scenario:**
  - An agent edits `samples/deck.pptx` v1 → v2 from the panel, across a
    bridge upgrade mid-turn.
  - The two-member scenario gains "both edit one file".

## 7. Milestones

- **M1 — edit a room file.**
  - The "Ask AI" panel, `fileRef`, materialize plus one line, and the
    explicit target publish with `editedFrom` and the conflict note.
  - The no-change notice.
  - The old-bridge notice.
- **M2 — point at a part.**
  - `preview.selection` in the pptx, docx and xlsx previewers.
  - The selection chip.
  - Manifest `suggestions` in the three Office previewers.
- **M3 — local documents.**
  - The dialog, drop-with-path, "Open With" and command-line entry points.
  - The private "My files" room.
  - The consent notice.
  - The path map.
  - Save back with conflict handling.
- **M4 —**
  - attachments as edit sources ("deck (edited).pptx");
  - visual slide-by-slide compare;
  - local-only mode for desktop cores.

## 8. Follow-ups

- Inline accept/reject of an edit (a diff to review before it becomes the
  current version), for text and sheet cells first.
- Edit several files in one request (`fileRef` as a list).
- Previewer thumbnails of edited slides in the panel thread.
