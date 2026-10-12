# Platform hardening and team features (2026-10-08 batch)

Status: landed (H1–H7, F1–F7; F8 Windows update publishing deferred until a Windows runner exists)

Constraint for every item: additive only. Existing routes, payloads, schema columns and
UI flows keep their behaviour; new capability arrives as new routes, new tables (with
migrations in both schema files + migrations.go), new UI entries. Each wave lands only
after `scripts/ci.sh all` and the post-deploy smoke (H1) pass.

## Wave 1 — hardening

| ID | Item | Acceptance |
|---|---|---|
| H1 | Post-deploy smoke: `bnbloc smoke` | Signs in with the stored CLI credential; creates a throwaway room, sends a chat, reads it back via history and SSE, optionally waits for an agent reply when an own agent is online, deletes the room; prints PASS/FAIL per step; non-zero exit on failure. |
| H2 | CI on the internal GitHub Enterprise | Workflow runs `scripts/ci.sh` stages on runners available there (self-hosted labels configurable); documented. |
| H3 | Multi-replica-safe auth state | One-time desktop SSO codes and CLI grants stored in MySQL (TTL, single use) instead of process memory; component installs reconcile periodically from platform settings on every replica. |
| H4 | Revocable refresh tokens | Refresh tokens are tracked server-side (token family, rotation, reuse detection); logout revokes; admins can revoke a member's sessions; tenant suspension / removal revokes. Existing 1h session bearer unchanged. |
| H5 | Private `/metrics` | Metrics no longer served on the public listener (separate internal listener or signed/internal-only); public `/metrics` returns 404. |
| H6 | Stale room 404s | Clients drop rooms that return 404 (deleted / no longer visible) from quick access and caches instead of re-requesting them. |
| H7 | Sidecar model lookup | The sidecar's per-turn model-index lookup reuses the cached catalog snapshot (no 15 s CLI call per turn). |

## Wave 2 — team features

| ID | Item | Acceptance |
|---|---|---|
| F1 | Team agent dashboard | Org admins (and super-admins per tenant) see every member's agents: online state, current/last job, turns and cost today/7d, click-through to the room/job. Members see their own. Read-only aggregation over existing bridge/job/activity data. |
| F2 | Prompt library (superseded by prompt packs, see F2 below) | Any message (human prompt or agent reply) can be saved to a tenant library with title/tags; library page with search/filter; "Use" inserts the prompt into the composer of a chosen room. Visibility: private / tenant. |
| F3 | Borrow billing | Per owner/borrower/agent usage and cost reports (day/month), CSV export; quota requests: a borrower asks for more runs/cost, the owner approves/declines in-app. |
| F4 | Handoff package | Export a room's (or channel's) conversation, artifacts and agent session metadata as a package (JSON + files zip); import into another room or transfer room ownership with the package as history. |
| F5 | Notifications | Per-member notification settings (mentions, agent finished/failed, borrow requests); signed delivery to installed notifier components (organization default + optional personal bot per member) in the public code; a private notifier turns them into enterprise-messenger bot messages. |
| F6 | Chat as a task | Mark a message as a task; room header shows open/done tasks; toggle done; tasks are messages, not a separate board. |
| F7 | Global search | Search messages and artifacts across the rooms the member can see (tenant-scoped, membership-checked), with jump-to-message. |
| F8 | Desktop builds | Intel macOS (universal) build; Windows build documented for a Windows runner. |

## Implementation notes

### H1
`bnbloc smoke` (`cmd/bnbloc/smoke.go`, tests `smoke_test.go` against an httptest
fake gateway with a real SSE stream). Credential is the CLI's usual chain
(`--token`/`BNBLOC_TOKEN`, else the stored refresh token). Steps, one
`PASS <step> (<ms>)` / `FAIL <step>: <reason>` / `SKIP` line each: `auth`
(subject from the refreshed grant or the static session's payload, proven by
`GET /v1/users/{subject}/rooms`), `create room` (private
`smoke-<utc>-<rand>`), `open stream` (`/events?allChannels=1&presence=0`,
opened before sending), `chat echo` (`clientMsgId` and sequence must match on
the stream), `history`, `agent reply` (`--agent` own agent by target id /
core/provider / provider via `GET /v1/my/agents`, else owner/provider; waits
`--timeout`, default 120s, for an assistant `agent_messages`; a job-failure
notice fails; no flag → `SKIP agent reply (pass --agent)`), `delete room`
(deferred, runs after any failure or Ctrl-C; `--keep` skips; a failed cleanup
fails the run). The CLI's `client.stream` now takes a context (used for the
cancellable SSE stream); no new request helpers. Documented in `docs/cli.md`
and in the deployment checklist (`docs/production-deployment.md`).

