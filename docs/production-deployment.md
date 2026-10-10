# Production Deployment

Deploys the bnbloc control plane: generic service containers behind a TLS
reverse proxy, against an externally provisioned MySQL. The single-host
compose path is the primary one; the Kubernetes chart is at the end.

## What runs

| Service | Port | Replicas | Role |
| --- | --- | --- | --- |
| gateway | 8080 | 1..n | public API + embedded SPA; proxies the others |
| room | 8080 | 1..n | rooms, members, invites, artifacts |
| activity | 8081 | 1..n | agent activity, bridge jobs, reply fan-out |
| realtime | 8082 | 1 (compose); 1..n (Kubernetes, by room ownership) | chat, attachments/artifact blobs, SSE event stream (in-process hub per room owner) |
| shareagent | 8083 | 1..n | agent borrowing authorization + audit |
| relay | 8085 | 1 (compose); 1..n (Kubernetes, by session ownership) | desktop file-sync byte relay (in-process pairing per session owner) |
| installed core sidecars | package-defined | — | Core Protocol adapters run by the desktop sidecar manager |

Production core adapters are private component packages: build and sign them
from their own repository, then install them through Platform → Components; the
generic stack ships no adapter containers.

The database is **not** part of the deployment. `deploy/compose.prod.yml`
disables the `mysql` container from `compose.yml` (via a profile Compose never
activates) and drops every `depends_on: mysql`. Every stateful service (all
but relay) connects to the DSN in `BNBLOC_MYSQL_DSN`.

Only the gateway (`127.0.0.1:7090`) and the relay (`127.0.0.1:7091`) are
published, both on loopback. The reverse proxy is the single public listener.

## Prerequisites

- Docker Engine with Compose v2.24+ (the overlay uses `!override`/`!reset`)
- A DNS record pointing at the host
- Ports 80/443 free for the reverse proxy
- A reachable MySQL 8 instance with an empty `bnbloc` database already created,
  and an account holding CREATE, ALTER and INDEX in addition to DML — the
  services apply the schema themselves

## Deploy

```bash
# 1. Secrets. Fill every CHANGE-ME; never reuse a copy across deployments.
cp .env.production.example .env.production
chmod 600 .env.production
$EDITOR .env.production     # DSN, internal key, signing key, OIDC, SUPER_ADMINS

# 2. Start. Both -f flags are mandatory: a bare `docker compose` also
#    auto-loads compose.override.yml, an internal-only build.
docker compose -f compose.yml -f deploy/compose.prod.yml \
  --env-file .env.production up -d --build

# 3. TLS. Point the proxy at the host and start it.
$EDITOR deploy/Caddyfile     # replace bnbloc.example.com
caddy run --config deploy/Caddyfile
```

The overlay sets `BNBLOC_ENV=production` on every service: a missing DSN,
internal key, session signing key, or OIDC setting stops that service at boot
(visible as a restart loop with the reason in `docker compose logs`) instead of
silently starting in dev mode.

The schema applies itself on first start: each service embeds
`internal/store/schema.sql` (a fresh database gets the full `CREATE TABLE`
set; `deploy/schema.sql` is a verbatim copy for review/manual DDL, and CI fails
if they diverge) and runs ordered migrations through a `schema_migrations`
ledger, serialized across services by a MySQL advisory lock. There is no manual
migration step — but the target database must exist and the account needs DDL
rights. `store.OpenMySQL` adds the client settings it needs
(`multiStatements`, and `interpolateParams`, which saves a server-side
prepare round trip per query), so only `parseTime=true` (plus `tls=true` for
a managed instance) belongs in your DSN. The connection charset must be
utf8mb4, the schema's charset: a DSN naming another charset or collation is
refused at startup, because client-side parameter escaping is unsafe in some
multibyte charsets. Each service process holds at most 20 connections and
keeps them open while idle; size the server's `max_connections` for 20 per
stateful service replica plus headroom.

The full variable reference is [Environment Variables](./environment-variables.md).

## Required settings

| Variable | Why it matters |
| --- | --- |
| `BNBLOC_MYSQL_DSN` | Full DSN of the external database. Required by every stateful service. |
| `BNBLOC_INTERNAL_KEY` | Signs `X-Bnbloc-User` between services; without it any container on the network could impersonate any user. |
| `BNBLOC_SESSION_SIGNING_KEY` | Signs browser sessions. Leaking it allows forging any identity; rotating it logs everyone out. |
| `BNBLOC_OIDC_ISSUER_URL` + `BNBLOC_OIDC_CLIENT_ID` | The identity provider. Production refuses the dev verifier, which accepts any identity string. |
| `BNBLOC_SUPER_ADMINS` | Cross-tenant platform admins; set real verified subject ids/emails. |

