# Per-Service Deployment Environment

Environment variables **grouped by service**, for wiring each deployment (one
container / Deployment per service). Every entry is verified against the code
that reads it in `services/<name>/cmd/main.go`. For the variable-by-variable
reference and semantics, see [Environment Variables](./environment-variables.md);
this document is the per-service checklist. `deploy/compose.prod.yml` and the
Helm chart (`deploy/k8s`) render exactly this matrix.

## Shared rules (apply to every service)

- **`BNBLOC_ENV=production` on every service.** It makes each dev fallback
  (unsigned internal identity, in-memory store, dev login verifier, ephemeral
  session key) fatal at startup, so a misconfigured service crash-loops
  visibly instead of serving in dev mode.
- **`BNBLOC_INTERNAL_KEY` must be identical across all services.** The
  gateway signs `X-Bnbloc-User` with it; the backends verify that signature.
  A single mismatched value makes that backend return 401 for every request.
- **`BNBLOC_MYSQL_DSN` points every stateful service at one shared database.**
  The account needs CREATE, ALTER and INDEX — services apply the schema
  themselves on start (idempotent, serialized by a migration lock).
  `parseTime=true` required; append `&tls=true` for a managed instance.
- **`*_BASE_URL` are in-cluster service addresses** (e.g. `http://bnbloc-room:8080`),
  not public domains.
- **Probes**: every service exposes `GET /healthz` (liveness: always 200 while
  the process serves) and `GET /readyz` (readiness: 503 when its database
  ping fails; always 200 for relay). Point liveness at `/healthz`, readiness at
  `/readyz`. The distroless images ship `/usr/local/bin/healthcheck <url>` for
  container HEALTHCHECKs (no shell or curl inside).
