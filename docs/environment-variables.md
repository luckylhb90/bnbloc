# Environment Variables

The authoritative environment reference for bnbloc. Every entry is verified
against the code that reads it (`config.FromEnvRaw` / `config.FromEnv` /
`os.Getenv` call sites); a variable that nothing reads does not belong here or
in any compose/Helm file.

For a per-service deployment checklist (which variables each service needs),
see [Per-Service Deployment Environment](./deploy-service-env.md).

Three deployment shapes consume these differently:

- **Compose** (`compose.yml` + `deploy/compose.prod.yml`): the overlay wires
  peers and pins the production switches; you supply secrets and identity via
  `--env-file .env.production` (template: `.env.production.example`).
- **Kubernetes** (`deploy/k8s`): the chart renders wiring and secret
  references; you create the referenced Secrets.
- **All-in-one** (`deploy/prod/Dockerfile`): the supervisor sets every
  internal wiring variable (`BNBLOC_*_BASE_URL`, `BNBLOC_ROOM_LISTEN_ADDR`)
  and defaults `BNBLOC_ENV=production`; you supply secrets and identity.

## Deployment mode

| Variable | Scope | Meaning |
| --- | --- | --- |
| `BNBLOC_ENV` | every Go service | `production` turns every dev-permissive fallback into a fatal startup error: a missing `BNBLOC_INTERNAL_KEY` (svcauth would trust the identity header unsigned), missing `BNBLOC_MYSQL_DSN` (in-memory store), missing `BNBLOC_SESSION_SIGNING_KEY` (ephemeral key), and dev-only switches such as `BNBLOC_ACTIVITY_ALLOW_SERVE_ALL`. Production never uses the dev verifier: without OIDC, direct login (`/v1/auth/login`) is disabled and sign-in is only through an installed auth-provider (SSO). Bootstrap the first super-admin on a fresh SSO-only deployment with `bnbloc mint-admin-token <subject>` run inside the gateway container (subject must be in `BNBLOC_SUPER_ADMINS`; prints a 15-minute token for `BNBLOC_TOKEN`). Any other value or unset = development, where those fallbacks run with loud warnings. Set by the production overlay, the Helm chart, and the all-in-one image. |

## Secrets loading (AWS Secrets Manager)

| Variable | Scope | Meaning |
| --- | --- | --- |
| `BNBLOC_AWS_SECRET_ID` | every Go service | Secret name/ARN whose JSON `SecretString` maps env var names to values, loaded into the process environment at start (only for variables that are unset or empty). Set-but-unloadable is fatal. Unset = no AWS call. |
| `BNBLOC_AWS_REGION` | every Go service | Optional region pin for the secret; unset defers to the AWS SDK default chain. |

## Metrics listener

| Variable | Scope | Meaning |
| --- | --- | --- |
| `BNBLOC_METRICS_ADDR` | every Go service | Internal listen address for Prometheus `/metrics` (e.g. `:9090`). Set: `/metrics` is served only there, never on the main listener. Unset: internal services (room, activity, realtime, shareagent, relay) keep `/metrics` on their main listener, which is reachable only on the private service network, and the gateway — the public edge — serves no metrics at all. The gateway's main listener always answers `/metrics` with 404. A value equal to an internal service's own listen address is the same as unset (logged); on the gateway it refuses to start, since it would publish `/metrics`. Never publish this port. The compose files and the Helm chart set `:9090` for the gateway; the all-in-one image hands the value (default `:9090`) to the gateway process only, since its six services share one network namespace. |

## Required on stateful / authenticated services

Read by gateway, room, realtime, activity, and shareagent. Relay and the
desktop bridge do not read them: relay is stateless and authorizes by the
sync-layer HMAC. The value must be identical across every service.

| Variable | Scope | Meaning |
| --- | --- | --- |
| `BNBLOC_MYSQL_DSN` | gateway, room, realtime, activity, shareagent | External database DSN, e.g. `bnbloc:pass@tcp(host:3306)/bnbloc?parseTime=true`. `parseTime=true` required; `store.OpenMySQL` adds `multiStatements` and `interpolateParams` (client-side parameter escaping: one round trip per query instead of a server-side prepare, execute and close), so the connection charset must be utf8mb4 — a `charset`/`collation` parameter naming anything else is refused at startup. Each service process holds up to 20 connections and keeps them open while idle. Append `&tls=true` for a managed instance. The account needs CREATE, ALTER and INDEX — services apply the schema themselves. Empty = in-memory store (dev only; fatal in production). |
| `BNBLOC_INTERNAL_KEY` | gateway, room, realtime, activity, shareagent | Shared secret authenticating service-to-service calls: the gateway (and every internal caller) signs `X-Bnbloc-User` with it; the backends reject unsigned/mismatched calls with 401. Unset = every backend trusts the header from anything on the internal network (dev only; fatal in production). |

## Gateway

