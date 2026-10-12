# Gateway image fails to build: the web stage type-checks tests that read Go fixtures (2026-10-12)

## Symptom

After the code-health batches were deployed, every backend ran the new commit,
but the gateway and the web bundle stayed on the previous one. `bnbloc version`
reported commit skew. The old gateway sent the old internal request shape to
the new backends, which refused it, so most pages showed errors.

## Root cause

The gateway image's `web-full` stage copies only `web/` and runs `pnpm build`,
which is `tsc -b && vite build`. `tsc -b` type-checked `*.test.ts` too. Batch 1
and Batch 5 added tests that import golden fixtures shared with Go:

- `internal/wire/testdata/*.json`
- `internal/membership/testdata/abilities.json`

Those files don't exist in the image stage, so `tsc` failed and the image never
built. Every in-repo check passed (local CI, GitHub CI, the desktop build),
because all of them build from the full checkout. No check reproduced the
image's layout.

## Fix

- **Tests excluded from the production type-check.** The production `tsc -b`
  (`web/tsconfig.json`) excludes `*.test.ts`. `web/tsconfig.test.json`
  type-checks the tests from the full checkout, and `scripts/ci.sh web` runs it
  (`pnpm run typecheck:tests`). Tests keep their shared fixtures and are still
  type-checked.
- **New boundary rule B1.** A production source under `web/src` may not import
  from outside `web/`.
- **Isolated build.** `scripts/ci.sh web` rebuilds `web/` in a temp dir with
  the image's layout (`web_isolated_build`). Any future dependency on files
  outside `web/` fails CI instead of the deploy.

## Verification

- `docker build --build-arg SERVICE=gateway --build-arg WEBUI=full .` failed
  before the fix and succeeds after it.
- A probe file importing a Go fixture from `web/src/lib` fails both B1 and the
  isolated build.