### H2
`.github/workflows/ci.yml` reads runner labels from Actions variables
(`BNBLOC_CI_LINUX_RUNS_ON`, `BNBLOC_CI_MACOS_RUNS_ON`, JSON label or label
array via `fromJSON`) and the MySQL service image from
`BNBLOC_CI_MYSQL_IMAGE`; unset variables keep `ubuntu-24.04` / `macos-14` /
`mysql:8.4`, so github.com behaviour is unchanged. Added a nightly `schedule`
and `workflow_dispatch`. `docs/ci.md` documents the GitHub Enterprise Server
setup: self-hosted runners (the `go` job needs Linux with Docker for the
service container), GitHub Connect or `actions-sync` mirrors for the
github.com actions, toolchain/registry egress or mirrors, no secrets needed,
and running `bnbloc smoke` post-deploy (not in CI). `scripts/ci.sh` stays the
single check list. actionlint passes.

### H6
A room is "gone" for a member when its detail answers 404/403 or the stream
delivers `room_deleted` / the member's own `member_removed`.
`web/src/lib/roomGone.ts` owns that rule (`isRoomGoneStatus`,
`roomAccessEnded`, `withoutRoom`) plus a session-wide
`reportRoomGone`/`onRoomGone` signal (tests: `roomGone.test.ts`). RoomView
handles a gone detail or revoking stream event once (report, one toast,
navigate away), so the `#/room/<id>` route and its stream stop; Shell prunes
the room from quick access and the unread poll set (a fresh server listing
stays authoritative); the standalone preview stops its stream and shows a gone
state. Bridge: a 404/403 on the room stream or the sync room lookup drops the
room (stream + sync cancelled, sessions forgotten) with a one-minute cooldown
that reconcile honours; a failed discovery keeps the current set instead of
tearing every stream down. The desktop shell keeps no room ids. Bug record:
`docs/bugs/2026-10-08-stale-room-404-loop.md`.

### F8
`apps/desktop/scripts/build-bridge-sidecar.mjs` resolves its target from
`--target <triple>`, then `TAURI_ENV_TARGET_TRIPLE` (set by
`cargo tauri build --target`), then the `rustc -vV` host, and writes
`src-tauri/binaries/bnbloc-bridge-<triple>[.exe]`. For
`universal-apple-darwin` it builds both mac arches and `lipo`s them into
`bnbloc-bridge-universal-apple-darwin` (Tauri needs the per-arch files while
compiling and the fat file while bundling). The logic is exported and tested
(`scripts/build-bridge-sidecar.test.mjs`, run by `scripts/ci.sh desktop`).
New `apps/desktop` scripts: `build:universal`, `build:x86_64`,
`build:windows`, `test:scripts`. `docs/desktop-update-release-runbook.md` §2a
documents rustup targets, output folders and updater keys; a universal
`bnbloc.app.tar.gz` is published with `bnbloc updates publish` once per target
at the same version (`--target darwin-aarch64`, then `--target darwin-x86_64`;
the upload is content-addressed, so both point at the same artifact). The
Windows build on a Windows runner (MSVC, WebView2, NSIS/MSI, Authenticode,
`.exe` sidecar) is documented; the CLI/web publishers do not yet accept a
Windows updater file, so `windows-x86_64` is set through the update-config API
by hand. Sidecar cross-compiles for x86_64-apple-darwin, x86_64-pc-windows-msvc
and universal were verified; no full release build was run.

### H3
SSO hand-offs (desktop one-time codes, CLI grants) live in the shared
`sso_handoff_codes` table (migration `2026_10_sso_handoff_codes`,
`internal/store/sso_handoffs.go`, memory + MySQL). The key is a
domain-separated sha256 of the code / nonce; the row holds only the verified
identity (subject, email, tenant hint) and the desktop nonce hash, never a
token: the session and its refresh family are minted when the hand-off is
redeemed (`POST /v1/auth/sso/exchange`, `POST /v1/auth/sso/claim`), after the
suspension gate runs again. Single use is a conditional
`UPDATE … SET consumed_at_ms WHERE consumed_at_ms IS NULL` (exactly one
replica wins); expired rows are pruned on insert. The in-process
`ssoCodeStore` is gone. Installed components: `extpipe.Pipeline.Reconcile`
diffs the durable records against the records this process activated
(fingerprint: backend/version/content hash/installedAt/network) and
activates/deactivates the difference; `Rehydrate` is now the first reconcile,
and Install/Uninstall/SetRollout/Reconcile are serialized per process. The
gateway boots on the same pipeline it serves with (no bootstrap service) and
runs `RunComponentReconcile` every 30 s (`ComponentReconcileInterval`,
a design constant; an idle tick is one settings read), logging changes. A
failed activation is audited once per fingerprint, not every tick.