| Variable | Default | Meaning |
| --- | --- | --- |
| `BNBLOC_SESSION_SIGNING_KEY` | ephemeral (dev) | Signs browser session JWTs. Leaking it allows forging any identity; rotating it logs everyone out; every gateway replica must share it. Required in production. |
| `BNBLOC_OIDC_ISSUER_URL` / `BNBLOC_OIDC_CLIENT_ID` | — | OIDC discovery URL and relying-party client id. Both set = OIDC; otherwise development uses the dev verifier (accepts any identity string) and production disables direct login (SSO via an installed auth-provider only). |
| `BNBLOC_SUPER_ADMINS` | — (`root` in the dev compose) | Comma-separated verified subject ids or emails granted cross-tenant Platform admin. A subject entry matches exactly (subjects are case-sensitive); an email entry matches the identity's email case-insensitively. The entries are also the default recipients of an admin alert (`POST /v1/platform/alerts`), as notifier `userId`s. A service subject (`svc:<name>`) of an API token must be listed here to act as a super-admin (e.g. to send the nightly smoke's failure alert). |
| `BNBLOC_ALLOWED_ORIGINS` | — | Comma-separated extra browser origins allowed for cross-origin API calls. Empty allows only the Tauri desktop origins (`tauri://localhost`, `http://tauri.localhost`); same-origin browser clients need nothing. |
| `BNBLOC_AUTH_ACS_PATH` | gateway | Public path the IdP posts assertions to — the ACS URL registered with the IdP (e.g. `/api/sso/acs`). Default `/api/auth/acs`. Must be under `/api/` and match the installed auth-provider's own ACS URL setting, which validates the assertion's recipient. |
| `BNBLOC_TRUSTED_PROXIES` | — | Comma-separated CIDRs/IPs of the reverse proxies in front of the gateway. Their `X-Forwarded-For` is believed (right to left) to find the client IP for the per-IP limits: direct login (`POST /v1/auth/login`) and the API-token exchange (`POST /v1/auth/api-token`) share one guess bucket per IP (a burst of 10, then 5 per minute), and public share reads (`/v1/public/shares/…`) have their own per-IP read and miss budgets (invite redemption is throttled per user; the desktop SSO code exchange is not throttled). Set it on every gateway behind a proxy, SSO-only production included: the API-token exchange and public shares stay reachable there, and without it every client behind the proxy shares one bucket. Unset trusts none (the TCP peer is the client). A malformed entry is fatal. The production overlay sets the compose network gateway (`172.29.80.1/32`); in Kubernetes set the ingress controller's pod CIDR. |
| `BNBLOC_SITES_ORIGIN` | — (sites on the public origin) | A separate https origin, e.g. `https://sites.bnbloc-usercontent.example`, for published sites and interactive apps (docs/prd/2026-10-12-interactive-apps.md §4.7). When set, the gateway serves sites (`/v1/sites/…`, `/v1/public/shares/{token}/site/…`), their `~app/` API and the SDK only when the request's Host is that origin's, answers every other path there 404 (no API route, no web client, no session), and answers site paths on the main origin 404; the site URLs it hands out (member site tickets, a public link's `siteUrl`) are on it, and `GET /v1/public/client-config` names it (the desktop shell's site scheme forwards there). It needs its own DNS name, TLS certificate and an ingress / edge-proxy rule sending it to the same gateway with its Host header (Caddyfile, Helm `gateway.sitesOrigin`); production should prefer a separate registrable domain over a subdomain of the main host. Must be a bare https origin (no path, no query; one trailing slash is the same origin); anything else is a startup error. A value on the host of `BNBLOC_PUBLIC_URL` is no separate origin: it is logged and treated as unset. Unset = sites are served from the gateway's own origin, isolated by their sandbox CSP only. |
| `BNBLOC_TENANT_AUTOPROVISION` | on | `false` locks tenant membership to pre-provisioned tenants instead of auto-provisioning at login. |
| `BNBLOC_DESKTOP_DOWNLOAD_URL` | — | Fallback desktop download link served when the deployment has no curated release history. |
| `BNBLOC_EXT_ALLOW_UNSIGNED` | unset | Dev-only: accept unsigned component packages (see Components below). |
| `BNBLOC_SECRET_STORE_KEY` | — (component settings off) | 32 random bytes, base64 or hex (`openssl rand -base64 32`), sealing component settings at rest (AES-256-GCM, `internal/sealbox`): the platform and organization values admins enter for the settings components declare (`/v1/platform/components/{name}/settings`, `/v1/tenant/components/{name}/settings`). It also seals endpoint components' credentials (a notifier's signing secret, an auth provider's; `internal/componentcred`), which realtime opens to sign notifier deliveries. Every gateway replica shares it, realtime reads the same value (see Member notifications), and the room service reads it for interactive apps' data (see Artifact files (room)). Unset = those settings cannot be stored (a save answers 409; a component that requires one needs setup) and a notifier or auth provider cannot be installed (409); a malformed value refuses to start. Losing or changing it makes stored values unreadable: admins enter them again and rotate every endpoint component's credential. Secret. |

## Internal wiring

The all-in-one supervisor, the production overlay, and the Helm chart set all
of these; supply them yourself only when wiring services by hand. Every listen
address defaults to the port shown.

| Variable | Scope | Default | Meaning |
| --- | --- | --- | --- |
| `BNBLOC_GATEWAY_LISTEN_ADDR` | gateway | `:8080` | Gateway bind address. |
| `BNBLOC_ROOM_LISTEN_ADDR` | room | `:8080` | Room bind address. The all-in-one image moves it to `:8086` to free `:8080` for the gateway. |
| `BNBLOC_ACTIVITY_LISTEN_ADDR` | activity | `:8081` | Activity bind address. |
| `BNBLOC_REALTIME_LISTEN_ADDR` | realtime | `:8082` | Realtime bind address. |
| `BNBLOC_SHAREAGENT_LISTEN_ADDR` | shareagent | `:8083` | Shareagent bind address. |
| `BNBLOC_RELAY_LISTEN_ADDR` | relay | `:8085` | Relay bind address. |
| `BNBLOC_ROOM_BASE_URL` | gateway, realtime, activity, shareagent | — | Room service URL: the membership authority (`membership.FromEnv`; unset is fatal under `BNBLOC_ENV=production`, and in development every room refuses every member) and the gateway's proxy target. |
| `BNBLOC_REALTIME_BASE_URL` | gateway, room, activity | — | Realtime service URL: room fans artifact changes to it, stores blob artifact versions and thumbnails in its blob store, and reads/writes the feed and attachments of handoff packages through it; activity publishes agent replies through it (without it replies persist but never reach clients). |
| `BNBLOC_ACTIVITY_BASE_URL` | gateway | — | Activity service URL. |
| `BNBLOC_SHAREAGENT_BASE_URL` | gateway | — | Shareagent service URL. |
| `BNBLOC_RELAY_BASE_URL` | gateway | — | Internal relay URL (`http://relay:8085`), read only to sample the relay's `GET /version` for `GET /v1/platform/versions`. Unset: the relay is left out of the deployment view. (The desktop bridge's variable of the same name is the relay's public base; see the bridge table.) |

