# Public environment example contains credential-like values

Status: fixed (a9e9132)

## Reproduction

1. Run `go run ./scripts/check-public-boundary --repo . --public-dir /tmp/bnbloc-public-boundary --skip-build`.
2. Inspect `/tmp/bnbloc-public-boundary/.env.production.example`.
3. Before the fix, the generated public tree contained non-placeholder values for `BNBLOC_INTERNAL_KEY`, `BNBLOC_SESSION_SIGNING_KEY`, and `BNBLOC_LOCAL_AGENT_TOKEN`; the scanner reported PASS.

## Root cause

`.env.production.example` is copied into the public materialization because it is not excluded by `.components/private-manifest.json`. The leak scanner checked configured textual patterns but did not detect arbitrary non-placeholder credential assignments.

## Fix

The public template now uses placeholders for all three values. The public-boundary scanner rejects populated credential-like assignments in generated files and includes excluded directories in the report count. Regression coverage checks populated values, safe placeholders, and exclusion counts.

Previously distributed values still require operator assessment and rotation.

## Recovery verification

The placeholder template and regression tests were re-integrated after the source worktree's uncommitted fix was found missing from `internal` (see `2026-10-07-internal-merge-omissions.md`). `go test ./scripts/check-public-boundary` passes (10/10, run in `golang:1.24-alpine` because freshly linked test binaries stall at `_dyld_start` on the dev Mac). The public-tree smoke scan no longer reports the three credential assignments. The scan still fails overall on pre-existing, unrelated boundary findings (internal-marked docs missing from the manifest, product text in tests); those are tracked separately.

`main` previously exited 1 without printing non-boundary errors (e.g. a dangling symlink in the walked tree), so a run could print `status: PASS` and still fail. It now prints the error to stderr.