### H4
Refresh tokens carry `fam` (family) and `jti`; `refresh_token_families`
(migration `2026_10_refresh_tokens`, `internal/store/refresh_tokens.go`)
tracks the current/previous jti, identity, last landed tenant + role, client
label and user agent, created/last-used/expires (sliding), revoked_at. A
refresh rotates the family (MySQL: `SELECT … FOR UPDATE`); the immediately
previous jti is accepted for 60 s and answered with the CURRENT jti (two tabs
renewing at once); any older jti is reuse → the family is revoked and 401.
A refresh token without a server-side family is refused. Refresh now replays
the stored identity including email. New routes: `POST /v1/auth/logout`
(`{refreshToken}`, idempotent 204), `POST /v1/auth/refresh/fork` (new family
for the same sign-in from a valid refresh token, default client `bridge`),
`GET /v1/my/sessions` (`current` marks the caller's own family; session
bearers carry `fam`), `DELETE /v1/my/sessions/{id}`, and on shareagent
`DELETE /v1/tenant/members/{userId}/sessions` (manage_org; revokes all of the
member's families). Removing a member revokes all of their families (a family
is the sign-in and can reach every tenant via switch-tenant); suspending a
tenant revokes families that landed in it except super-admins. Client labels:
`login` takes an optional `client` (default `web`), exchange = `desktop`,
claim = `cli`, fork = `bridge`. Desktop/bridge: the desktop keeps handing the
web's refresh token to the bridge; the bridge forks it once and rotates only
its own family, never re-presenting the handed token (no Rust change needed),
and revokes its family on graceful stop. Web sign-out and `bnbloc logout`
call `/v1/auth/logout` best-effort. Session bearers (1 h) are unchanged and
stay valid until expiry after a revocation. Known limit: in-memory dev stores
are per process, so shareagent-side revocation reaches the gateway only with
the shared MySQL store. Signing out of the desktop web does not revoke the
running bridge's family (as before); it can be revoked from the session list.

### H5
`internal/obs` builds listeners from a plan: `BNBLOC_METRICS_ADDR` set →
`/metrics` only on that internal listener; unset → internal services keep it
on their main (private-network) listener; `obs.PublicListener()` (the
gateway) → the main listener always answers `/metrics` 404 (in front of the
SPA catch-all) and metrics exist only on `BNBLOC_METRICS_ADDR`. compose.yml,
the prod overlay and Helm (`gateway.metricsPort`, container + Service port
`metrics`) set `:9090` for the gateway; the all-in-one entrypoint hands the
value to the gateway process only. `/healthz`, `/readyz` stay on the main
listener. routeLabel learned `refresh`, `fork`, `sessions`.

### F1
Read-only aggregation served by **activity** (it owns the bridge heartbeats,
advertised agents, job queue and reply records). Two paths, one handler:
`GET /v1/team/agents?range=today|7d[&tzOffsetMin=]` returns the caller's own
agents (scope `self`, any member); `GET /v1/tenant/team/agents` returns every
member's agents in the ctx tenant (scope `tenant`) — the gateway's existing
manage_org gate on `/v1/tenant/*` makes it org_admin/super_admin only, and a
super-admin's acting tenant is the signed ctx tenant, so activity needs no role
knowledge (gateway routes `team` and `tenant/team` to activity). `today` starts
at the viewer's local midnight (`tzOffsetMin` = `Date.getTimezoneOffset()`).
Row per (owner, core, provider): `online`/`lastSeenAtMs` (bridge_agents LEFT
JOIN bridge_heartbeats, so offline agents stay listed), `currentJob` (newest
non-terminal job), `lastJob` (newest terminal job in the range), `turns`
(replied jobs), `failedTurns` (failed/timed out), `borrowedRuns` (replied jobs
whose requester ≠ owner), and `usage` (`reportedTurns`, `inputTokens`,
`outputTokens`, `costUsd`) summed from the `metrics` agents report on reply
payloads (`agent_messages.payload_json`; only `reported` metrics count). Jobs
carry no prompt text in the reply. Fields with no source are omitted
(`costUsd` absent ≠ 0). Replies carry provider but not core, so an owner
exposing one provider under two cores gets the combined usage on both rows,
flagged `shared`. Store: `TeamAgentReader` (bridge-job store) +
`AgentUsageReader` (activity store), memory + MySQL; DTOs in
`internal/wire/team_agents.go`. Index migration `2026_10_usage_indexes`
(agent_jobs / agent_messages tenant+time). Web: **Team** nav entry
(`#/team`, all members), Agents tab with Today/7 days toggle, Team/Mine toggle
for admins, online dot, click-through to the room (no per-job view exists),
table on wide screens and cards on narrow ones, 30 s refresh while the page is
visible. Known limit: in-memory dev stores are per process, so the dashboard
needs the shared MySQL store to see bridge/job data written by other services;
reply timestamps come from the core (`createdAtUnixMs`), so a core that reports
none contributes no usage to a range.

