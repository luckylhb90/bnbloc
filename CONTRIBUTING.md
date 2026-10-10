# Contributing to bnbloc

Thanks for your interest! Bug reports, ideas and pull requests are all
welcome.

## Before you start

- **Bugs:** open an issue with steps to reproduce, what you expected and what
  happened. Include the version (`bnbloc version`, or the commit shown in the
  Platform console) and relevant logs, with secrets removed.
- **Features:** open an issue first to discuss the idea. bnbloc records
  features as short design notes under `docs/prd/` and bugs under
  `docs/bugs/`. A larger change usually starts with one of those.

## Development

```sh
make up                 # full stack with Docker on http://localhost:7090
scripts/ci.sh           # every check CI runs
scripts/ci.sh go        # or one stage: go | web | desktop | secrets
```

- **Go:** `go build ./... && go vet ./... && go test ./...`. Set
  `BNBLOC_TEST_MYSQL_DSN` to run the MySQL integration tests.
- **Web:** `cd web && pnpm install && pnpm build`, and unit tests with
  `node --test 'src/**/*.test.ts'` (Node 24).
- **Desktop:** see [`apps/desktop`](apps/desktop). Rebuild the web bundle and
  the bridge sidecar before building the app.

## Pull requests

- Keep each change focused, and include tests for new behaviour and for the
  bug you fixed.
- `scripts/ci.sh` must pass.
- Database changes need a new migration (never edit a released one); see the
  notes in `internal/store/migrations.go`.
- A change to configuration or deployment updates every deploy surface
  (`compose.yml`, `deploy/compose.prod.yml`, `deploy/k8s`, the all-in-one
  entrypoint) and `docs/environment-variables.md` together.

## Contributor License Agreement

Before your first pull request is merged, the CLA bot asks you to accept the
[Contributor License Agreement](CLA.md) with a one-line comment. You keep
ownership of your work; the CLA lets the project keep distributing it,
including under other terms in the future. Everything already released stays
available under that release's open-source license.

The bnbloc name and logo are covered by the [trademark policy](TRADEMARKS.md).