- **Version**: every internal service answers `GET /version`
  (`{service, commit, builtAt, instance}`, next to `/healthz`, no auth); the
  gateway aggregates them at the super-admin `GET /v1/platform/versions`.
  Build images with `--build-arg CI_COMMIT_SHA=<commit>` or the commit reads
  `unknown` (see docs/environment-variables.md#build-identity-build-argument-not-runtime).
- **Metrics**: `GET /metrics` is on each internal service's main listener
  unless `BNBLOC_METRICS_ADDR` moves it to a dedicated internal listener; the
  gateway never serves it publicly (see the gateway table).
- Listen address defaults are shown per service; override with
  `BNBLOC_<SERVICE>_LISTEN_ADDR` only if needed.

## gateway (`:8080`)

The public edge: authenticates sessions, proxies to backends, serves the
embedded SPA. Stateless apart from the shared database — scale horizontally.

| Variable | Required | Notes |
| --- | --- | --- |
| `BNBLOC_MYSQL_DSN` | yes | Tenant registry, platform settings, components. |
| `BNBLOC_INTERNAL_KEY` | yes | Signs `X-Bnbloc-User`. Same value as all backends. |
| `BNBLOC_SESSION_SIGNING_KEY` | yes | Signs session JWTs. All replicas share it. |
| `BNBLOC_OIDC_ISSUER_URL` + `BNBLOC_OIDC_CLIENT_ID` | no | OIDC relying party. Unset in production = direct login disabled; sign in via an installed auth-provider (SSO). |
| `BNBLOC_SUPER_ADMINS` | yes | Comma-separated verified subject ids/emails (cross-tenant admins). |
| `BNBLOC_ROOM_BASE_URL` | yes | `http://bnbloc-room:8080` |
| `BNBLOC_REALTIME_BASE_URL` | yes | `http://bnbloc-realtime:8082` |
| `BNBLOC_ACTIVITY_BASE_URL` | yes | `http://bnbloc-activity:8081` |
| `BNBLOC_SHAREAGENT_BASE_URL` | yes | `http://bnbloc-shareagent:8083` |
| `BNBLOC_RELAY_BASE_URL` | optional | `http://bnbloc-relay:8085` — only to report the relay's `/version` in `GET /v1/platform/versions`; unset leaves the relay out. |
| `BNBLOC_TRUSTED_PROXIES` | only with direct login | CIDRs of the edge proxy, so the per-IP direct-login throttle sees real clients. Not needed for SSO-only production (direct login disabled). |
| `BNBLOC_ALLOWED_ORIGINS` | if cross-origin | Extra browser origins for CORS. |
| `BNBLOC_LOCAL_AGENT_TOKEN` | optional | Shared daemon bearer; empty disables that path. |
| `BNBLOC_CORES` (+ `BNBLOC_CORE_<NAME>_URL`) | optional | Explicit cores only; installed packages need none. |
| `BNBLOC_METRICS_ADDR` | for metrics | Internal `/metrics` listener (`:9090` in compose/Helm). The public listener never serves `/metrics` (404); unset = no gateway metrics. Never route or publish it. |

The gateway does **not** proxy the relay (it only reads the relay's `/version`);
the edge proxy routes `/relay/*`.
Replicas share auth state (SSO hand-offs, refresh-token families) through
the database and reconcile installed components from platform settings every
30 s, so no sticky routing is needed.

## room (`:8080`)

Room directory, membership, invites; fans artifact changes to realtime.

| Variable | Required | Notes |
| --- | --- | --- |
| `BNBLOC_MYSQL_DSN` | yes | Rooms/members/invites. |
| `BNBLOC_INTERNAL_KEY` | yes | Same value as gateway. |
| `BNBLOC_REALTIME_BASE_URL` | yes | `http://bnbloc-realtime:8082` — artifact-change fan-out, blob artifact versions + thumbnails (stored in realtime's blob store over the signed internal route), and the feed/attachment side of handoff package export/import. |
| `BNBLOC_ARTIFACT_MAX_BYTES` | no | Max bytes of one blob artifact version. Unset = 100 MiB; must be 1..268435456 (realtime's 256 MiB blob cap) or the service refuses to start. Raise the edge proxy's body limit for `POST /v1/rooms/*/artifacts/*/versions` with it. |

## realtime (`:8082`) — singleton

Presence + business-event fan-out over SSE; durable chat history; attachment
and artifact blobs. **Run exactly one replica**: the SSE hub (per-room
sequence counter, replay buffer, presence) lives in process memory, so a
second replica would split each room's subscribers and sequences.

| Variable | Required | Notes |
| --- | --- | --- |
| `BNBLOC_MYSQL_DSN` | yes | Chat history + hub sequence seed. |
| `BNBLOC_INTERNAL_KEY` | yes | Same value as gateway. |
| `BNBLOC_ROOM_BASE_URL` | yes | `http://bnbloc-room:8080` — membership checks. |
| Blob storage | yes | A volume at `/var/lib/bnbloc/attachments` (`BNBLOC_ATTACHMENTS_DIR`), or `BNBLOC_ATTACHMENTS_S3_BUCKET` (+ `_REGION`, `_ENDPOINT`, credentials, `_KEY_PREFIX`, `_PATH_STYLE`). |
| `BNBLOC_ATTACHMENTS_S3_DIRECT_DOWNLOADS` | optional | S3 only: opt-in downloads answer 302 to a 5-minute presigned URL (clients must reach the bucket). Default off: everything streams. |
| `BNBLOC_ATTACHMENTS_S3_PUBLIC_ENDPOINT` | optional | S3 base URL clients reach, when `_ENDPOINT` is internal-only. Empty = `_ENDPOINT`. |
| `BNBLOC_PUBLIC_URL` | recommended | Public web origin; member-notification deep links. Unset = no links. |
| `BNBLOC_WEB_PUSH_VAPID_PUBLIC_KEY` / `_PRIVATE_KEY` | optional | Browser push notifications (a pair from `bnbloc keys gen-vapid`; the private key is a secret). Unset = channel off. |
| `BNBLOC_WEB_PUSH_VAPID_SUBJECT` | with the keys | `mailto:<ops address>`; defaults to an https `BNBLOC_PUBLIC_URL`. |

Realtime also runs the notification outbox worker (signed delivery to the
installed notifier components, see [notifications.md](./notifications.md)); it
must reach each notifier's `endpoint.baseUrl` on the private network (the
package's `permissions.network` is enforced). With
the VAPID keys set it also delivers browser push notifications: realtime then
needs egress to the browsers' push services (public https endpoints; it never
connects to a private, loopback or link-local address for them, and honours
`HTTPS_PROXY` when one is configured).

Realtime streams `text/event-stream`; the edge proxy must not buffer responses
and needs a long idle timeout.

## activity (`:8081`)

Agent-activity ingest, dedup, durable bridge jobs, and reply fan-out.

| Variable | Required | Notes |
| --- | --- | --- |
| `BNBLOC_MYSQL_DSN` | yes | Agent activity, replies, bridge jobs. |
| `BNBLOC_INTERNAL_KEY` | yes | Same value as gateway. |
| `BNBLOC_ROOM_BASE_URL` | yes | `http://bnbloc-room:8080` — membership. |
| `BNBLOC_REALTIME_BASE_URL` | yes | `http://bnbloc-realtime:8082` — the path agent replies reach clients. Without it replies persist but never reach clients. |
| `BNBLOC_ACTIVITY_ALLOW_SERVE_ALL` | never in prod | Dev-only single-host serve-all bridges; fatal in production. |
| `BNBLOC_PUBLIC_URL` | recommended | Links in usage budget warnings (queued to the shared outbox; realtime delivers). Unset = no links. |

## shareagent (`:8083`)

Shared-agent (borrow) authorization, audit, usage.

| Variable | Required | Notes |
| --- | --- | --- |
| `BNBLOC_MYSQL_DSN` | yes | Borrow grants + audit. |
| `BNBLOC_INTERNAL_KEY` | yes | Same value as gateway. |
| `BNBLOC_ROOM_BASE_URL` | yes | `http://bnbloc-room:8080` — membership. |
| `BNBLOC_PUBLIC_URL` | recommended | Deep links in borrow-request notifications (queued to the shared outbox; realtime delivers). |

## relay (`:8085`) — singleton

ICE config, rendezvous, and the WebSocket byte-stream relay for desktop file
sync. No database, no svcauth (endpoints are authorized by the sync-layer
HMAC). **Run exactly one replica**: pairing state is in process memory, so two
replicas would leave peers on different pods unpaired.

| Variable | Required | Notes |
| --- | --- | --- |
| `BNBLOC_RELAY_STUN_SERVERS` | optional | Comma-separated STUN servers (only for real p2p). |
| `BNBLOC_RELAY_ENDPOINTS` | optional | Comma-separated advertised endpoints. |

Deployment-layer requirements: the edge routes `https://<host>/relay/*` to it
(forwarding the WebSocket upgrade, long idle timeout), and its public entry is
**TLS only** — the sync token rides the stream in cleartext.

## cli — nightly smoke job (optional)

Not a service: the operator CLI image (`SERVICE=cli`,
`deploy/prod/cli/Dockerfile`, entrypoint `bnbloc`) run on a schedule as
`bnbloc smoke --alert-on-failure` (Helm `smoke.*` CronJob, compose profile
`smoke`; see [production-deployment.md](./production-deployment.md#nightly-smoke)).
No database, no internal key; it talks to the gateway like any client.

| Variable | Required | Notes |
| --- | --- | --- |
| `BNBLOC_GATEWAY` | yes | The gateway Service (`http://bnbloc-gateway:8080`) or the public URL. |
| `BNBLOC_API_TOKEN` | yes | A super-admin-issued API token (from a Secret); its subject must be in the gateway's `BNBLOC_SUPER_ADMINS` to send the failure alert. |

## Coverage matrix

| Service | Port | Replicas | MYSQL_DSN | INTERNAL_KEY | ROOM_BASE_URL | REALTIME_BASE_URL | Extra |
| --- | --- | --- | --- | --- | --- | --- | --- |
| gateway | 8080 | 1..n | ✅ | ✅ | ✅ | ✅ | session key, OIDC, super-admins, activity/shareagent URLs, trusted proxies |
| room | 8080 | 1..n | ✅ | ✅ | — | ✅ | — |
| realtime | 8082 | **1** | ✅ | ✅ | ✅ | — | blob storage |
| activity | 8081 | 1..n | ✅ | ✅ | ✅ | ✅ | — |
| shareagent | 8083 | 1..n | ✅ | ✅ | ✅ | — | — |
| relay | 8085 | **1** | — | — | — | — | edge route `/relay/*` |

All Go services additionally take `BNBLOC_ENV=production` and, optionally,
`BNBLOC_AWS_SECRET_ID` / `BNBLOC_AWS_REGION`.