### F3
Served by **shareagent** over its grants/audit (gateway section `borrow`;
`/v1/shared-agents/...` was not used because `shared-agents` is an id
collection in routing and metrics). Usage report:
`GET /v1/borrow/usage-report?as=owner|borrower&groupBy=day|month&from=&to=[&tzOffsetMin=][&format=csv]`
— `as=owner` = borrows of the caller's agents, `as=borrower` = the caller's
borrows, both keyed on `X-Bnbloc-User`; `GET /v1/tenant/borrow/usage-report`
is the whole tenant (manage_org). Rows `{period, owner, borrower, provider,
roomId, runs, cost?}` from allowed `turn` audit records with actor ≠ owner;
provider is the grant discriminator (`<core>:<provider>`). Cost: the audit row
gained a nullable `cost` (migration `2026_10_usage_indexes`, plus
`idx_audit_tenant_time`); `.../authorization/record` stores the reporter's
`cost` when supplied (absent = NULL, never 0). Today's bridge reports no cost,
so reports show runs only until a reporter supplies one. CSV escapes
formula-leading cells. Quota requests (table `shared_agent_quota_requests`,
migration `2026_10_borrow_quota_requests`; one open request per
(room, owner, provider, requester) via a unique `pending_key`):
`POST /v1/borrow/quota-requests`, `GET /v1/borrow/quota-requests?role=owner|requester[&status=][&roomId=]`,
`POST /v1/borrow/quota-requests/{id}/approve` (`{runs?}`) / `.../decline`.
Requester and deciding owner are always `X-Bnbloc-User`; only the request's
owner may decide (403 otherwise; the store also refuses). Creating requires a
live, run-limited authorization (409 otherwise). Approving raises the
authorization's `run_quota` by the granted runs through the new atomic
`GrantStore.RaiseRunQuota`, which never touches `runs_used`/`cost_used` — the
re-authorize upsert (`PutGrant`) is deliberately not used because it resets
usage; if the raise fails (revoked in between) the request is reopened. The
authorization read (`GET .../authorization?owner=`) additively reports
`runQuota`/`runsUsed`/`quotaExhausted` for run-limited authorizations. The
owner is notified through the F5 notification emitter when a request is
created; the web also polls. Web: the borrower's send path refuses a borrow
whose quota is exhausted (toast) and opens a **Request more** dialog; the
in-room handshake icon shows a pending-request badge and the authorization
dialog lists pending requests with approve/decline; Team → **Usage &
billing** has the month picker, day/month grouping, My agents / My borrows /
Organization (admins) views, CSV download, and both request lists. Known
limits: quota is still enforced at record time (after the bridge drives), as
before; the bridge does not pre-check `quotaExhausted`. `/v1/borrow/*` is a
tenant-level path (session tenant), so a request for a room that lives in a
member's non-primary tenant needs the member to switch to that tenant first.

### F5

Contract and operations: `docs/notifications.md`. New package
`internal/notify` (producer, delivery worker, settings handlers, signing, URL
guard); DTOs in `internal/wire/notifications.go`.

- **Preferences**: `GET/PUT /v1/my/notification-prefs`
  `{mentions, agentFinished, agentFailed, borrowRequests, quietRooms}` (all on
  by default), table `notification_prefs` keyed `(tenant_id, user_id)`.
  Served by realtime; the gateway routes it there and lets a viewer write it
  (`PermRead`: personal, not cost-bearing).
- **Tenant webhook**: `GET/PUT /v1/tenant/notification-webhook`,
  `POST /v1/tenant/notification-webhook/test` (manage_org via the existing
  `/v1/tenant/*` RBAC; routed to realtime). Stored in the generic platform
  settings store under `notify.webhook/<tenantId>` (no new table). The
  secret is write-only: generated server-side when absent or on
  `rotateSecret`, returned once in that PUT response, never in a GET.
  The test send is synchronous and bypasses the outbox.