The gateway does not proxy the relay: `/relay/*` is routed by the edge proxy
(`deploy/Caddyfile`, the Helm Ingress) straight to the relay service.

## Scale-out peer sets (realtime, relay, gateway)

realtime and relay keep per-key state in process memory (rooms and member
plugin streams; relay pairings). To run more than one replica, each replica
learns its peers and owns the keys rendezvous hashing assigns it; a request
for a key another replica owns is forwarded there in one hop (SSE and
WebSocket pass through), so callers keep using the load-balanced service URL
(`internal/shard`). Leave both variables of a service unset for a single node
(compose, the all-in-one image, a one-replica chart): it owns every key and
nothing is forwarded. Setting one without the other refuses startup. The Helm
chart sets them when `replicas > 1` or an HPA is enabled.

| Variable | Scope | Default | Meaning |
| --- | --- | --- | --- |
| `BNBLOC_REALTIME_PEERS` | realtime | — | The realtime replica set. One `host:port` is a DNS name re-resolved every 5 s, each address a peer at that port (a Kubernetes headless Service: `bnbloc-realtime-peers:8082`); a comma list of two or more `host:port` is a fixed set. Every candidate is probed every second (`GET /readyz`; two consecutive failures drop it, about 2 s), and a failed call to a live peer re-probes it at once (about 0.25 s under traffic), so a dead or unready replica leaves the set in both forms. With peers configured, startup refuses the disk blob store (more than one replica requires `BNBLOC_ATTACHMENTS_S3_BUCKET`). |
| `BNBLOC_REALTIME_SELF` | realtime | — | This replica's own address as its peers reach it: `host:port` (one of the list's entries in list form), or a bare IP that takes the DNS entry's port (the pod IP from the downward API; IPv6-safe). |
| `BNBLOC_RELAY_PEERS` / `BNBLOC_RELAY_SELF` | relay | — | The same pair for the relay (keys: sync session keys). |
| `BNBLOC_GATEWAY_PEERS` | gateway | — | The gateway replica set, same forms (Helm: `bnbloc-gateway-peers:8080`). The gateway owns no keys; it divides every per-replica rate limit (chat/borrow per subject, guess, public share reads) by the live replica count so N replicas enforce the configured budget in total, not N times it. Unset = one replica. |

### Build identity (build argument, not runtime)

`CI_COMMIT_SHA` is a **build argument** of the root `Dockerfile` (and so of the
generated `deploy/prod/<service>/Dockerfile`s), the all-in-one
`deploy/prod/Dockerfile` and `compose.yml`'s builds, also read by
`apps/desktop/scripts/build-bridge-sidecar.mjs`: the source commit stamped into
`internal/buildinfo` (`-ldflags -X`, with the build time) and into the web
bundle (`build.json`). The name is the build platform's predefined commit
variable, which it injects as a build arg on every image build, so platform
builds are stamped with no pipeline change. `.git` is not in the image build
context, so a manual build passes it:
`docker build --build-arg CI_COMMIT_SHA=$(git rev-parse HEAD) …` or
`CI_COMMIT_SHA=$(git rev-parse HEAD) docker compose build`. Without it the
commit reads `unknown`, which the deployment view reports as unverifiable (a
warning), not as skew. Local builds (desktop sidecar, web dev) fall back to the
checkout's `git rev-parse HEAD`.
Every internal listener serves `GET /version` (`{service, commit, builtAt,
instance}`); the gateway's public listener does not.

## Attachments and artifact blobs (realtime)

Default is a local disk directory. For more than one host, use S3.

| Variable | Default | Meaning |
| --- | --- | --- |
| `BNBLOC_ATTACHMENTS_DIR` | `/var/lib/bnbloc/attachments` (image) | Local blob directory. The service images create it owned by the nonroot user, so a named volume mounted there is writable. Max attachment 25 MiB; staged sidecar artifacts up to 256 MiB; room artifact files up to `BNBLOC_ARTIFACT_MAX_BYTES`. |
| `BNBLOC_ATTACHMENTS_S3_BUCKET` | — | S3 bucket. Setting it switches from disk to S3. |
| `BNBLOC_ATTACHMENTS_S3_REGION` | — | S3 region (falls back to `AWS_REGION` / `AWS_DEFAULT_REGION`). |
| `BNBLOC_ATTACHMENTS_S3_ENDPOINT` | — | S3-compatible endpoint (empty = AWS). |
| `BNBLOC_ATTACHMENTS_S3_ACCESS_KEY_ID` / `BNBLOC_ATTACHMENTS_S3_SECRET_ACCESS_KEY` / `BNBLOC_ATTACHMENTS_S3_SESSION_TOKEN` | — | Static credentials (fall back to `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` / `AWS_SESSION_TOKEN`, then the AWS default chain, e.g. IRSA). Both key halves or neither. |
| `BNBLOC_ATTACHMENTS_S3_KEY_PREFIX` | — | Object-key prefix under the bucket (e.g. `bnbloc/`). |
| `BNBLOC_ATTACHMENTS_S3_PATH_STYLE` | `false` | Path-style addressing (for S3-compatible stores). A switch: `1/true/yes/on` or `0/false/no/off`; any other value is a startup error. |
| `BNBLOC_ATTACHMENTS_S3_DIRECT_DOWNLOADS` | `false` | S3 only: downloads that opt in (`?download=1` on a deliverable or public share, `?direct=1` on `/v1/artifacts/{key}`) answer 302 to a presigned S3 URL valid 5 minutes instead of streaming through bnbloc. Clients must reach the bucket. A disk store ignores it (logged). A switch like `_PATH_STYLE`; any other value is a startup error. |
| `BNBLOC_ATTACHMENTS_S3_PUBLIC_ENDPOINT` | — | Base URL presigned URLs are issued for, when `BNBLOC_ATTACHMENTS_S3_ENDPOINT` is a name only the services resolve (e.g. `http://minio:9000` inside compose, `https://files.example.com` outside). Same path-style / virtual-host addressing. Empty = the endpoint. |

