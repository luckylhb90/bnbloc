# Branch consolidation into `internal` dropped or mis-merged uncommitted work

Date: 2026-10-07
Status: fixed (a9e9132)

## Reproduction

After merging `components-workbench-redesign`, `chore/unified-consolidation-20261006` and
`feature/desktop-update-handoff-20261005` into `internal` (`1be3204`), every branch tip was an
ancestor of `internal`, yet comparing each worktree's uncommitted files and both stashes
(`stash@{0}`, `stash@{1}`) against `internal` showed:

- `apps/desktop/src-tauri/tauri.conf.json` (staged) dropped `bundle.externalBin`
  (the bridge sidecar) and the updater `pubkey`; `Cargo.toml` (staged) re-introduced the
  `internal` feature with optional `reqwest`/`sha2`/`futures-util`/`tokio`. Both came from a
  working copy that predated `b89ea52`, which removed `tauri.internal.conf.json` and the
  feature. The resulting app would ship without the bridge and a plain build would not
  compile.
- `1be3204` itself did not build: `web/src/lib/api.ts` imported the deleted
  `./sidecarPublish`, and `RoomView`/`MessageFeed` carried two desktop-handoff
  implementations side by side.
- Authenticated release downloads (`ReleaseDownloadButton`) were never wired into
  Agents, RunAgentDialog, AgentCoreEmptyState and ReleaseHub, so gateway artifacts (which
  require a session bearer) were linked with a bare `<a href>`.
- The generic intake drop zone still accepted only `.json`; the intake classifier was an
  older draft (valid JSON without a `kind` reported as invalid JSON, contrary to the PRD).
- PRD/bug/runbook updates from the worktrees were missing.

## Root cause

The consolidation merged branch commits but carried uncommitted worktree changes by
copying/stashing files by hand. Files were taken from whichever snapshot was at hand, not
reconciled against the newer committed design, and the result was committed without a
build.

## Fix

- Desktop config restored to the committed (`b89ea52`) design; only the 0.1.3 version bump kept.
- Final intake classifier, tests and multi-format drop zone taken from the components worktree;
  test fixtures use neutral product names.
- Single handoff wiring in `RoomView`/`MessageFeed` (from the consolidation worktree).
- `ReleaseDownloadButton` wired into the four download surfaces.
- Missing PRD, bug and runbook updates applied; runbook build command updated to
  `cargo tauri build` (no internal config/feature since `b89ea52`).
- Removed the unused `mut` left in `updater_client.rs`.

The standalone desktop-handoff worktree and the components worktree's
`sidecar_util.rs` draft were verified to be superseded by `internal` and were not taken.

## Verification

- Web: `tsc -b`, file-length check, `vite build`, 5 lib tests, 21 script tests pass.
- Go: `go build ./...` and `go test ./scripts/check-public-boundary` pass (in a Linux container).
- Desktop: `cargo check --tests` clean; `cargo test --lib` 30/30 (18 sidecar tests included).

## Prevention

One branch, no ad-hoc worktrees/stashes for in-flight work; build and test before committing a
merge.