- **Event sources** (all off the publish path): realtime's post-publish tap
  (`deliverPluginEvent`, beside plugin hook delivery) hands events to a
  bounded queue (512; a full queue drops and counts). `chat_message` →
  `mention` for `@<memberId>` tokens that name a room member (agent handles
  `@owner/provider` excluded, author excluded). `agent_job_updated`
  terminal states → `agent_finished` (replied) / `agent_failed`
  (failed, timed_out; cancels ignored) to the job's requester; a
  `borrow_not_authorized` failure additionally sends `borrow_request` to the
  owner. Borrow quota requests (F3, shareagent) call the same emitter
  (`notify.Emitter.BorrowRequested`, detached and bounded).
- **Filtering**: actor never notified, per-member prefs and quiet rooms,
  per-(member, room, type) collapse window (30 s; 5 min for borrow
  requests), and nothing is queued without an enabled tenant webhook.
- **Delivery**: table `notification_outbox` (one row per webhook event,
  `(tenant_id, id)` primary key; the id is derived from the logical event so
  re-observation is idempotent). realtime's worker claims due rows with an
  `UPDATE … ORDER BY … LIMIT` lease (claim token + pushed-out
  `next_attempt_at_ms`), posts with `X-Bnbloc-Signature: t=…,v1=…` and
  `X-Bnbloc-Event-Id`, backs off 10 s·2^n (±20 %, cap 30 min, 8 attempts),
  and records outcomes only under its own lease. Finished rows are pruned
  after 7 days. Every attempt / config change / test is audited in
  `extension_audit` (`kind=webhook`).
- **SSRF guard**: production requires public https; link-local and
  metadata addresses are always refused; private/loopback receivers (e.g. an
  adapter container on the deployment network) need
  `BNBLOC_NOTIFY_WEBHOOK_ALLOW_HOSTS` (reuses `extgov.NetGuard` host
  matching). Checked again on the resolved IP at dial time; no redirects, no
  proxy.
- **Deep links**: `BNBLOC_PUBLIC_URL` (realtime, shareagent) →
  `<origin>/#/room/<roomId>`; unset = no `url`. Wired in compose, prod
  overlay, Helm (`publicURL`, `notifications.webhookAllowHosts`), the
  all-in-one notes, `.env.production.example` and both env docs.
- **Web**: Settings view (new nav entry) with the four switches and the
  muted-room list; a bell button in the room header mutes/unmutes the room;
  Organization → Notifications holds the URL / enable switch, the one-time
  secret display, rotation and "Send test notification" (toast with the
  result).
- **Enterprise messenger**: the adapter that turns these webhooks into bot
  messages lives in the private components repository; nothing
  vendor-specific is in this repo.

Known limits: a mention's deep link opens the room, not the message (the hash
router has no message anchor); `agent_finished` carries the request text, not
the reply; collapse windows are per process (realtime is a singleton;
shareagent replicas each keep their own window for borrow requests).

#### Revision: notifier component, default + personal bot (2026-10-08)

The tenant-admin webhook above forked the component seam (an admin-entered
URL with its own SSRF guard and allowlist env). Clean cutover, no
compatibility path (`2026_10_notification_outbox` had only run on dev/test
databases, so it was changed in place):

- **Notifier kind**: `bnbloc.notifier.package/v1`, contract
  `bnbloc.notifier.contract/v1`, runtime `endpoint` only, `endpoint.baseUrl`
  (path prefix allowed) within `permissions.network`. Extreg kind `notifier`
  hosted by realtime. Install/probe/activate go through the new shared
  `internal/endpointpkg` (decode, validate, NetGuard, discover probe, guarded
  client), which the auth-provider kind now uses too. Probe:
  `GET <baseUrl>/discover`; delivery: `POST <baseUrl>/v1/notifications`.
- **NetGuard** gained the connect-time address check the webhook guard had
  (link-local / metadata / unspecified / multicast refused for every
  extension client); `BNBLOC_NOTIFY_WEBHOOK_ALLOW_HOSTS` and the URL policy
  are gone.
- **Distribution**: tenant scope and rollout like every component (off → no
  delivery; optin → only recipients with a `component_installs` row; auto →
  all). Outbox rows are per (event, notifier): key
  `(tenant_id, id, notifier)`. Realtime's component pipeline (plugins +
  notifiers) rehydrates at boot and reconciles every 30 s
  (`extpipe.Pipeline.RunReconcile`, shared with the gateway loop).
- **Signing secret**: per notifier, generated at first install, stored in
  platform settings (`notify.secret/<name>`), returned once (install
  response, `POST /v1/platform/components/{name}/secret/rotate`), deleted on
  uninstall. `POST /v1/platform/components/{name}/test` (super-admin) sends a
  test through the default delivery. CLI: `bnbloc components rotate-secret |
  test <name>`; the install path prints the secret once.