Optional: `BNBLOC_LOCAL_AGENT_TOKEN` (shared daemon bearer; empty disables
that path), `BNBLOC_ALLOWED_ORIGINS`, S3 attachment settings,
`BNBLOC_AWS_SECRET_ID` to load secrets from AWS Secrets Manager.

### Components and plugin packages

Cores, auth providers, and plugin sidecars are distributed as signed packages
through the Components API. In production:

- **Signed only.** The overlay pins `BNBLOC_EXT_ALLOW_UNSIGNED` empty on the
  gateway and realtime, so a package installs only when its detached signature
  verifies against a trusted publisher key (`POST /v1/platform/component-keys`,
  or the Workbench's trust panel). Register the publisher key BEFORE the first
  install.
- **Rollout is the member gate.** Each component publishes with
  `auto|optin|off`; `optin` components reach only members who installed them,
  `off` ones reach no member surface. `/v1/cores`, `/v1/components`, and
  `/v1/cores/{name}/artifact` enforce the same gate, and `GET /v1/artifacts/{key}`
  serves members only the artifacts of components visible to them (plus
  published desktop releases).
- **Artifacts carry provenance.** Sidecar binaries are staged through
  `POST /v1/platform/artifacts` (streamed to realtime's blob store, 256 MiB
  cap — the reverse proxy allows that size on this one path) before the signed
  envelope references them; the desktop refuses a binary whose sha256 does not
  match the signed package.

Supervision of sidecar processes is exclusively the desktop's sidecar
manager — the server never launches a component process; persisted records
rehydrate at gateway boot.

## What the production overlay changes

`compose.yml` targets a developer laptop. `deploy/compose.prod.yml`:

- sets `BNBLOC_ENV=production` everywhere and pins the dev-only switches
  (`BNBLOC_EXT_ALLOW_UNSIGNED`, `BNBLOC_ACTIVITY_ALLOW_SERVE_ALL`) empty
- removes the `host.docker.internal` alias — a remote gateway cannot reach a
  member's local daemon. `compose.yml` itself carries no core-specific host
  wiring; add a compose overlay (`-f`) for site-specific host wiring in
  development
- replaces the `0.0.0.0:7090` publish with a loopback bind, and publishes the
  relay on `127.0.0.1:7091` for the proxy's `/relay/*` route
- pins the compose network to `172.29.80.0/24` and trusts its gateway address
  (`BNBLOC_TRUSTED_PROXIES=172.29.80.1/32`) — the address the host-side proxy
  arrives from — so the per-IP limits (direct login and the API-token exchange, public share reads) see real client IPs. Change both if the
  range collides on your host
- adds `restart: unless-stopped`, JSON log rotation (20 MB × 5), memory/CPU
  limits, and readiness healthchecks: each distroless image probes itself with
  the bundled `/usr/local/bin/healthcheck` against `/readyz` (503 while its
  database is unreachable), and `depends_on: service_healthy` orders start-up
  on that readiness

## Reverse proxy

`deploy/Caddyfile` carries a working Caddy config (validated with
`caddy validate`) and an nginx equivalent in comments. Non-optional settings:

- **Disable response buffering.** Room events are Server-Sent Events; a
  buffering proxy withholds frames and the room looks frozen
  (`flush_interval -1` in Caddy, `proxy_buffering off` in nginx).
- **Per-path body limits.** 32 MiB by default (attachments allow 25 MiB,
  artifact content 2 MiB); 256 MiB on `POST /v1/platform/artifacts` only,
  where sidecar binaries are staged.
- **Route `/relay/*` to the relay** (`127.0.0.1:7091`), forwarding the
  WebSocket upgrade. Desktop bridges then use
  `BNBLOC_RELAY_BASE_URL=https://<host>` for live file sync.
- **TLS in front of the relay is mandatory.** The bridge's file-sync stream
  carries the room's sync token — `HMAC(BNBLOC_SYNC_SECRET, roomId)` — in
  cleartext and relies on transport TLS. Never expose the relay port directly.
- **Long timeouts** for SSE/WebSocket streams (30 min idle) and large uploads
  (10 min body read). In Caddy these are global server options.

## Verify a deployment

```bash
docker compose -f compose.yml -f deploy/compose.prod.yml ps   # 6 services, all (healthy)
curl -sS -o /dev/null -w '%{http_code}\n' https://<host>/      # 200

# Per-service readiness from inside each container (distroless: no shell)
for s in gateway room activity realtime shareagent relay; do
  port=$(case $s in activity) echo 8081;; realtime) echo 8082;; shareagent) echo 8083;; relay) echo 8085;; *) echo 8080;; esac)
  printf '%s: ' $s
  docker compose -f compose.yml -f deploy/compose.prod.yml exec -T $s \
    /usr/local/bin/healthcheck http://127.0.0.1:$port/readyz && echo ready
done
```

Confirm the hardening actually took effect:

```bash
# production mode everywhere; no dev-mode warning lines
docker compose -f compose.yml -f deploy/compose.prod.yml logs | grep -i "dev mode\|dev verifier\|in-memory"

# loopback publishes only, and no host bind mount
docker inspect <project>-gateway-1 --format '{{json .NetworkSettings.Ports}}'
docker inspect <project>-gateway-1 --format '{{range .Mounts}}{{.Source}} {{end}}'

# SSE streams rather than buffers: expect text/event-stream + chunked,
# and no Content-Length
curl -sS -N -D - -o /dev/null -H "Authorization: Bearer <tok>" "https://<host>/v1/rooms/<id>/events"
```

Then run the end-to-end smoke of the message path (throwaway room, stream-first
chat echo, history, optional agent reply, cleanup; non-zero exit on any FAIL —
see [`docs/cli.md`](cli.md#post-deploy-smoke)):

```bash
bnbloc login --gateway https://<host>       # once per operator machine
bnbloc smoke --gateway https://<host>       # add --agent <your agent> when a bridge is online
```

Run it after every deploy and upgrade; a deploy is done only when it passes.
Schedule it nightly with alerting — inside the deployment (below, no CI
system needed) or through the [`nightly-smoke` workflow](ci.md#nightly-smoke).

## Nightly smoke

The deployment can run its own nightly smoke: the CLI ships as an image
(`--build-arg SERVICE=cli` on the root `Dockerfile`, or the generated
`deploy/prod/cli/Dockerfile`; entrypoint `bnbloc`) and a scheduled job runs

```bash
bnbloc smoke --alert-on-failure [--agent <agent>] [--alert-to <ids>]
```

against the internal gateway address. A failed run alerts the platform admins
through the installed notifier components (`POST /v1/platform/alerts`, see
[cli.md](cli.md#alert-on-failure)) and exits non-zero; when the alert itself
cannot be sent (no notifier, token refused, gateway down) the reason is on the
job's stderr and the failed job/exit status is the fallback signal.

The credential is an API token, never a person's sign-in: issue one for a
service subject and make that subject a super-admin (alerts are
super-admin-only), see [ci.md](ci.md#the-api-token):

```bash
bnbloc api-tokens create --label nightly-smoke --subject svc:smoke --ttl-days 180
# add svc:smoke to BNBLOC_SUPER_ADMINS (gateway)
```

`--agent` additionally waits for that agent's reply; the token's subject must
own the agent and its bridge must be online, so a service subject usually runs
without it (message path only).

**Compose.** The production overlay defines a profile-gated `smoke` service
(never started by `up`). The host's scheduler runs it — no scheduler process
inside a container to babysit, and cron's mail/log of a non-zero exit is the
fallback signal. Put the inputs in `.env.production`:

| `.env.production` | Meaning |
| --- | --- |
| `BNBLOC_SMOKE_API_TOKEN` | the API token (required) |
| `BNBLOC_SMOKE_GATEWAY` | default `http://gateway:8080` (the gateway container); the public `https://` URL also covers the reverse proxy |
| `BNBLOC_SMOKE_AGENT` | optional `--agent` |
| `BNBLOC_SMOKE_ALERT_TO` | optional comma-separated recipients; default every `BNBLOC_SUPER_ADMINS` entry |

```bash
# rebuild the CLI image on every upgrade: `up --build` skips profile-gated
# services, and `run` only builds a missing image, never a stale one
docker compose -f compose.yml -f deploy/compose.prod.yml --env-file .env.production build smoke
# crontab (host time zone; CRON_TZ=Europe/Berlin on cronie to pin one)
41 2 * * * cd /srv/bnbloc && docker compose -f compose.yml -f deploy/compose.prod.yml --env-file .env.production run --rm smoke
```

A systemd timer (`OnCalendar=*-*-* 02:41:00`, `Persistent=true`) running the
same `docker compose ... run --rm smoke` works as well.

**Kubernetes.** Set `smoke.enabled=true` and create the token Secret; the
chart renders a `bnbloc-smoke` CronJob (`concurrencyPolicy: Forbid`, no
retries — a retry would alert twice) using the `<repository>-cli` image:

```bash
kubectl create secret generic bnbloc-smoke --from-literal=apiToken=bnbloc_at_...
helm upgrade bnbloc deploy/k8s --reuse-values \
  --set smoke.enabled=true --set smoke.apiTokenSecretRef.name=bnbloc-smoke \
  --set smoke.timeZone=Europe/Berlin
```

| Value | Default | |
| --- | --- | --- |
| `smoke.schedule` / `smoke.timeZone` | `41 2 * * *` / controller zone | cron schedule; IANA zone (Kubernetes ≥ 1.27) |
| `smoke.gatewayURL` | `http://bnbloc-gateway:8080` | the public URL also covers the ingress |
| `smoke.agent`, `smoke.alertTo`, `smoke.extraArgs` | empty | `--agent`, `--alert-to`, extra flags (e.g. `--timeout 180s`) |
| `smoke.apiTokenSecretRef` | `{name: "", key: apiToken}` | name required when enabled |
| `smoke.activeDeadlineSeconds` | `900` | upper bound of one run |

Watch the job with `kubectl get jobs -l app.kubernetes.io/name=bnbloc-smoke`;
a failed Job (or `kube_job_status_failed` from kube-state-metrics) is the
fallback signal.

**All-in-one.** The all-in-one image also contains the CLI at
`/usr/local/bin/bnbloc` (not supervised). Schedule it from the host against
the in-container gateway, passing the token from a root-only file rather than
the command line:

```bash
41 2 * * * docker exec --env-file /etc/bnbloc/smoke.env <container> bnbloc smoke --gateway http://127.0.0.1:8080 --alert-on-failure
# /etc/bnbloc/smoke.env (0600): BNBLOC_API_TOKEN=bnbloc_at_...
```

On a platform that only runs one container per pipeline, schedule the CLI
image (`deploy/prod/cli/Dockerfile`) as that platform's scheduled job with
`BNBLOC_GATEWAY` and `BNBLOC_API_TOKEN` in its environment.

## Backup

- **Database** — backed up by whoever operates the managed MySQL. Confirm the
  retention and restore procedure with them.
- **`bnbloc-attachments` volume** — attachments and staged sidecar artifacts
  on the realtime service, the only state living on this host. For more than
  one host, switch to S3 with the `BNBLOC_ATTACHMENTS_S3_*` variables. With
  S3, `BNBLOC_ATTACHMENTS_S3_DIRECT_DOWNLOADS=true` sends large downloads
  straight to the bucket through 5-minute presigned URLs (set
  `BNBLOC_ATTACHMENTS_S3_PUBLIC_ENDPOINT` when clients reach S3 under
  another URL than realtime does).

## Data retention

Nothing is deleted until someone sets a retention period. A super admin sets
the platform default in the Platform console (Overview → Data retention); an
organization admin can choose a different period for their own organization
(Organization → Settings → Data retention, "Platform default" follows the
platform). Periods are whole days, at least 31 (monthly usage budgets and
borrow reports read the current month) and at most 36,500.

| Kind | What is deleted |
| --- | --- |
| Room messages | Chat messages and agent replies older than the period, with their message tasks and the activity copy of agent replies, plus chat attachments uploaded before it. When a room's newest message is itself expired (the room was quiet for the whole period), that one row is emptied instead of deleted: it keeps the room's sequence number so later messages never reuse one. |
| Older deliverable versions | Artifact versions older than the period except each artifact's latest version, and public links pinned to a deleted version. |
| Audit log | Borrow decisions and component, member, tenant and session audit entries. Platform-level entries follow the platform default. |
| Finished agent runs | Agent jobs that replied, failed, timed out or were cancelled. Queued and running jobs stay. |

realtime runs the cleanup on every replica; a MySQL advisory lock lets one
replica at a time do an hourly pass. Each pass deletes in batches of 500 rows
along age indexes, then — at most once a day — removes blobs that no row
references any more (attachments, artifact versions and thumbnails, and the
component and desktop-release artifacts named in platform settings). A blob
last stored less than 7 days ago is never removed, which covers sidecar and
desktop artifacts staged before their package is published. Each pass that
deletes something writes a `retention` / `purge` entry to the organization's
audit log; the Platform console shows when the last pass ran and what it
removed; metrics are listed in [monitoring.md](./monitoring.md#retention).

Two rules for the blob store follow from the collection:

- **One deployment per bucket and key prefix** (or per attachments volume).
  The collection deletes every blob under `sha256/` that this database does
  not reference, so a second deployment, a staging copy or a restored test
  database must never share the production bucket prefix.
- **Back up blobs as long as the database.** Restoring a database backup
  older than the last collection brings back rows whose blobs are gone. Keep
  bucket versioning (or blob backups) for at least the database backup
  horizon.

## Upgrade

```bash
git pull
docker compose -f compose.yml -f deploy/compose.prod.yml \
  --env-file .env.production up -d --build
```

Migrations run at service start under an advisory lock (other services wait
up to 600 s for it), so a restart is safe. Roll back by checking out the
previous revision and repeating — migrations do not auto-revert.

Upgrade notes:

- **Data retention (2026-10-11)** adds migration `2026_10_data_retention`:
  four nullable columns on `tenants` and age/blob-key indexes on
  `room_messages`, `attachments` and `artifact_versions`. The index builds
  are online but take time on large tables; watch the first service's logs.

- **First boot after 2026-10-07** on an existing database runs migration
  `2026_10_converge_fresh_schema`, which rebuilds 8 indexes so an upgraded
  database matches a fresh one. On large tables this takes time and holds the
  migration lock; plan a short window and watch the first service's logs.
- **Network pin.** The overlay now pins the compose network subnet; a stack
  created before that must be taken down once (`docker compose ... down`, which
  keeps volumes) so the network is recreated.
- **deviceauthority removed.** The service had no callers and is gone; a stale
  `bnbloc-deviceauthority` container from an older deployment can be removed
  (`docker compose ... up -d --remove-orphans`).

## CI that builds one Dockerfile

Some CI platforms take a single Dockerfile path and expose no build args,
while the root `Dockerfile` takes `--build-arg SERVICE=<name>` (and
`WEBUI=full` for the gateway). `deploy/prod` serves those platforms.

Every image also takes `--build-arg CI_COMMIT_SHA=<commit>` (the build
identity behind `GET /version`, `bnbloc version` and the Platform console's
Deployment card; `.git` is not in the build context). A platform that cannot
pass build args builds images whose commit reads `unknown`, which the
deployment view reports as a problem — pass the commit wherever the platform
allows it.

### Per-service (one pipeline each)

`deploy/prod/<service>/Dockerfile` builds exactly one service with no build
arg. These files are **generated** from the root `Dockerfile` by
`deploy/prod/gen-dockerfiles.sh` (only the `SERVICE`/`WEBUI` defaults differ);
edit the root file and re-run the script — CI fails on drift. Only `gateway`
builds the SPA. Wire them with the environment in
[Per-Service Deployment Environment](./deploy-service-env.md).
`deploy/prod/cli/Dockerfile` is the operator CLI (`./cmd/bnbloc`), the image
of the [nightly smoke](#nightly-smoke) job.

### All-in-one (one pipeline, one container)

`deploy/prod/Dockerfile` compiles all six services and runs them in one
container under a supervisor (`deploy/prod/aio-entrypoint.sh`), exposing only
the gateway on 8080. It defaults `BNBLOC_ENV=production`; provide the same
secrets as the compose deployment as container environment. Inside the
container room binds :8086 (the gateway owns :8080) and services reach each
other over loopback; the supervisor sets that wiring. If any service exits,
the container exits non-zero and the orchestrator restarts the whole unit.

Trade-off: the services share one lifecycle and cannot scale independently,
and the relay is not reachable from outside (it is not published); use the
compose or Kubernetes shape when desktop file sync is needed.

### Web tier (standalone)

`deploy/prod/web/Dockerfile` serves the built SPA from its own non-root nginx
container (`nginxinc/nginx-unprivileged`, uid 101, port 8080), separate from
the gateway (whose image already embeds the same SPA). It does **not** proxy
the API: it only hosts static files with SPA history fallback and a `/healthz`.
The SPA calls the backend over same-origin paths, so the front proxy (ALB,
ingress, or nginx in front) must serve this container and the backend under
one host and own the path routing: `/v1`, `/api` → gateway, `/relay` → relay,
everything else → web. No runtime environment variables are read.

## Kubernetes

`deploy/k8s` is a Helm chart rendering one workload + Service per service
(Deployments; StatefulSets with a headless peer Service for realtime and
relay), an Ingress, and a PVC for realtime's disk blob store. It is validated with
`helm lint`, `helm template`, and kubeconform schema checks; it has not been
exercised against a live cluster in this repository.

Before installing, create the Secrets the values reference (names/keys are
configurable): the MySQL DSN (`bnbloc-mysql/dsn`), the internal key
(`bnbloc-internal/internalKey`), the session signing key
(`bnbloc-session/signingKey`), and optionally the local-agent token and S3
credentials. Set `oidc.issuerURL`/`oidc.clientID` and
`gateway.superAdmins`. Every pod runs with `BNBLOC_ENV=production`, so a
missing Secret key or OIDC value fails the pod at startup with the reason in
its log.

- **Scaling realtime and relay.** Both keep per-key state in process memory
  and scale out by key ownership: every room and member (realtime) or sync
  session (relay) is owned by one pod, picked by rendezvous hashing over the
  ready pods. They run as StatefulSets behind a headless
  `bnbloc-<name>-peers` Service; with `replicas > 1` (or `hpa.enabled`) each
  pod gets `BNBLOC_<NAME>_PEERS` (the headless name, re-resolved every 5 s)
  and `BNBLOC_<NAME>_SELF` (its pod IP). Any pod accepts any request and
  forwards it one hop to the key's owner, so the gateway, room, activity and
  the ingress keep using the normal Service. A pod joining, leaving or
  failing readiness moves only its own keys: their streams close, clients
  reconnect to the new owner and catch up from history (no durable message
  is lost: the new owner seeds each room's sequence from the durable max,
  and a send retried by `clientMsgId` is idempotent). Allow realtime →
  realtime and relay → relay traffic on their ports in any network policy.
  Size the replica count with the load test (`docs/load-testing.md`); an HPA
  works but every scale event moves a share of rooms.
- **Blob storage.** With no `realtime.attachments.s3.bucket`, realtime mounts
  the `bnbloc-realtime-attachments` PVC (ReadWriteOnce: one realtime pod,
  replaced in place). More than one realtime replica requires the bucket —
  the chart fails otherwise and realtime refuses the disk store when peers
  are configured. Credentials come from a Secret or the pod's IAM identity;
  `directDownloads: true` (+ `publicEndpoint` when the endpoint is
  cluster-internal) answers downloads with presigned URLs.
- **Ingress.** One host: `/relay` → relay, `/` → gateway. The default
  annotations target ingress-nginx (256 MiB bodies, no buffering, 30 min
  timeouts); translate them for another controller. Set
  `gateway.trustedProxies` to the controller's pod CIDR so per-IP rate limits
  see real clients.
- **Multiple gateway replicas.** Sessions are portable (shared signing key),
  SSO hand-offs and refresh-token families live in the database, and every
  replica reconciles installed components from platform settings every 30 s,
  so an install/uninstall on one replica reaches the others without a restart
  and no sticky routing is needed. Rate limits live in each replica's memory;
  the chart sets `BNBLOC_GATEWAY_PEERS` (headless `bnbloc-gateway-peers`) so
  each replica enforces 1/N of every budget, N being the live replica count.
- **Probes.** Liveness `/healthz` (never DB-gated), readiness `/readyz`
  (DB-gated).
- **Nightly smoke.** `smoke.enabled` renders a CronJob running
  `bnbloc smoke --alert-on-failure` from the `<repository>-cli` image against
  the gateway Service, with an API token from an existing Secret; see
  [Nightly smoke](#nightly-smoke).
- **Metrics.** The gateway's public port never serves `/metrics` (404); scrape
  the `metrics` port (`gateway.metricsPort`, default 9090) in-cluster and never
  route it from the ingress. Scrape config, alert rules and the metric list:
  [`docs/monitoring.md`](monitoring.md) (`deploy/monitoring/`).
