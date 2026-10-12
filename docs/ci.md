# CI

`scripts/ci.sh [go|web|desktop|secrets|all]` is the single list of checks.
`.github/workflows/ci.yml` only runs one stage per job; any other runner, or a
developer before pushing, runs the same script. Add checks to the script, never
to the workflow file.

| Job | Stage | Runner | Needs |
|---|---|---|---|
| `go` | `scripts/ci.sh go` | Linux **with Docker** (the `mysql` service container) | Go (`go.mod`) |
| `web` | `scripts/ci.sh web` | Linux | Node 24, pnpm 10 |
| `desktop` | `scripts/ci.sh desktop` | macOS | Go, Node 24, pnpm 10, Rust stable |
| `secrets` | `scripts/ci.sh secrets` | Linux | gitleaks 8 (full history, `fetch-depth: 0`) |

Triggers: every push, pull requests, a nightly scheduled run of the default
branch (catches drift in toolchains and images without a push), and manual
`workflow_dispatch` (Actions → ci → Run workflow).

## Running on GitHub Enterprise Server

The same workflow file runs on github.com and on a GitHub Enterprise
Server host. Nothing in it is host-specific; the host-dependent parts are read
from Actions **variables** (Settings → Secrets and variables → Actions →
Variables, at repository or organization level). Unset variables keep the
github.com defaults, so github.com behaviour does not change.

| Variable | Default | Value |
|---|---|---|
| `BNBLOC_CI_LINUX_RUNS_ON` | `"ubuntu-24.04"` | JSON: one label in quotes, or a label array, e.g. `["self-hosted","linux","x64"]` |
| `BNBLOC_CI_MACOS_RUNS_ON` | `"macos-14"` | JSON, e.g. `["self-hosted","macos","arm64"]` |
| `BNBLOC_CI_MYSQL_IMAGE` | `mysql:8.4` | a reachable copy of the MySQL 8.4 image, e.g. `registry.example.internal/mirror/mysql:8.4` |

The runner variables are parsed with `fromJSON`, so a single label must keep
its quotes (`"linux-x64"`); a bare word is invalid JSON and the job fails to
start. The `vars` context needs a GitHub Enterprise Server release with
Actions configuration variables (3.8 or later).

### Runners

GitHub-hosted labels (`ubuntu-24.04`, `macos-14`) do not exist on GitHub
Enterprise Server unless the instance provisions them. Register self-hosted
runners at the repository or organization level and point the variables at
their labels:

- **Linux runner for `go`** must run Docker (service containers only work on
  Linux runners with Docker) and be able to pull `BNBLOC_CI_MYSQL_IMAGE`. The
  job reaches MySQL on `127.0.0.1:3306`, so a runner that itself runs in a
  container needs the published port reachable (or Docker-in-Docker with host
  networking).
- **macOS runner for `desktop`** builds the Tauri shell natively;
  Xcode command-line tools are required (no other system packages).
- Each runner needs outbound HTTPS to the module/package registries the stages
  use (Go module proxy, npm registry, crates.io), or configured internal
  mirrors (`GOPROXY`, an npm registry in `.npmrc`, a cargo source
  replacement) set in the runner's environment.

### Actions from github.com

The jobs use `actions/checkout`, `actions/setup-go`, `actions/setup-node`,
`pnpm/action-setup` and `dtolnay/rust-toolchain`. GitHub Enterprise Server
ships `actions/*` bundled, but versions may lag; third-party actions resolve
only when one of these is in place:

1. **GitHub Connect with "Server can use GitHub.com actions"** enabled by a
   site admin (Enterprise settings → GitHub Connect). Actions are then fetched
   from github.com on demand. This is the expected setup.
2. **Mirrored actions**: without GitHub Connect, sync each action repository
   (at the referenced major tag) into the instance with the `actions-sync`
   tool, under the same `owner/name` (create the `pnpm` and `dtolnay`
   organizations, or the `actions` org copies for the bundled ones).

The setup actions also download toolchains (Go from `go.dev`/the
`actions/go-versions` manifest, Node from `nodejs.org`, pnpm from npm, Rust via
`rustup`). On runners without that egress, preinstall the toolchains (Go per
`go.mod`, Node 24, pnpm 10, Rust stable) in the runner's tool cache or image;
the setup actions then resolve the cached versions.

### Secrets