### Artifact files (room)

Room artifacts are text (inline) or files: a blob version's bytes live in
realtime's blob store above (realtime is the only blob owner; the room service
writes and reads them over the signed internal route).

| Variable | Default | Meaning |
| --- | --- | --- |
| `BNBLOC_SECRET_STORE_KEY` | — (app data off) | The gateway's secret store key (one value per deployment, every room replica). The room service wraps each interactive app's data key with it (`appdata.Cipher`; docs/prd/2026-10-12-interactive-apps.md §4.8); the database holds only wrapped keys and sealed documents. Unset = the app data routes answer 503; a malformed value refuses to start. Losing it makes every app's data unreadable; rotating it re-wraps the data keys only (`appdata.Rotate`). Secret. |
| `BNBLOC_ARTIFACT_MAX_BYTES` | `104857600` (100 MiB) | Max bytes of one blob artifact version (room). Must be 1..268435456 (realtime's 256 MiB blob cap); anything else is a startup error. Publishers (the bridge) read the effective value from the artifact list's `maxBytes`. Raise the edge proxy's body limit for `POST /v1/rooms/*/artifacts/*/versions` (Caddyfile, ingress `proxy-body-size`) with it. |

## Member notifications (realtime, shareagent)

Notifications are delivered to installed notifier components (super-admin
installs, rollout and tenant scope like every component; each component's
signing secret lives in platform settings, sealed with
`BNBLOC_SECRET_STORE_KEY`). A member's own delivery (a
personal bot) is a desktop plugin whose settings stay on the member's
machine; the server stores no member notifier settings. Contract:
[notifications.md](./notifications.md).