- **Default + personal bot** (superseded 2026-10-08: personal bots moved
  into the desktop client as a plugin with desktop-local settings; the
  server-side member settings below were removed — see "Revision: personal
  bots run in the desktop client" in
  `2026-10-08-batch-2-gaps-ops-features.md`): a notifier may declare `memberSettings`
  (≤ 10 fields `{key, label, help?, secret, required}`, validated in
  `core/pkgmeta`). Members manage theirs at `GET /v1/my/notifiers`,
  `PUT|DELETE /v1/my/notifiers/{name}/settings`,
  `POST /v1/my/notifiers/{name}/test` (realtime; viewer-permitted). Table
  `member_notifier_settings` (migration
  `2026_10_member_notifier_settings`); secret fields AES-256-GCM with a key
  HKDF-SHA256-derived from `BNBLOC_INTERNAL_KEY` (row identity as AAD; no key
  → 503 for secret fields; undecryptable rows read as not configured and are
  logged). Uninstall deletes a notifier's member settings; removal from a
  tenant deletes the member's settings there.
- **Payload**: `recipients: [{userId, settings?}]` — settings resolved and
  decrypted by the worker at send time, never stored in the outbox or logged.
  A 2xx may answer `{results: [{userId, ok, error?, retryable}]}`; only
  retryable recipients are re-sent (same event id, narrowed recipients).
- **Removed**: `GET/PUT /v1/tenant/notification-webhook`,
  `POST /v1/tenant/notification-webhook/test`, settings key
  `notify.webhook/<tenant>`, Organization → Notifications, the URL policy and
  its env var (compose, prod overlay, Helm `notifications.webhookAllowHosts`,
  all-in-one notes, `.env.production.example`, env docs).
- **Web**: Settings → Notifications → Delivery (per notifier: organization
  default / my own bot form, save, send test, use default; opt-in toggle for
  optin notifiers; a note when none is installed). Platform → Components:
  Notifiers filter, one-time secret reveal with copy, Rotate secret, Send
  test notification.
- **Enterprise messenger**: the private notifier is hosted inside the private
  auth-provider's process (no new deployment); its default bot is optional.

### F4

Handoff package, format `bnbloc.room-export/v1`, documented in
`docs/room-export-format.md` (DTOs: `internal/wire/handoff.go`).

- **Ownership split**: the room service orchestrates both directions (it owns
  the room, members, channels and artifacts); realtime owns the feed, its
  per-room sequence and the attachment blobs. room reaches realtime through
  the `HandoffFeed` port, implemented by `internal/realtime.HandoffClient`
  (forwards the end user's identity, svcauth-signed, so realtime applies the
  same membership checks). No new env var: room already had
  `BNBLOC_REALTIME_BASE_URL`.
- **Routes**: `GET /v1/rooms/{id}/export` (owner/admin — the room model's
  existing room-wide-action roles, since a package copies every member's
  content out of the room's access control; plain members and visitors get
  403), `POST /v1/rooms/import` (gateway: `create_room` permission and the
  room-creation rate limit), `GET /v1/rooms/{id}/handoff` (member; provenance,
  404 when not imported). Realtime gained two internal routes:
  `GET /internal/v1/rooms/{id}/feed` (forward cursor over every channel,
  soft-deleted excluded — new `ChatStore.RoomFeed`, memory + MySQL) and
  `POST /internal/v1/rooms/{id}/import` (history-only write). `routeLabel`
  learned `export`, `import`, `handoff`, `feed`.