The `ci.yml` stages need **no secrets** (the nightly smoke needs one, below). `go` uses throwaway MySQL credentials local
to the job, and the MySQL integration tests enable themselves from
`BNBLOC_TEST_MYSQL_DSN` set in the workflow. The tests never use the DSN's
database itself: each test binary (or test) creates its own throwaway
`<database>_t*` database on that server and drops it afterwards
(`internal/testmysql`), so the DSN's user needs CREATE and DROP DATABASE
(the job uses the container's root). A private registry for
`BNBLOC_CI_MYSQL_IMAGE` is expected to be reachable by the runner's own
Docker credentials (`docker login` on the runner host).

## Post-deploy smoke (not in CI)

CI checks the source tree; it has no deployed gateway. After every deploy, run
the message-path smoke from an operator machine (see
[`docs/cli.md`](cli.md#post-deploy-smoke) and the
[deployment checklist](production-deployment.md#verify-a-deployment)):

```sh
bnbloc smoke --gateway https://<host>               # PASS/FAIL per step, non-zero exit on failure
bnbloc smoke --gateway https://<host> --agent codex # also wait for your own agent's reply
```

A deploy pipeline with a reachable gateway can run the same command as its
final step with an API token in `BNBLOC_API_TOKEN` (below). `BNBLOC_TOKEN`
also works but holds a 1-hour session, so it suits a one-off run only; a
stored `bnbloc login` rotates its refresh token on every run, so it only fits
a host that keeps `~/.bnbloc/config.json`.

## Nightly smoke

Two ways to run it nightly: the deployment schedules it itself (a Helm
CronJob, a compose `smoke` service run by the host's cron, or the CLI inside
the all-in-one image — no CI system needed; see
[production-deployment.md](production-deployment.md#nightly-smoke)), or the
workflow below. Both run the same command with the same API token.

`.github/workflows/nightly-smoke.yml` runs the smoke against a deployed
gateway every night (02:41 UTC) and on demand (Actions → nightly-smoke → Run
workflow, optionally naming an agent). It builds the CLI from the checkout
(`go build ./cmd/bnbloc`) and runs:

```sh
bnbloc smoke --alert-on-failure [--agent <agent>] [--alert-to <ids>]
```

A failed run alerts the platform admins through the deployment's installed
notifier components (`POST /v1/platform/alerts`, type `admin_alert`, see
[notifications.md](notifications.md#admin-alerts)) with the gateway and every
failed step, and the job fails. When the alert itself cannot be sent (no
notifier reaches the recipients, the token is refused, the gateway is down)
the CLI prints why on stderr and the job still fails — the red workflow run
is the fallback signal.

| Setting | Kind | Value |
|---|---|---|
| `BNBLOC_SMOKE_GATEWAY` | variable | gateway URL, e.g. `https://bnbloc.example.com`. Unset: the job is skipped. |
| `BNBLOC_SMOKE_API_TOKEN` | **secret** | an API token whose subject is a platform super-admin (alerts are super-admin-only). |
| `BNBLOC_SMOKE_AGENT` | variable, optional | also wait for this agent's reply (`--agent`, e.g. `codex`); the token's subject must own the agent and its bridge must be online. |
| `BNBLOC_SMOKE_ALERT_TO` | variable, optional | comma-separated alert recipients (member ids); default every `BNBLOC_SUPER_ADMINS` entry. |
| `BNBLOC_CI_LINUX_RUNS_ON` | variable | runner labels, as for `ci.yml`. The runner needs HTTPS to the gateway. |

### The API token

Refresh tokens rotate on every use and a CI secret cannot store the rotated
value; a session lasts one hour. The unattended credential is a
super-admin-issued **API token**: stored hashed, listed, revocable, audited
(extension audit, kind `api-token`), never a session itself. The CLI
exchanges it once per run at `POST /v1/auth/api-token` for a 1-hour session,
which re-resolves the subject's tenant and role every time (removing the
subject from `BNBLOC_SUPER_ADMINS` takes effect at the next run).

Issue one (as a super-admin) for a service identity and list it in
`BNBLOC_SUPER_ADMINS`, or for yourself:

```sh
bnbloc api-tokens create --label nightly-smoke --subject svc:smoke --ttl-days 180
# add svc:smoke to BNBLOC_SUPER_ADMINS (gateway) so it may send the alert
bnbloc api-tokens list
bnbloc api-tokens revoke <id>        # e.g. when rotating the secret
```

The token is printed once; store it as the `BNBLOC_SMOKE_API_TOKEN` secret.
A token's subject is either the issuer or a `svc:<name>` service subject — a
super-admin cannot mint a token that acts as another person. Tokens expire
after `--ttl-days` (default 90, at most 365); rotate before expiry by issuing
a new one, updating the secret, and revoking the old one. A smoke run with
`--agent` needs the service subject to own an agent with a bridge online, so
an agent check usually runs with a token issued for the operator who runs
that bridge.