| Variable | Scope | Default | Meaning |
| --- | --- | --- | --- |
| `BNBLOC_PUBLIC_URL` | gateway, realtime, shareagent, activity | — (`http://localhost:7090` in the dev compose) | Public web origin, e.g. `https://bnbloc.example.com`. Notification payloads deep-link to `<origin>/#/room/<roomId>?seq=<n>[&channel=<id>]` (the message) or `<origin>/#/room/<roomId>`. The gateway builds every link and artifact URL it hands out (content notifications, published package artifacts, a previewer's CSP) on it. Unset = notifications carry no `url`, and the gateway uses the request's host with the scheme a trusted proxy forwarded (`BNBLOC_TRUSTED_PROXIES`). |
| `BNBLOC_WEB_PUSH_VAPID_PUBLIC_KEY` | realtime | — (channel off) | Browser push notifications: the VAPID public key (base64url P-256 point), generate the pair with `bnbloc keys gen-vapid`. Served to browsers by `GET /v1/push/vapid-public-key`. Neither key set = the channel is off (the route answers 404, the web hides the toggle); one without the other or a mismatched pair is fatal at startup. Not required in production. |
| `BNBLOC_WEB_PUSH_VAPID_PRIVATE_KEY` | realtime | — | The matching VAPID private key (base64url P-256 scalar). Secret. Rotating the pair invalidates every browser subscription (members turn the toggle on again). |
| `BNBLOC_WEB_PUSH_VAPID_SUBJECT` | realtime | an https `BNBLOC_PUBLIC_URL` | VAPID contact, `mailto:<address>` or `https://<origin>`, sent to push services. Required when the keys are set and `BNBLOC_PUBLIC_URL` is not https. |
| `BNBLOC_SECRET_STORE_KEY` | realtime | — | The gateway's value: realtime opens each notifier's signing secret with it to sign deliveries. Unset = a notifier whose secret is sealed (installed or rotated since secrets are sealed) is not delivered to (the outbox retries, logged); a secret stored in clear before then still works. Malformed refuses to start. Secret. |

## Message search (realtime)

Global search (`GET /v1/search`) uses MySQL FULLTEXT (ngram) unless an
OpenSearch cluster is configured. Either way results pass the same
membership and people-chat filters: OpenSearch returns candidates and
realtime re-checks each one in MySQL. See
[production-deployment.md](./production-deployment.md#search-backend).

| Variable | Default | Meaning |
| --- | --- | --- |
| `BNBLOC_SEARCH_OPENSEARCH_URL` | — (MySQL FULLTEXT) | OpenSearch base URL (`http` or `https`, optionally with a path prefix; no credentials, query or fragment). Setting it switches message search to the index `<prefix>-room-messages-v<N>`. At startup realtime checks the cluster answers, accepts the credentials, and that the index exists with the expected mapping or can be created, and waits until it is allocated. Under `BNBLOC_ENV=production` it retries for a minute and then refuses to start; in development it logs a warning and retries on every index call. A malformed value is fatal in both modes. Trusts the system CA store (`SSL_CERT_FILE` / `SSL_CERT_DIR` add a private CA). |
| `BNBLOC_SEARCH_OPENSEARCH_USERNAME` | — | HTTP basic auth user. Both this and the password, or neither. |
| `BNBLOC_SEARCH_OPENSEARCH_PASSWORD` | — | HTTP basic auth password. Secret. |
| `BNBLOC_SEARCH_OPENSEARCH_INDEX_PREFIX` | `bnbloc` | Index name prefix: lowercase letters, digits, `.`, `_`, `-`, starting with a letter or digit. Use one prefix per deployment when deployments share a cluster. |

## Activity

| Variable | Default | Meaning |
| --- | --- | --- |
| `BNBLOC_ACTIVITY_ALLOW_SERVE_ALL` | off (`true` in the dev compose) | Dev-only: accept job claims from a single-host serve-all bridge (`BNBLOC_BRIDGE_SERVE_ALL_OWNERS`, the desktop's "Single-host mode") that drives every member's agent. Fatal when on (or malformed) and `BNBLOC_ENV=production`; an explicit off (`false`, `0`, `no`, `off`) is the same as unset. The production overlay pins it empty. |

## Agent cores (gateway, bridge)

No core is compiled in or pre-wired: a core exists only after a super-admin
installs a signed package (Platform console → Components, or
`bnbloc cores install`), which persists to platform settings and rehydrates at
boot. The variables below are operator overrides, defaulting to nothing.
A manifest core's discovery may additionally read variables the manifest
itself names (`discover.listener.pathEnv` to override the listener-descriptor
path, `discover.hostRewrite.env` to override the loopback host rewrite —
set-to-empty disables it); those names belong to the manifest, not to bnbloc.

| Variable | Scope | Meaning |
| --- | --- | --- |
| `BNBLOC_CORES` | gateway, bridge | Comma-separated cores to activate explicitly (installs need no entry). Unknown names are skipped with a warning. |
| `BNBLOC_CORE_<NAME>_URL` / `BNBLOC_CORE_<NAME>_TOKEN` | gateway, bridge | Explicit endpoint and token for core `<NAME>` (uppercased). |
| `BNBLOC_CORE_MANIFESTS_DIR` | bridge | Transient manifest directory written only by the desktop's sidecar manager (gateway-published manifests for its bridge); the DB remains the source of truth. Not an install path. |
| `BNBLOC_DEV_DISCOVERY` | gateway | Dev-only: enables `/v1/local-agent/discover` and a caller-supplied local-agent URL/token on `/v1/local-agent/run`. Fatal at startup when on (or malformed) and `BNBLOC_ENV=production`; an explicit off is the same as unset. |

## Components and plugin packages (gateway, realtime)

Typed packages (auth providers, cores, plugin sidecars, MCP tools) are
installed by a super-admin through `POST /v1/platform/components` (signed
package body) or the CLI (`bnbloc cores install`, `bnbloc cores
publish-sidecar`, `bnbloc cores publish-mcp`). Platform and organization
component settings need `BNBLOC_SECRET_STORE_KEY` on the gateway (see
Gateway). Production
requires a signature that verifies against a trusted publisher key
(`POST /v1/platform/component-keys`).

| Variable | Scope | Default | Meaning |
| --- | --- | --- | --- |
| `BNBLOC_EXT_ALLOW_UNSIGNED` | gateway, realtime | unset (signatures required) | Dev-only: accept unsigned packages so the console upload flow and `bnbloc cores install --unsigned` work out of the box. Realtime reads it because plugin packages rehydrate there. The dev compose defaults it on; the production overlay pins it empty. |

### How component distribution behaves (no env needed)

These are code behaviors, not variables — recorded here because operators ask:

- **Rollout gate.** Every component record carries a rollout policy (`auto`,
  `optin`, `off`, set at publish or via
  `PUT /v1/platform/components/{name}/rollout`). `off` hides it from all
  member surfaces; `optin` shows it only to members who installed it
  (`POST/DELETE /v1/components/{name}/install`); `auto` distributes to
  everyone. Opted-in components are activated at gateway boot and filtered per
  member; `/v1/cores`, `/v1/components`, and `/v1/cores/{name}/artifact` all
  enforce the same gate.
- **Tenant scope is explicit.** `PUT /v1/platform/components/{name}/tenants`
  with an empty list is rejected (400); clear the scope with
  `{"allTenants": true}`.
- **Kind disambiguation.** Component routes accept `?kind=`; a name shared by
  two kinds without `?kind=` is a 409.
- **Artifact provenance and entitlement.** Sidecar binaries are staged through
  the super-admin `POST /v1/platform/artifacts` (streamed to realtime's blob
  store, 256 MiB cap) before the package is assembled, so the signed envelope
  pins hashes the gateway produced. `GET /v1/artifacts/{key}` is entitlement-
  gated: super-admins may fetch any key; members only the artifacts of
  components visible to them and published desktop releases. The desktop
  refuses to run a binary whose sha256 mismatches the signed package.
- **Plugin sidecars.** A plugin package (`bnbloc.plugin.package/v1`) only
  subscribes to code-owned hooks (`chat_message.v1`, `agent_messages.v1`,
  and `notification.v1` — one member's notification, routed to their desktop
  instead of the server notifiers).
  Realtime decides which member sees which event and audits the outcome, but
  never dials a sidecar: each member's desktop holds
  `GET /v1/my/plugin-events` (SSE through the gateway; `?plugins=1` while it
  runs a plugin sidecar, the only streams deliveries go to), posts every delivered
  envelope to its own loopback `POST /events`, and acknowledges it with
  `POST /v1/my/plugin-events/{id}/ack`. Delivery is per member (every member
  of the room with the plugin and a connected desktop gets the event) and
  best-effort (an offline desktop misses events). The same stream carries
  `components_changed` hints: after a component install, uninstall, rollout
  or tenant change, or a member's opt-in, the gateway signals realtime
  (`POST /internal/v1/components/changed`), every replica pushes the hint to
  the member streams it holds, and the desktop refreshes its component
  catalog at once instead of at its 60 s poll. No variable configures it.
  The server never launches plugin processes — supervision is the desktop
  sidecar manager's. The desktop launches a plugin with
  `BNBLOC_PLUGIN_HOST_URL`/`BNBLOC_PLUGIN_HOST_TOKEN` (its loopback host API,
  when the package declares `permissions.host`),
  `BNBLOC_COMPONENT_SETTINGS_FILE` (every value of the settings it declares:
  platform and organization values from the gateway, the member's from
  this computer) and each setting's `deliver.env`; these are read by the
  plugin, not by any bnbloc service
  ([notifications.md](./notifications.md#launch-environment)).
- **Auth-provider sidecars.** An auth-provider package
  (`bnbloc.auth-provider.package/v1`) exposes
  `bnbloc.auth-provider.contract/v1` on loopback; the gateway proxies
  `/api/auth/*` to it and issues the bnbloc session after the provider returns
  a verified identity (`internal/authprovider.Assertion`: `subject`, `email`,
  `relay`, and optional `groups`, `department`, `displayName`). The optional
  directory attributes feed organization sync, configured in the Platform
  console, not by a variable ([organization-sync.md](./organization-sync.md)).
- **Notifier endpoints.** A notifier package (`bnbloc.notifier.package/v1`,
  runtime `endpoint`) names an externally supervised service at
  `endpoint.baseUrl` (a path prefix is allowed) that answers
  `GET <baseUrl>/discover` with `bnbloc.notifier.contract/v1`; the gateway
  probes it at install, realtime activates it on its periodic reconcile and
  posts signed notifications to `<baseUrl>/v1/notifications`. Its signing
  secret is generated at install and shown once. See
  [notifications.md](./notifications.md).
- **Sandbox runtimes.** A sandbox package (`bnbloc.sandbox.package/v1`,
  contract `bnbloc.sandbox.contract/v1`) is a Sandbox Protocol v1
  implementation ([sandbox-protocol.md](./architecture/sandbox-protocol.md)):
  runtime `sidecar` (per-platform artifacts, loopback `sidecar.localUrl`, and a
  required `sidecar.tokenEnv`) or runtime `endpoint` (`endpoint.baseUrl`, host
  in `permissions.network`). Nothing runs in the gateway; it keeps the record
  and lists installed sandboxes in the member `GET /v1/components` (rollout +
  tenant gated). The package-declared launch variables below are read by the
  sidecar, not by any bnbloc service; desktop supervision of sandbox sidecars
  lands in the next phase.
- **Sidecar launch block (any sidecar kind).** Besides `sidecar.listenEnv`
  (listen address) and `sidecar.supervisedEnv` (`1` = exit when stdin closes),
  a package may declare `sidecar.tokenEnv` (the desktop sets it to a fresh
  random bearer per launch; the sidecar must require it), `sidecar.dataDirEnv`
  (a private per-component data directory), and `sidecar.files`
  (`[{name, env, executable?}]`: extra per-platform artifacts such as a VMM
  helper or firmware library, published as `artifacts["<platform>/<name>"]`,
  sha256-verified like the executable, path passed in `env`). Every
  `<platform>/<name>` key must be a declared file of a platform that has a main
  artifact, and env names must be unique upper-case identifiers.
  **Where a sidecar listens.** A **core** is one daemon per computer, on the
  package's fixed `sidecar.localUrl`, shared by every signed-in account whose
  catalog pins that build (another build of a running core waits until it
  stops; the account is told so). A **plugin** and a **sandbox** run once per
  account on a loopback port the desktop picks and hands to the process in
  `sidecar.listenEnv`, which a plugin must therefore declare (its `localUrl`
  is only the declared shape, never an address it may bind; the desktop
  reaches the plugin at the address it handed out). `docs/prd/2026-10-22-
  multi-account.md` §2.4.

## Relay

The relay carries the desktop bridge's live file-sync byte stream as a
WebSocket. Its endpoints (`/relay/*`) are unauthenticated at the transport;
authorization is the sync-layer HMAC (`BNBLOC_SYNC_SECRET`, bridge). That token
rides the stream's cleartext HELLO, so the relay's public entry **must be TLS**.
Pairing state is in process memory: one replica pairs everything itself; more
replicas need `BNBLOC_RELAY_PEERS`/`_SELF` (Scale-out peer sets above) so both
halves of a session meet on the session key's owner.

| Variable | Default | Meaning |
| --- | --- | --- |

## Bridge (member daemon / desktop sidecar)

The desktop app sets these for its embedded bridge from its settings; a
standalone bridge (compose `bridge` profile) reads them from the environment.

| Variable | Default | Meaning |
| --- | --- | --- |
| `BNBLOC_GATEWAY_BASE_URL` | — (required) | Gateway the bridge talks to. |
| `BNBLOC_BRIDGE_OWNER` | — (required) | The member this bridge acts for. |
| `BNBLOC_BRIDGE_TENANT` | — | Tenant to sign in to (multi-tenant members). |
| `BNBLOC_GATEWAY_TOKEN` | — | Static gateway bearer. Empty (normal) = the bridge mints the owner's session via login; set it when the gateway's IdP rejects that login. |
| `BNBLOC_GATEWAY_REFRESH_TOKEN` | — | The owner's refresh token (set by the desktop after sign-in, in memory only). The bridge forks it once into its own refresh family (`POST /v1/auth/refresh/fork`) and from then on rotates only that family via `POST /v1/auth/refresh` — it never rotates the handed-over token, which the member's client keeps using (a second rotator would trip reuse detection and revoke the sign-in). A graceful stop revokes the bridge's own family (`POST /v1/auth/logout`). Required on SSO-only gateways (direct login disabled). Empty = dev login fallback (development gateways only). |
| `BNBLOC_BRIDGE_ROOMS` | all the owner's rooms | Comma-separated room ids to watch. |
| `BNBLOC_BRIDGE_ID` | derived from owner+tenant | Stable durable-job worker identity. |
| `BNBLOC_BRIDGE_APP_INSTANCE_ID` / `BNBLOC_BRIDGE_VERSION` | — | Heartbeat metadata (set by the desktop). |
| `BNBLOC_BRIDGE_DISABLED_PROVIDERS` | — | Comma-separated `core/provider` keys the member turned off. |
| `BNBLOC_BRIDGE_SERVE_ALL_OWNERS` | off | Single-host testing only: drive every member's agent from this one bridge (needs `BNBLOC_ACTIVITY_ALLOW_SERVE_ALL` on activity). |
| `BNBLOC_BRIDGE_SUPERVISED` | off | Set by the desktop: exit when stdin closes (parent died). |
| `BNBLOC_BRIDGE_CONTEXT_WINDOW_CHARS` / `BNBLOC_BRIDGE_CONTEXT_RECENT_ROWS` | built-in | Per-drive room-context tuning floors (0 = default). |
| `BNBLOC_BRIDGE_STATE_DIR` | — (memory only) | Directory the bridge keeps its state in across restarts: `workspaces.json`, which workspace each agent of a room uses (owner-only). Set by the desktop to the member's own directory (`<member dir>/bridge`). Empty: the bindings live in memory and a restart starts each agent in a new workspace. |
| `BNBLOC_BRIDGE_MAX_TURN` | `0` (none) | An optional bound on one agent turn (a Go duration: `90m`, `8h`). Unset, a turn runs as long as its agent works: a turn ends when the core reports it neither busy nor producing anything for a sliding 2-minute window, or when a member stops it. When set, the bridge sends the deadline to the core with the turn (`deadlineMs`), and a turn still running then fails as having run out of time. |
| `BNBLOC_RELAY_BASE_URL` | — | Relay base for live file sync, e.g. `https://bnbloc.example.com` (the edge routes `/relay/*`). Empty disables file sync; chat/agents are unaffected. |
| `BNBLOC_BRIDGE_SYNC_ENGINE` | `gosync` | File-sync engine for live-sync rooms. |
| `BNBLOC_SYNC_SECRET` | — | Deployment-wide secret; the per-room sync token is `HMAC(secret, roomId)`. **Required for file sync in any shared deployment** — empty yields an empty token (dev only). All members' bridges must share it. |
| `BNBLOC_CORES`, `BNBLOC_CORE_*` | — | See Agent cores. |
| `BNBLOC_SANDBOX_MODE` | `borrowed` | Set by the desktop from the sandbox component's `mode` setting (Components → the sandbox → Settings): `off` \| `borrowed` \| `all` (which of the member's runs execute their commands in the local VM; with the tenant's `requireSandboxForBorrow` on, borrowed drives that cannot run sandboxed are refused). Always set by the desktop; any other value is a fatal startup error. |
| `BNBLOC_SANDBOX_URL` / `BNBLOC_SANDBOX_TOKEN` | — | The local sandbox sidecar (Sandbox Protocol v1) and its per-launch admin bearer. Set by the desktop only while the sandbox is ready and the mode is not `off`, in memory only; the bearer never leaves the bridge. |
| `BNBLOC_SANDBOX_MACHINE` | — | The shared machine the desktop prepared (`bnbloc`); the bridge never creates or deletes machines. Set with `BNBLOC_SANDBOX_URL`. |
| `BNBLOC_SANDBOX_OWN_MACHINE` | — | The member's own machine (`bnbloc-own`), whose egress opens the private hosts open to them (the sandbox component's settings `orgHosts` and `privateHosts`). Set by the desktop with `BNBLOC_SANDBOX_URL` only while it runs with those hosts; every drive on the owner's own behalf (no one else's script-bearing content) then runs there, and no borrowed or platform drive ever does (`docs/prd/2026-10-21-sandbox-private-hosts.md`). |
| `BNBLOC_SANDBOX_VMEXEC` | — | Absolute path of the `bnbloc-vmexec` helper from the sandbox package. Set with `BNBLOC_SANDBOX_URL`. |
| `BNBLOC_SANDBOX_ENV` | — | Set by the desktop from the sandbox component's `forwardEnv` setting (Components → the sandbox → Settings), with `BNBLOC_SANDBOX_URL`: comma-separated environment variable names or `PREFIX*` patterns (never values) that the sandboxed runs of the member's OWN agents pass into the VM beyond the default (terminal, locale, git identity). Borrowed and platform runs never get them. An entry `pkgmeta.ValidForwardEnv` refuses (`BNBLOC_*`, `HOME`, `PATH`, …) is ignored with a log line. |
| `BNBLOC_SANDBOX_MOUNT_ROOTS` | the member's home folder | Comma, space or newline separated absolute guest folders a component may mount read-only copies under (`docs/architecture/sandbox-mounts.md`). A relative, unclean or `/` entry is ignored with a log line; a list replaces the default. Components' descriptor files are read from `<BNBLOC_BRIDGE_STATE_DIR>/mounts/<component>/mounts.json` before every sandboxed turn. |
| `BNBLOC_SANDBOX_WORKSPACES_ROOT` | — | The host directory shared into the machine at the identical guest path (`<home>/bnbloc-workspaces`); sandboxed room workspaces live under it. Set with `BNBLOC_SANDBOX_URL`. |
| `BNBLOC_MCP_TOOLS_DIR` | — | Set by the desktop: the directory it materializes stdio MCP tool binaries into (sha256-verified, one per installed tool for this platform), with `index.json` listing `{name, sha256, path}`. The bridge attaches a stdio tool only from an entry carrying the gateway's pin for this platform. Unset (a standalone bridge) = stdio MCP tools show as missing. |
| `BNBLOC_COMPONENT_SETTINGS_DIR` | — | Set by the desktop: the member's own component settings (`mcp@<tool>.json`, 0600, entered on the Components page, never sent to the gateway). Read only for drives the member asked for themselves. Unset = a tool with a required member setting shows as missing. The bridge writes each stdio tool's settings file (`BNBLOC_COMPONENT_SETTINGS_FILE`, 0600) under `BNBLOC_BRIDGE_STATE_DIR/component-settings`, else a private temporary directory. |
| `BNBLOC_MCP_SIGN_INS_DIR` | — | Set by the desktop: the member's MCP OAuth sign-ins (`<tool>.json`, 0600, signed in from the Components page). Read only for drives the member asked for themselves. Unset = oauth tools show as missing. |

### Room tools stdio shim (`bnbloc-bridge roomtools-stdio`)

No operator sets these. The bridge attaches its loopback room tools MCP
server to every agent session it creates (docs/architecture/room-tools.md);
an agent without http MCP launches the stdio shim, and the bridge puts the
session's bearer in the shim's environment (never in argv).

| Variable | Default | Meaning |
| --- | --- | --- |
| `BNBLOC_ROOMTOOLS_TOKEN` | — (required by the shim) | The session's room tools bearer, set by the bridge in the MCP server's `env`. The endpoint is the `--url` argument. |

## Sandbox exec helper (`cmd/bnbloc-vmexec`)

Written by a `sandboxExec` core into the agent CLI's environment from the
bridge's `execution` descriptor (`core/protocol.VMExecEnv`; parsed by
`SandboxFromEnv`). Per session, never configured by hand.

| Variable | Meaning |
|---|---|
| `BNBLOC_VMEXEC_URL` / `BNBLOC_VMEXEC_TOKEN` | Sandbox Protocol endpoint and the short-lived **space-scoped exec token** (never the admin bearer). |
| `BNBLOC_VMEXEC_MACHINE` / `BNBLOC_VMEXEC_SPACE` | Machine id/name and space the commands run in. |
| `BNBLOC_VMEXEC_MOUNTS` | JSON `[{"host":…,"guest":…}]`: host→guest path mapping; a cwd outside every mount is refused (exit 126). |
| `BNBLOC_VMEXEC_PASSTHROUGH` | Comma-separated command names allowed to run on the host instead. |
| `BNBLOC_VMEXEC_PATH` / `BNBLOC_VMEXEC_WORKDIR` | The helper's own path (what rewritten hook commands call) and the session workdir. |
| `BNBLOC_CONFINE_WORKSPACE` | The workspace `confine` grants read+write. |
| `BNBLOC_CONFINE_ALLOW_READ` / `_ALLOW_WRITE` / `_DENY_WRITE` | JSON arrays of absolute host paths; deny-write wins over every allow. |

## CLI (`cmd/bnbloc`)

| Variable | Meaning |
| --- | --- |
| `BNBLOC_GATEWAY` | Default `--gateway` URL. |
| `BNBLOC_TOKEN` | Default `--token` (a super-admin session token for install/publish commands). |
| `BNBLOC_API_TOKEN` | A super-admin-issued API token (`bnbloc api-tokens create`), used when no `--token`/`BNBLOC_TOKEN` is given: exchanged once per command for a 1-hour session (`POST /v1/auth/api-token`). The credential for unattended runs such as the nightly smoke workflow (see [ci.md](./ci.md#nightly-smoke)). |

The CLI image (`SERVICE=cli`, `deploy/prod/cli/Dockerfile`) reads the same
variables. The scheduled nightly smoke wires them per deploy surface (see
[production-deployment.md](./production-deployment.md#nightly-smoke)): the
Helm CronJob sets `BNBLOC_GATEWAY` (default `http://bnbloc-gateway:8080`)
and `BNBLOC_API_TOKEN` (from `smoke.apiTokenSecretRef`); the compose `smoke`
service (profile `smoke`, `deploy/compose.prod.yml`) takes these
`.env.production` inputs:

| `.env.production` input | Becomes |
| --- | --- |
| `BNBLOC_SMOKE_API_TOKEN` | `BNBLOC_API_TOKEN` (required) |
| `BNBLOC_SMOKE_GATEWAY` | `BNBLOC_GATEWAY`, default `http://gateway:8080` |
| `BNBLOC_SMOKE_AGENT` | `--agent` (empty = message path only) |
| `BNBLOC_SMOKE_ALERT_TO` | `--alert-to` (empty = every `BNBLOC_SUPER_ADMINS` entry) |

## Build-time and test-only

| Variable | Where | Meaning |
| --- | --- | --- |
| `BNBLOC_DEFAULT_GATEWAY` | desktop build (`option_env!`) | Gateway URL compiled into the desktop app's updater; see [the release runbook](./desktop-update-release-runbook.md). |
| `BNBLOC_TEST_OPENSEARCH_URL` | `go test` | Runs the OpenSearch integration tests (`internal/search`) and the search parity test (`services/realtime`) against a real cluster, e.g. `opensearchproject/opensearch:2` single-node with the security plugin disabled; without it the parity test uses an in-process fake. Each run creates and deletes its own `bnbloc-test-*` / `bnbloc-itest-*` indexes. |
| `BNBLOC_TEST_MYSQL_DSN` | `go test` | Enables the MySQL integration tests (skipped otherwise); CI sets it against a MySQL service container. The tests run in throwaway `<database>_t*` databases they create and drop on that server (`internal/testmysql`), so the user needs CREATE and DROP DATABASE. |

## Minimum production set

```bash
BNBLOC_MYSQL_DSN=bnbloc:CHANGE@tcp(db.internal:3306)/bnbloc?parseTime=true&tls=true
BNBLOC_INTERNAL_KEY=<random>
BNBLOC_SESSION_SIGNING_KEY=<random>
BNBLOC_OIDC_ISSUER_URL=https://idp.corp.example
BNBLOC_OIDC_CLIENT_ID=bnbloc
BNBLOC_SUPER_ADMINS=admin@corp.example
# BNBLOC_ENV=production is set by the overlay / chart / all-in-one image.
```

`.env.production.example` at the repo root is the annotated template.
