# A working copy's files are not deliverables; Files has folders (2026-10-27)

Status: built 2026-10-27 (bridge: desktop 0.1.119; web: desktop and the gateway image).

## 1. Why

An agent asked to `git clone` a repository into its workspace filled the
room's Files with the repository's biggest files (a schema, migrations,
lock files, design docs: 25 of them). The publisher takes every file
written during the turn (its mtime), and a clone writes every file of the
repository then; only `maxPublishFiles` (24 per pass) bounded it.

And Files is one flat grid: with many deliverables from one project, there
is no folder to open, although every deliverable carries its workspace path.

## 2. What it is

1. **Files git tracks are never published automatically.** Before a pass
   publishes, the bridge finds, for each changed path, the working copy it
   is in (the nearest folder of the workspace, the workspace included, with
   a `.git` entry) and asks git which of those paths it tracks
   (`git ls-files -z`, with `core.fsmonitor` off, hooks to `/dev/null` and
   `safe.directory=*`: the repository is the agent's, so nothing in it
   runs). Tracked paths leave the pass, sites included. What git does not
   track (a report or an image the agent wrote, inside the repository or
   not) publishes as before. A file the person handed for editing
   (`file_edit.go`) still publishes to its own file.
2. **Files has folders.** Deliverables are shown by their path's folders:
   a folder card per first-level folder, opening a folder shows its files
   and folders, a breadcrumb goes back. A toggle returns the flat grid.
   Search and the filters search everything, flat.

## 3. Decisions (asked and answered 2026-10-27)

1. Inside a working copy, only files git does not track become
   deliverables (not "nothing in a working copy", not "nested repositories
   only"). When git cannot answer for a working copy (no git, an error),
   its paths are taken as tracked: the rule exists to keep a repository out
   of Files, and an unknown answer must not let it in (logged).
2. Folders are derived from the deliverables' paths in the web; no live
   project tree (a later PRD if wanted).

The workspace's folder is the sandbox workdir or the core's reported
session workdir; a core that reports none publishes as before.

## 4. Placement

| Part | Where | Why |
|---|---|---|
| Which workspace files publish | platform (bridge publisher) | the publisher is the bridge's; no component seam exists for it |
| The folder view | platform (web Files view) | presentation of the room's own artifacts |

Name test: "git" is named in the bridge as a fact of the workspace (a
working copy marker), as `node_modules` already is.

## 5. Design review

| Lens | Answer |
|---|---|
| Actors | The agent's workspace is untrusted: git runs with nothing of the repository's config able to run a command (fsmonitor, hooks); `ls-files` runs no filter. |
| Time | One `git ls-files` per working copy per pass. |
| Scale | A repository of 100k files: one listing per pass (bytes, no reads of content). |
| Failure | git missing or failing: the working copy's paths are not published (decision 1), logged once per pass. |
| Defaults | No workdir known: as before. |
| Release | Bridge (desktop) and web; nothing stored changes. |
| Load on agents | None. |

Pre-mortem: *a report written in the repo disappears* — untracked files
publish; *a repository at the workspace root hides everything* — only its
tracked files; *git hangs on a huge repo* — the listing is the index, read
once.