- **Export** streams the zip (no temp file, no whole-room buffer, no total
  cap): two forward passes over the feed (messages.jsonl, then transcript.md
  bounded by the first pass's last sequence), referenced attachments streamed
  from realtime, artifacts with full version history, manifest written last.
  A mid-stream failure aborts the connection (`http.ErrAbortHandler`) instead
  of finishing an archive that lacks data. Agent session metadata comes from
  the feed (session ids, owner/provider, counts, first/last time); the
  activity service's `agent_sessions.state_json` is core-reported state and is
  deliberately not exported (agent-collaboration boundary decision).
- **Import** validates the whole archive first (format, names, hashes, every
  line), then creates a new private room owned by the importer (sole member),
  recreates channels and artifacts, re-uploads attachments through realtime's
  regular upload route (type gate, sniffing, cap, content-addressed blobs; a
  4xx is skipped and reported), and streams messages in ≤500-row / ≤8 MiB
  batches. A failure after creation deletes the room again.
- **Imported representation**: no `room_messages` schema change. Realtime
  stamps `payload.imported = {fromRoomId, fromRoomName, originalSequence,
  importedAtMs, editedAtMs?}`, which the unchanged history endpoint already
  carries; original timestamps are kept as `created_at_ms`. Imported rows are
  stored with an empty author, so nobody can edit/delete them as their own
  (ids from a file never authorize anything).
- **Never live**: imported rows go through the new `Hub.CommitHistory`, which
  shares the room lock and the durable-max-seeded counter with
  `PublishCommit` (monotonic seqs, live chat continues after them) but neither
  retains them for replay nor delivers them: no bridge drive, no agent job, no
  plugin hook. Tested by subscribing to the hub (live and replay) around an
  import.
- **Provenance**: table `room_imports` (both schema files, migration
  `2026_10_room_imports`, `RoomImportStore` memory + MySQL; removed by
  `DeleteRoom`).
- **Web**: room header package icon (owner/admin; role read from the member
  list) downloads the package with byte progress and toasts; My rooms →
  **Import handoff package** uploads and opens the new room; imported rooms
  show "Imported from <room> on <date> — invite original members?" with a
  suggestions dialog that adds members through the existing add-member call
  (a source owner is suggested as admin; ownership only moves by transfer);
  imported messages carry a subtle "imported" badge and hide edit/delete. A
  shared `lib/saveBlob.ts` replaces the copy in `releaseDownload.ts`.
- **Tests**: `services/room/handoff_test.go` (real room + realtime over HTTP:
  export → import round trip equal modulo seq/ids per channel, soft-delete
  excluded, attachments/artifacts restored, no author, nothing published;
  owner/admin gate; malformed/wrong-format/unknown-kind/out-of-order/
  oversized-line/oversized-artifact/hash-mismatch/path-traversal packages
  refused with nothing created; refused attachment skipped),
  `services/realtime/handoff_test.go` (no live/replay delivery, monotonic seq
  continuation, batch validation and caps, feed paging, membership, unsigned
  refusal), `internal/store/room_import_test.go` (memory + MySQL),
  `services/gateway/handoff_route_test.go`, `web/src/lib/handoff.test.ts`.

Known limits: packages above 256 MiB export fine but cannot be imported; the
source room's legacy empty channel (rows written before channels existed) is
imported into the new default channel; an imported attachment's uploader is
the importer (the original uploader survives only as message attribution).

### F2

**Superseded (2026-10-12):** the prompt library was removed in favor of
prompt packs, content components with variables and slash commands
(`docs/prd/2026-10-12-experts-and-teams.md` §4, step 3; reference
`docs/architecture/content-components.md`). Its table, store, routes,
moderation path and web views are gone (start empty, no conversion); "Save
to library" became "Save as prompt". The record below is history.

Prompt library, owned by **realtime** (beside the room feed it is saved from;
no new service). Table `prompts` (both schema files, migration
`2026_10_prompt_library`, `PromptStore` memory + MySQL, DTO `wire.Prompt`).

- **Routes** (gateway section `prompts` → realtime): `GET /v1/prompts?q=&tag=&mine=1`
  (own + tenant-visible, newest first; `q` is a case-insensitive substring of
  title/body — a tenant's library is small, so LIKE, no text index),
  `POST /v1/prompts` `{title, body, tags[], visibility: private|tenant,
  sourceRoomId?, sourceSeq?}` (author = `X-Bnbloc-User`, tenant = the signed
  tenant; body copied at save time; tags trimmed/lowercased/deduped, ≤12;
  a `sourceRoomId` must be a room the author belongs to),
  `PATCH|DELETE /v1/prompts/{id}` (author only: another member's tenant prompt
  is 403, their private prompt 404), `POST /v1/prompts/{id}/use` (use counter).
  Org admins remove any tenant-visible prompt via
  `DELETE /v1/tenant/prompts/{id}` — `/v1/tenant/*` keeps its gateway
  `manage_org` gate; private prompts stay invisible to admins.
- **Web**: message action "Save to library" (bookmark icon beside copy; dialog
  with title prefilled from the first line, editable body, tags, visibility);
  **Library** nav page (search, tag chips, All/Mine, cards with Use / Copy /
  Edit / Delete); "Use" picks a room (default: last opened room), counts the
  use and hands the body to that room's composer through an in-memory
  hand-off (`lib/team.ts` `composerDrafts`, consumed by
  `useInjectedDraft` once the composer has its channel; merged after any draft
  in progress, never replacing it). The Plaza (the existing shared showcase)
  shows the latest tenant prompts with Use + "Open library".
- **Tests**: `internal/store/team_test.go` (memory + MySQL contract:
  visibility, tag/text filters incl. LIKE metacharacters and CJK, author-only
  update/delete, moderator scope, use counter, tenant isolation),
  `services/realtime/team_test.go`, `services/gateway/team_route_test.go`,
  `web/src/lib/team.test.ts`.

### F6

Chat message as task, owned by **realtime**. Table `message_tasks`
(`tenant_id, room_id, seq, channel_id, status open|done, title,
message_author, created_by, created_at_ms, done_by, done_at_ms`, PK
`(room_id, seq)` like `room_messages`; migration `2026_10_message_tasks`,
`MessageTaskStore` memory + MySQL, DTO `wire.MessageTask`).

- **Routes** (room sub-resource `tasks` → realtime; membership-checked, any
  member): `GET /v1/rooms/{id}/tasks`, `PUT /v1/rooms/{id}/tasks/{seq}`
  `{status}` (first mark looks the message up — it must exist and not be
  soft-deleted — and snapshots title = first non-empty line, author, channel;
  later puts change only status/done fields), `DELETE /v1/rooms/{id}/tasks/{seq}`.
  Each change publishes the room-wide transient `task_updated`
  (`wire.TaskUpdatedPayload`); the `persist()` allowlist is unchanged, so it
  never enters history (tested).
- An agent batch's rows share the batch's room sequence (the feed shows them as
  `seq + i/1000`), so a task on one marks the batch.
- **Web**: message action "Mark as task" (beside copy; grouped rows get a
  hover cluster positioned absolutely), a Task/Done badge on task messages
  (click toggles), and a floating "N open tasks" pill at the top-right of the
  conversation that expands into the list (checkbox toggles done, title jumps
  to the message, done ones collapsed). The pill is absolutely positioned, so
  the feed layout and scroll anchoring are untouched.

### F7

Global search, owned by **realtime**: `GET /v1/search?q=&rooms=&limit=&before=`
→ `{results:[{kind: message|artifact|prompt, roomId, roomName, channelId?,
seq?, artifactId?, promptId?, title?, snippet, author, atMs}], nextBefore?}`.

- **Scope**: the rooms searched are the caller's memberships as bnbloc-room
  lists them (`GET /v1/users/{id}/rooms?detail=1`, signed, tenant-scoped;
  `membership.RoomClient.MemberRooms`); `rooms=` only narrows that set
  (a foreign room yields nothing — tested). Messages page by `before`
  (created_at_ms of the oldest hit); artifact (title) and prompt hits come
  with the first page; prompts only without a room filter. Queries shorter
  than 2 characters are a 400. Snippets are plain text; the client
  highlights.
- **Index — FULLTEXT ngram, chosen over LIKE**: `room_messages.search_text`
  (`MEDIUMTEXT`, the chat text or the joined agent reply texts, written by
  `AppendRoomMessage` and rewritten by `EditRoomMessage`) carries
  `FULLTEXT KEY ft_room_messages_search WITH PARSER ngram` (migration
  `2026_10_message_search`). The room feed is the one unbounded corpus, so a
  scan per keystroke is not acceptable, and ngram is MySQL's built-in parser
  for CJK text without word delimiters. Queries run as a quoted BOOLEAN MODE
  phrase, i.e. substring match for terms ≥ `ngram_token_size` (default 2,
  hence the 2-character minimum). The index is built with
  `innodb_ft_enable_stopword = OFF` (session, in schema.sql and the
  migration): the ngram parser drops every token *containing* a stopword and
  the default list holds "a"/"i", which would make e.g. "data" unsearchable —
  verified against MySQL 8.4 (`INNODB_FT_CONFIG use_stopword = 0` on a fresh
  schema; contract test searches "data"). Soft-deleted rows are excluded.
  Rows stored before the migration have no `search_text`; the batch-3 R7
  backfill (`bnbloc search reindex`, `POST /v1/platform/search/reindex`)
  fills them. Artifact titles use LIKE (short, bounded by the
  caller's rooms). The memory backend matches substrings case-insensitively.
- realtime now stamps the server time on a feed event whose publisher left it
  unset (activity's reply path), so every stored row has a usable
  `created_at_ms` for ordering and paging.
- **Web**: Cmd/Ctrl+K or the sidebar **Search** entry opens a dialog
  (debounced, grouped Messages / Documents / Prompts, arrow keys + Enter,
  "Load older messages"). A message hit queues a jump (`messageJumps`) and
  opens the room; `useMessageJump` switches to the message's channel, pages
  history ("Load earlier") until the sequence is loaded (bounded at 40
  pages), then scrolls to and outlines the row (`data-seq` on feed rows). A
  document hit opens its room; a prompt hit opens the Library filtered to it.
- **Verified** on real binaries (room + realtime on MySQL 8.4): chat → task
  mark/toggle seen live as `task_updated` on a member's SSE stream and absent
  from history; non-member 403; search for "data" and a CJK term returns only
  the caller's rooms; a foreign `rooms=` filter returns nothing.
