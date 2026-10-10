# Batch 3: deploy safety, correctness gaps, scale features (2026-10-08)

Status: landed (R1–R14)

Same constraints as batches 1–2 (additive; core flows unchanged; ci.sh all +
`bnbloc smoke --agent` before landing). New rule from the outbox incident:
**every schema change is a new migration id** — ids in deployed commits are
immutable (AGENTS.md).

## Deploy safety

| ID | Item | Acceptance |
|---|---|---|
| R1 | Migration lock | A checked-in lock (id → body checksum) verified by `scripts/ci.sh go`; editing a locked migration fails CI with a message naming the id; new ids are appended to the lock by an explicit update command. |
| R2 | Version visibility | Every service serves `/version` {service, version, commit, builtAt}; the gateway aggregates backends (and its own replica id) at a super-admin route; `bnbloc smoke` and `bnbloc version` report skew; Platform console shows it. Build metadata comes from build args (no hardcoding). |
| R3 | In-cluster nightly smoke | A CronJob (Helm) / scheduled container (compose) runs `bnbloc smoke --alert-on-failure` with an API token secret; no CI system needed. |
| R4 | No silent 500s | Every handler path that answers 5xx logs the cause (structured, no secrets); a test/lint guards new handlers where practical. |

## Correctness gaps

| ID | Item | Acceptance |
|---|---|---|
| R5 | Durable plugin fallback | A notification routed to a desktop plugin keeps a durable pending record; realtime restart before ack still falls back to the server notifier once. |
| R6 | All notifications reach personal bots | Borrow requests (shareagent) and admin alerts (gateway) also route to a member's desktop notification plugin with the same fallback. |
| R7 | Search backfill | A one-shot reindex (CLI/admin route) fills the search index for messages stored before search existed; resumable, batched. |
| R8 | Exact borrow quota | A borrowed run reserves quota at authorization (fail-closed when exhausted), usage is attached after the drive; concurrent borrows can no longer exceed the limit; abandoned reservations expire. |
| R9 | Stateless notifier replies | The notifier registers (card ref → reply token) with bnbloc instead of keeping it in process memory, so any notifier replica can answer a reply. |

## Features

| ID | Item | Acceptance |
|---|---|---|
| R10 | Agent run records | Per job: request, addressed agent, timing per phase, status, error, reply preview, usage; job list + detail view in the room and on the Team page; members see their own and their rooms', admins the tenant. |
| R11 | Organization sync | Auth providers may return groups/departments; super-admin mapping rules (group → tenant, role, default rooms) apply at sign-in and on a periodic resync; deprovisioned users lose access. |
| R12 | Usage budgets | Monthly token/cost budgets per member and per tenant (admin-set); warning notification at a threshold; optional hard stop at drive time; dashboard shows burn. |
| R13 | Audit log page | Admin page over the audit trail (components, API tokens, sessions revocations, borrow decisions, alerts) with filters and CSV export. |
| R14 | Mobile / PWA | Installable web app (manifest, icons, offline shell), mobile layout pass for room, composer, notifications; push works from the installed app. |

## Implementation notes

### R3

In-deployment nightly smoke, no CI system needed
([production-deployment.md](../production-deployment.md#nightly-smoke)).

- **CLI image.** The root `Dockerfile` builds `./cmd/bnbloc` for
  `SERVICE=cli` (a `case` on the build arg; every other value stays
  `./services/<name>/cmd`); `deploy/prod/gen-dockerfiles.sh` generates
  `deploy/prod/cli/Dockerfile` like the service images (`--check` in CI).
  Same distroless base, entrypoint `/usr/local/bin/bnbloc`. The all-in-one
  image also carries the CLI (`/usr/local/bin/bnbloc`, unsupervised).
- **Helm.** `smoke.enabled` renders `templates/smoke-cronjob.yaml`: a
  `bnbloc-smoke` CronJob (`schedule`, optional IANA `timeZone`,
  `concurrencyPolicy: Forbid`, `startingDeadlineSeconds: 3600`,
  `backoffLimit: 0` — a retry would alert twice, `activeDeadlineSeconds`)
  running `smoke --alert-on-failure [--agent] [--alert-to] [extraArgs]` from
  `<repository>-cli:<tag>`, nonroot, with `BNBLOC_GATEWAY` (default the
  `bnbloc-gateway` Service; `gatewayURL` may name the public URL to cover the
  ingress) and `BNBLOC_API_TOKEN` from `smoke.apiTokenSecretRef` (name
  `required` when enabled). Verified with `helm lint` / `helm template`
  (disabled renders nothing; enabled without a Secret name fails the render).
- **Compose.** Chosen: host scheduler, not an in-container sleep loop. The
  prod overlay defines a profile-gated `smoke` service (never started by
  `up`, `restart: "no"`); a host crontab line (or systemd timer) runs
  `docker compose ... run --rm smoke`. A loop container would need a shell
  (the images are distroless) or a scheduler inside the CLI, plus its own
  liveness story; cron already handles time zones (`CRON_TZ`), missed runs
  (systemd `Persistent=true`) and mails a non-zero exit — the fallback
  signal when the alert cannot be sent. Inputs come from `.env.production`
  (`BNBLOC_SMOKE_API_TOKEN`, `_GATEWAY` default `http://gateway:8080`,
  `_AGENT`, `_ALERT_TO`); empty `--agent` / `--alert-to` mean "skip" /
  "every super-admin" in the CLI already. Not added to the dev `compose.yml`
  (dev runs the CLI directly).
- **All-in-one.** Documented: `docker exec --env-file <0600 file> <aio>
  bnbloc smoke --gateway http://127.0.0.1:8080 --alert-on-failure` from the
  host's cron, or the CLI image as the platform's scheduled job.
- Docs: production-deployment (Nightly smoke, CI-that-builds-one-Dockerfile,
  Kubernetes), ci.md, monitoring.md, cli.md, environment-variables.md (CLI
  image + compose inputs), deploy-service-env.md (cli job),
  `.env.production.example`.

### R7

- **Store.** `ChatIndex.ReindexSearchText(ctx, cursor, batch)`
  (`internal/store/search_reindex.go`), cross-tenant. MySQL walks
  `room_messages` in primary-key order `(room_id, seq)` from an opaque cursor
  `"<roomId>/<seq>"` (split on the last slash), 500 rows per batch; the
  payload is selected only for `chat_message` / `agent_messages` rows whose
  `search_text` is NULL or empty, the text is `MessageText` (the exact
  append-path normalization), and each row is written with
  `UPDATE ... WHERE ... AND (search_text IS NULL OR search_text = '')` — so a
  concurrent edit or a second run is never overwritten, and re-running is a
  no-op. The keyset predicate is spelled `room_id >= ? AND (room_id > ? OR
  seq > ?)`: EXPLAIN on MySQL 8.4 shows a PRIMARY range scan for it, but a
  full index scan for the row constructor `(room_id, seq) > (?, ?)`. Rows
  with no text (attachment-only, other kinds) stay NULL and are just passed
  over. Done = a short batch. The memory backend matches payload text at
  query time, so it walks rows with the same cursor contract and always
  reports `updated = 0`. No migration (the column and index exist since
  `2026_10_message_search`).
- **Route.** `POST /v1/platform/search/reindex` `{cursor?, maxBatches?}` →
  `{scanned, updated, cursor, done}` (`wire.SearchReindex*`), served by
  realtime (owner of `room_messages`); the gateway routes exactly that path
  to realtime and gates it like every `/v1/platform/*` path
  (`PermManagePlatform`). One call runs at most `maxBatches` (default 10, cap
  50) and ~10 s of batches, so it stays inside proxy timeouts; a bad cursor
  is a 400, a store error a logged 500; each call logs subject + progress.
- **CLI.** `bnbloc search reindex [--cursor] [--batches]` loops until
  `done`, one progress line per call; a failed call names the cursor to
  resume from (the 1-hour session bounds one invocation).
- Tests: store cursor round-trip + memory walk; MySQL backfill (legacy NULL
  rows become FULLTEXT-searchable incl. a CJK term, empty/non-message rows
  untouched, already-indexed and later-edited text never overwritten);
  realtime route (401 without subject, 400 bad cursor, walk to done,
  resume-at-end no-op); gateway route + permission; CLI loop + resume hint.

### R9

- Seam: `POST /v1/notifier/reply-refs {replyToken, ref, recipient}`
  (gateway, `services/gateway/notifier_reply_refs.go`), authenticated
  exactly like `/v1/notifier/replies` — both go through one
  `verifyNotifierCall` (component header, installed notifier, stored
  signing secret, `notify.Verify` 5-minute window). The token must be live
  and delivered through the calling notifier (404 / 410); `ref` and
  `recipient` are opaque receiving-system ids, ≤128 characters, exact match
  (400 otherwise). 204 on success; same binding again is a no-op; a
  `(ref, recipient)` already bound to another token is 409.
- Key is `(notifier, ref, recipient)`, not `(notifier, ref)`: a notifier
  sends one message to several members (one ref, one token per recipient),
  and the recipient account is what the server-side sender check matches.
  `recipient`/`sender` are the receiving system's account ids — the account
  the message was sent to and the account that answered — because a
  notifier cannot map an inbound account back to a member without state;
  bnbloc already knows the member from the token.
- `/v1/notifier/replies` accepts exactly one of `{replyToken}` or
  `{ref, sender}` (400 otherwise); `{ref, sender}` resolves through the
  registered reference to the token (404 when nothing is registered for
  that pair — e.g. a forwarded message answered by someone else; 410 once
  expired). Distribution, membership, rate limit, clientMsgId idempotency
  and audit are unchanged.
- Storage: table `notification_reply_refs` (both schema files, utf8mb4_bin
  ids), migration `2026_10_b3_r9_notification_reply_refs`; store methods
  on the reply-token store (`internal/store/notification_reply_refs.go`):
  the reference copies its token's expiry, token pruning and notifier
  uninstall remove references too. `notify.ReplyTokens.Register` /
  `ResolveRef` (`internal/notify/replyref.go`).
- Tests: store contract memory + MySQL
  (`internal/store/notification_reply_refs_test.go`); gateway end-to-end
  (`services/gateway/notifier_reply_refs_test.go`): register, idempotent
  re-register, reply by ref posts as the token's member with its addressing,
  retry idempotency, foreign sender 404, foreign-notifier token 404,
  validation 400s, rebind 409, expiry 410, uninstall deletes references.
- Private notifier: registers each `(card refID, recipient account)` after
  sending (background, retried), posts quoted replies as `{ref, sender}`,
  no in-memory card index; any replica answers any reply. The desktop
  personal-bot plugin keeps its local index (one member, one process).
- Contract: `docs/notifications.md` ("Reply references").

### R13
Served by **shareagent** (it owns the tenant registry and
`shared_agent_audit`, and the gateway already routes `/v1/tenant/*` and
`/v1/platform/*` there; no gateway routing change). Routes:
`GET /v1/tenant/audit-log` (manage_org; the signed ctx tenant only — no
tenant parameter is read, and a super-admin targets an org with the usual
`?tenantId=`) and `GET /v1/platform/audit-log[?tenant=]` (manage_platform;
all tenants plus tenantless platform rows, or one tenant). Query: `kind`
(comma list), `actor`, `outcome` (exact), `from`/`to` (unix ms or
YYYY-MM-DD in `tzOffsetMin`'s zone, `to` exclusive), `cursor`, `limit`
(100, max 500), `format=csv` (whole filtered range, newest first, capped at
20 000 rows; `X-Bnbloc-Truncated` set when cut). Reply `wire.AuditLogReply
{entries, nextCursor}` (`internal/wire/audit_log.go`).
- Merge: `store.MergeAuditLog` over `store.AuditLogReader`s
  (`internal/store/audit_log.go`) — `extension_audit` and
  `shared_agent_audit` (memory + MySQL). One total order, `at_ms DESC,
  source rank (extension, borrow), row id DESC`; each reader answers
  "rows after cursor" in that order with limit+1, the merge cuts the page
  and the opaque cursor (base64 `at.source.id`) is the last row's position.
  An unscoped query returns nothing (fail closed). Borrow rows normalize to
  kind `borrow`, subject = agent owner, outcome = decision, detail = deny
  reason/model/channel.
- New audit rows (additive, into `extension_audit`): member add / role /
  remove and join-request approve/reject (kind `member`, ctx tenant),
  tenant create / status (kind `tenant`, the changed tenant), admin "sign
  out member" (`session/revoke_member`) and the revocations a member
  removal or tenant suspension triggers (`session/revoke`, only when
  something was revoked) in shareagent; in the gateway a member's logout,
  single-session revoke, revoke-others and refresh-token reuse
  (`session/logout|revoke|reuse_revoked`, the family's tenant, via
  `GetRefreshFamily`). There is no room-level membership audit; room
  member changes are not in the log.
- Indexes: migration `2026_10_b3_r13_audit_log_indexes` adds
  `extension_audit (tenant_id, at_ms)` and `shared_agent_audit (at_ms)`
  (both schema files).
- Web: an **Audit log** tab in the Organization console
  (`web/src/views/org/AuditLogTab.tsx`): org admins read their tenant; for
  a super-admin the same console is "Platform" and the tab reads the
  platform log with a tenant filter (All tenants / one). Filters: kind
  group, actor, outcome, from/to days; Load more; Export CSV. Path rules in
  `web/src/lib/auditLog.ts` (+ `auditLog.test.ts`), requests in
  `auditLogApi.ts`.
- Tests: `internal/store/audit_log_test.go` (merge order with ties inside
  and across trails, paging at sizes 1/2/3/100, tenant isolation incl.
  tenantless rows, kind/outcome/range filters; MySQL variant),
  `services/shareagent/audit_log_test.go` (tenant view never leaks other
  tenants or platform rows and ignores `?tenant=`, platform paging/filters,
  CSV, bad input 400s, administration rows), `services/gateway/
  audit_log_route_test.go` (routing + RBAC: member/viewer denied, org admin
  denied the platform route; session revocations audited with tenant).
- Limits: API-token, component lifecycle and notifier-test rows are written
  without a tenant, so only the platform view shows them; in-memory dev
  stores are per process (the log needs the shared MySQL store to see rows
  written by other services).

### R5

- **Desktop channel.** The member's desktop plugin is an outbox channel,
  `notify.DesktopChannel` = `builtin:desktop` (same pattern as
  `builtin:web-push`; `internal/notify/desktop.go`). The emitter queues ONE
  desktop row per event (all filtered recipients, plus the message anchor
  `channelId`/`seq` the plugin payload needs) while a `notification.v1`
  plugin is distributed to the tenant (`Emitter.Plugins`, plugin install
  records; `pluginruntime.Subscribes` is the shared record predicate, also
  used by the dispatcher's `pluginsForHook`); otherwise the event goes
  straight to the notifier rows as before (no extra hop for deployments
  without personal bots).
- **Worker routing** (`Worker.attemptDesktop`; `Worker.Desktop` = realtime's
  `PluginHookDispatcher`, `Worker.Emitter` queues the server rows). First
  claim: split recipients by `HasDesktop`; persist the split on the row
  (`routed`, `server`, pending recipients) with the new store outcome
  `NotificationDispatched` — claim token kept, no attempt counted, body
  replaced, `next_attempt_at_ms` = outcome deadline (2 min, > the 60 s ack
  timeout + 10 s sweep) — BEFORE any side effect; then queue `server` under
  the event id and hand each pending member to the dispatcher. Each outcome
  (`DesktopRoute.DeliverNotification(..., done func(delivered bool))`,
  reported once: first delivered ack, else when every offer ended) is
  applied under a per-row mutex: not delivered → server rows under
  `FallbackRowID(event, member)`; the member leaves the row (recorded as
  another `Dispatched` under the same token); the last one finishes the row
  `delivered` with `last_error` naming the fallbacks. A claim after the
  deadline (restart, lost outcome, failed enqueue) re-queues `server` and
  falls back for every still-pending member; all row ids are derived and
  the insert is `INSERT IGNORE`, so it is idempotent; a late outcome from a
  dead lease fails `ErrNotificationLeaseLost` and queues nothing new.
- **Removed (clean cutover).** `Emitter.Desktop`, `Emitter.fallback`, the
  in-memory `func()` fallback in `DesktopRoute`/`deliveryGroup` (now
  `done(bool)`).
- No schema change (the row reuses `notification_outbox`; `Dispatched` is
  a store-level outcome in memory + MySQL). Dispatched rows count as
  `queued` in the outbox gauges; the `notification_deliveries_total` series
  only count final/retry outcomes.
- Tests: `internal/notify/desktop_test.go` (routing + fallback with the same
  event id, delivered needs no fallback, restart: a new worker instance does
  nothing before the deadline, then falls back once for the still-pending
  member only and the notifier receives it once, replayed deadline fallback
  idempotent); `internal/store/notification_dispatch_test.go` (memory +
  MySQL: claim kept, no attempt, deadline re-claim, old token refused);
  realtime dispatcher tests now assert the single reported outcome.

### R6

- One producer path: shareagent already used `notify.NewEmitter`; the
  gateway's admin-alert emitter is now `notify.NewEmitter` too (adds the
  plugin directory and browser push to alerts). Producers only enqueue;
  the routing decision per recipient is the realtime worker's (R5), so
  borrow requests and admin alerts reach a member's connected desktop
  plugin with the same durable fallback. Prefs/mutes/collapse stay decided
  at emit time; admin alerts keep their exemptions (`Operational`).
- A room-less event (admin alert) never gets a default deep link: `url` is
  only the sender's (`Emitter.payload`), so the gateway's
  `BNBLOC_PUBLIC_URL` (if any) cannot change the documented alert payload.
- `SendAlert`: `notifiers` in the 202 lists notifier components plus
  `builtin:desktop` / `builtin:web-push` when they apply; 409
  (`ErrAlertNoNotifier`) only when none of them reaches a recipient.
- Docs: `docs/notifications.md` routing section rewritten (all types,
  durability); the "borrow requests/admin alerts use server notifiers only"
  and "fallback is in-memory" limitations are gone.
- Tests: `TestBorrowRequestAndAlertUseTheDesktopChannel` (notify);
  `TestOtherServicesNotificationsReachDesktopPlugin` (realtime: a
  producer-only emitter, as in shareagent/gateway, queues a borrow request
  and an alert; realtime's worker delivers both to the member's plugin
  stream; the delivered one never reaches the notifier, the failed alert
  falls back with the same event id).

### R10
A run record **is** an agent job: no new table, a read side over
`agent_jobs` served by **activity** (it owns the job queue, bridge reports
and replies). Routes (gateway routes room sub-resource `jobs`, `/v1/my/jobs`
and `/v1/tenant/jobs` to activity; `jobs` was already a route word / id
collection for metric labels):
- `GET /v1/rooms/{id}/jobs?before=&limit=&status=&from=&to=&owner=&core=&provider=`
  — room members (activity membership check, 403 otherwise);
- `GET /v1/rooms/{id}/jobs/{jobId}` — one run of that room (404 for another
  room's job) with the bridge's raw diagnostics and the first reply's room
  sequence (`reply.roomSeq`), found by scanning the persisted feed after the
  request (`RoomFeed`, ≤ 5×200 rows) for the `agent_messages` batch carrying
  the reply's `messageId`;
- `GET /v1/my/jobs` — runs of the caller's agents OR requested by the caller;
- `GET /v1/tenant/jobs[?member=]` — the ctx tenant (gateway manage_org gate
  on `/v1/tenant/*`; a super-admin acts in the signed ctx tenant).
`status` is a comma list (`failed,timed_out`) or `active`; `from`/`to` are
created-at unix ms (`[from, to)`); `limit` 1–100 (default 50); paging by an
opaque `nextBefore` cursor (`<createdAtMs>:<jobId>`, order
`created_at_ms DESC, job_id DESC`). Outside the room scope a run whose room
the viewer is not a member of keeps metadata, outcome, error and usage but
has request/reply text withheld (`redacted`) — an org admin does not read
private rooms' prompts through this view.
Record (`internal/wire/agent_runs.go`): request (`messageSeq`, 280-rune
preview, attachment count), agent (owner/core/provider/target/model/effort),
requester + `borrowed` (requester ≠ owner), status, failure reason, `error` /
`warning` from diagnostics, attempt, phases (`queuedAtMs` = created,
`claimedAtMs` = latest claim, `startedAtMs` = first `running`,
`firstReplyAtMs`, `finishedAtMs` = terminal), reply (first reply id,
preview, time, count) and usage (model, tokens, cost — present only when
reported). Phases: the claim had no timestamp, so `ClaimAgentJobs` now stamps
`claimed_at_ms` (new column). The reply/usage summary is reported by the
bridge through its existing job-status report, additively: the terminal
`replied` / `failed` reports' `diagnostics` gain `reply`
(`wire.RunDiagReply`) and `usage` (`wire.RunDiagUsage`), built from the
turn's posted replies and their reported metrics (`services/bridge/run_report.go`;
`turnUsage` now also notes posted replies). Older bridges send neither; their
runs simply have no reply preview/usage.
Store: `AgentRunReader` (`ListAgentRuns`, `GetAgentRun`; memory + MySQL,
`internal/store/agent_runs.go`, `mysql_agent_runs.go`); the repeated
agent_jobs column list became one `agentJobColumns` constant. Migration
`2026_10_b3_r10_agent_run_records`: `agent_jobs.claimed_at_ms` +
`idx_agent_jobs_room_created`, `idx_agent_jobs_owner_created`,
`idx_agent_jobs_requester_created` (both schema files).
Web: room header **Agent runs** button → `RunsDialog` (list with status
filter, failed runs tinted with their error, load-older paging, 30 s refresh
of the first page; detail drawer with phase timeline and per-phase deltas,
request/reply previews with jump-to-message via `messageJumps`, usage,
diagnostics). Team page: new **Runs** tab (Team/Mine for admins, member
filter in Team scope) and a **Runs** link on every agent row (danger-toned
when the agent has failed turns) that opens the tab narrowed to that agent;
jumping opens the room at the message. Shared view
`web/src/views/runs/RunsBrowser.tsx`; wire/helpers `web/src/lib/agentRuns.ts`
(+ tests), endpoints `agentRunsApi.ts`.
Tests: `internal/store/agent_runs_test.go` (memory + MySQL contract: filters,
tie-broken order, cursor, tenant isolation, claim stamp),
`services/activity/runs_test.go` (bridge status route → record with phases,
reply, usage; membership 403/401; detail reply seq + cross-room 404; my/tenant
scopes; redaction; filters; paging; 400s),
`services/bridge/run_report_test.go`, `services/gateway/runs_route_test.go`.
Known limits: `/v1/my/jobs` is scoped to the session's tenant like the team
dashboard; `reply.roomSeq` is omitted when the reply lies beyond the feed
scan window.

### R11

Organization sync, documented in `docs/organization-sync.md`.
Contract: the auth-provider ACS result is now the typed
`internal/authprovider.Assertion` with optional `groups`, `department`,
`displayName` (the gateway's inline struct is gone).
Core: `internal/orgsync` — `Plan` (ordered rules; first match per tenant
decides role + default rooms, first match overall is the landing tenant;
roles limited to member/org_admin, anything else skipped), `Syncer`
(`Rules`/`Validate`/`SaveRules` over platform setting `org_sync.rules`;
`Apply` converges sync-owned memberships, keeps manual ones, removes stale
sync ones and revokes the refresh families that landed in that tenant
(`RefreshRevokeFilter{Subject, TenantID}`), adds default rooms once; dry run
for the preview; `Resync` over recorded subjects; `Deprovision` removes every
membership, revokes all sign-ins, forgets the subject). Every change is an
extension-audit row of kind `org-sync`.
Gateway: `handleAuthProviderACS` applies sync before `completeSSO` and sets
`identity.Tenant` to the landing tenant (it rides the hand-off and refresh
family as the tenant hint, so desktop/CLI flows land the same way); a sync
failure refuses the sign-in (503). Routes `GET/PUT
/v1/platform/org-sync/rules`, `POST .../preview`, `.../deprovision`,
`.../resync` (super-admin; `services/gateway/org_sync.go`), wired in
`cmd/main.go` from the shared backends.
Store: `TenantMember.Source` (`manual` | `sync`; unset = manual; an upsert
writes it, so an admin add/role change takes the row over — shareagent's
role change now sets manual), `TenantStore.ListMembershipsForUser`,
`OrgSyncStore` (memory + MySQL). Migration `2026_10_b3_r11_org_sync`:
`tenant_members.source`, tables `org_sync_subjects`,
`org_sync_default_rooms` (both schema files).
CLI: `bnbloc org-sync rules|set-rules|preview|deprovision|resync`.
Web: Platform → **Org sync** tab (`web/src/views/org/OrgSyncTab.tsx`, API
`web/src/lib/orgSync.ts`): ordered rule editor, preview, resync, deprovision;
the Members tab marks synced rows.
"Periodic resync" is `POST .../resync` (CLI `bnbloc org-sync resync`) driven
by the operator's scheduler or directory feed — the gateway runs no timer, so
replicas never race one. Deprovisioning cannot be inferred from sign-ins; it
is the explicit deprovision call a directory feed makes.
The private auth provider passes groups/department/display name from IdP
attributes named by env (no defaults), documented in its package README.
Tests: `internal/orgsync/orgsync_test.go` (rule ordering, manual memberships
untouched, removal revokes only that tenant's sessions, super_admin never
granted, default rooms once, dry run, resync, deprovision),
`internal/store/org_sync_test.go` (memory + MySQL),
`services/gateway/org_sync_test.go` (ACS sign-in lands in the rule's tenant
with its role; super-admin gate, 400 on a super_admin rule, preview,
deprovision), `cmd/bnbloc/orgsync_test.go`.
Lock: `migrations.lock` was not updated here (several batch-3 migrations are
still unlocked); run `go test ./internal/store -run TestMigrationLock -update`
once the batch lands.

### R14

Browser only; the desktop shell registers no worker and ignores the manifest
(same bundle). User/operator doc: `docs/mobile.md`.
- **Install**: `web/public/manifest.webmanifest` (name/short_name, `id`/
  `start_url`/`scope` `./`, standalone, theme/background = `--color-bg`
  `#111113`), icons in `web/public/icons/` generated from the desktop app
  icon (192/512 any, 512 maskable and the 180 px apple-touch-icon from a
  full-bleed centre crop of the iOS 1024 icon; commands in the doc).
  `index.html`: manifest link, `theme-color`, apple-touch-icon, iOS web-app
  meta, and `interactive-widget=resizes-content` on the viewport. Gateway
  `webui`: `/manifest.webmanifest` as `application/manifest+json`,
  `no-cache` (Go's MIME table lacks `.webmanifest`); `TestManifestHeaders`.
- **One service worker**: `web/public/push-sw.js` now also carries the app
  shell cache — `shellRoute`: root navigation network-first (cached copy only
  when the fetch fails, so the no-store `index.html` and the reload notice
  stay authoritative), `<scope>assets/*` cache-first, everything else
  (`/v1/*`, `/api/*`, SSE, downloads, non-GET, other origins, the reload
  notice's own non-navigation fetch) not handled at all. Install primes the
  shell (the registering page loaded its assets before the worker existed);
  a changed deployed `index.html` drops the previous build's assets; cache
  `bnbloc-shell-v1`, activate deletes other `bnbloc-*` caches.
  `web/src/lib/appWorker.ts` is the single registration (page load via
  `main.tsx`, and idempotently from `enableBrowserPush`, which no longer
  registers itself); gated by `appWorkerWanted` (not desktop shell, secure
  context, worker API).
- **Mobile layout** (375 px audit with a real local stack): Shell sidebar is a
  real drawer on compact (fixed overlay with backdrop, modal layer/Escape,
  never the collapsed rail, menu button labelled); content dialogs
  (`md`/`lg`) full-screen under 640 px within safe areas, confirms stay cards;
  room header actions move to their own (scrollable) row under the title on
  compact; `PageHeader` wraps actions (My rooms actions ran off-screen,
  Library subtitle was truncated to nothing); Plaza grid `grid-cols-1`
  (a long room name widened the card past the viewport); checkboxes/radios
  excluded from the 44 px coarse-target sizing (they were painted 44 px —
  their label is the target); iOS text fields 16 px (no focus zoom); shell
  height `--app-height` = visual viewport on touch devices
  (`lib/viewport.ts`) so the composer stays above the iOS keyboard, Android
  via `interactive-widget`. Wide layouts unchanged (verified at 1280 px).
- **Push / deep links**: notification click opens `scope + #/room/<id>?seq=`
  (inside the manifest scope, so the installed app opens) or posts it to an
  open client — unchanged path, now from the installed app too. iOS: Settings
  → browser notifications shows an Add-to-Home-Screen hint in an iOS tab
  without push (`lib/pwa.ts` `iosInstallHint`; iPadOS desktop UA counted).
- **Verified** on the gateway path (local dev stack, headless Chromium):
  worker registered at `/` and controlling, shell cache holds `/`, entry
  JS/CSS and lazily loaded chunks; offline reload renders the app; room SSE
  behaves identically with workers blocked vs allowed. Not verifiable here:
  real iOS keyboard/standalone behaviour.
- Tests: `web/scripts/pwa.test.mjs` (worker routing/asset/cache-cleanup run
  in a vm as shipped; manifest/icons/index.html consistency incl. colour =
  token), `web/src/lib/pwa.test.ts`, gateway `webui` test.

### R8

Reserve-then-settle replaces count-at-record (the old path counted a run
only after the drive, so concurrent borrows overran `run_quota` /
`cost_allowance`).

- **Reserve** — `POST /v1/rooms/{room}/shared-agents/{target}/authorization/reserve?owner=`
  `{borrower, model?, channelId?}` (shareagent `reserve.go`). Owner-only
  (`X-Bnbloc-User` = owner; the owner's bridge drives the borrow), the
  borrower must be another room member. The tenant's org ceilings are checked
  first (as before, `org_quota_exhausted`); then `GrantStore.ReserveRun`
  admits the run in ONE conditional `UPDATE shared_agent_grants SET
  runs_used = runs_used + 1 WHERE … revoked_at_ms IS NULL AND (expires_at_ms
  IS NULL OR expires_at_ms > now) AND (run_quota IS NULL OR runs_used <
  run_quota) AND (cost_allowance IS NULL OR cost_used < cost_allowance)` and
  inserts the run's audit row (`turn`/`allow`, actor = borrower,
  `reservation_id`) in the same transaction. A miss is classified by
  re-reading the grant (retry when the re-read admits — a concurrent
  re-authorize). 200 `{reserved, reservationId, expiresAtMs, runsUsed,
  runQuota?}`; 403 `{reserved:false, denyReason}` with
  `owner_not_authorized` (absent/revoked/expired), `quota_exhausted`,
  `cost_exhausted`, `org_quota_exhausted` — every refusal audited as before.
  No new table: the reservation IS the run's audit row (migration
  `2026_10_b3_r8_run_reservations`: `shared_agent_audit.reservation_id`,
  `settled_at_ms`, unique `(tenant_id, reservation_id)`).
- **Settle** — `POST .../authorization/record?owner=` now takes
  `{reservationId, model?, cost?, inputTokens?, outputTokens?}` (clean
  cutover: `borrower`/`channelId` moved to reserve). `SettleRun` updates the
  open, unexpired audit row (usage, reported model, `settled_at_ms`) and adds
  the cost to `cost_used` in one transaction; 404 unknown, 409 already
  settled / expired. A revoked grant still settles (the run happened).
- **Cost semantics (decided)**: a run cannot be undone once driven, so the
  cost allowance is checked at reserve against the cost settled so far
  (`cost_used < cost_allowance` admits one more run) and the actual cost is
  added at settle even when it passes the allowance; the next reservation is
  then refused. Concurrent in-flight runs can therefore overshoot the cost
  allowance by at most their own costs; the run quota is exact.
- **Expiry (decided)**: an unsettled reservation (bridge crash) expires after
  30 min (`store.RunReservationTTLms`): it stays counted (the agent most
  likely ran), keeps its audit row without usage, and a late settle is 409.
  Nothing sweeps — counting happened at reserve.
- **Bridge**: `authorizeBorrow` reserves before `driveClaim` when the bridge
  acts as the owner; the denial reason becomes the room notice
  (`borrowDeniedText`: run quota used up → "Ask <owner> for more runs", cost
  allowance, org quota, not authorized, authority unreachable = fail closed)
  and the job fails `borrow_not_authorized` with `denyReason` in its
  diagnostics. After the drive (any outcome) `settleBorrow` posts the usage
  with the reservation id. `gatewayError` now keeps the (bounded) response
  body so the 403 reason is readable. A serve-all bridge driving another
  owner's agent cannot reserve (owner-only) and keeps the read-only
  `GET .../authorization` check, unaccounted (single-host testing only).
- Unchanged: the composer pre-check (`GET .../authorization` →
  `quotaExhausted = runsUsed >= runQuota`, now including in-flight
  reservations), quota requests (`RaiseRunQuota` never touches `runs_used`),
  usage reports (the reservation audit row is the borrow turn), the legacy
  per-grantee `Controller.Authorize` path (already consumed at authorize;
  `ConsumeDenyReason` is now shared).
- Tests: store `reservationCases` (memory + MySQL: 40 concurrent reserves vs
  quota 5, settle once + cost semantics, expiry, revoked/expired/absent,
  settle after revoke, raise after exhaustion); shareagent
  `reserve_test.go` (20 concurrent HTTP reserves vs quota 3, refusals +
  audit, settle validation/once, expiry) and the billing/quota tests now run
  through reserve+settle; bridge tests (reserve before drive, settle carries
  usage, owner-only reservation, deny reasons from the 403 body).

### R12

- **Store**: table `usage_budgets` (migration `2026_10_b3_r12_usage_budgets`)
  keyed `(tenant_id, user_id)`, `user_id ''` = the tenant budget; nullable
  `tokens` / `cost`, `warn_at_percent` (1..99, default 80), `hard_stop`,
  and `warned_period` / `warned_level` — the highest warning already sent in
  that UTC month. `ClaimBudgetWarning` is one conditional UPDATE (`warned_period
  <> ? OR warned_level < ?`), so each threshold notifies once per month even
  with several activity replicas. `ReplaceUsageBudgets` replaces the tenant's
  whole set in one transaction; a budget whose limits changed re-arms its
  warnings.
- **Service**: activity (`services/activity/budgets.go`) — it already owns
  the reported-usage aggregation (`AgentUsageReader`, the team dashboard's)
  and the job claim. `GET/PUT /v1/tenant/budgets` (manage_org via the
  `/v1/tenant/*` gate) answers `{period, periodStartMs, periodEndMs, tenant,
  members[], usage[]}` with each budget's burn / percent / reached and every
  owner's burn this month; PUT validates (a token or cost limit, warn 1..99,
  member of the tenant, no duplicates, ≤1000). `GET /v1/my/budget` (any
  member) answers the caller's budget (or null), burn, and `blocked` /
  `blockedBy` (`member` | `tenant`). Gateway routes both to activity.
- **Burn**: tokens = reported input + output tokens, cost = reported
  `costUsd`, over the UTC calendar month, from reply metrics attributed to
  the agent's owner. **Borrowed runs count against the OWNER's budget** — they
  consume the owner's agent (the same attribution as the team dashboard).
  Unreported usage counts as nothing.
- **Warnings**: after `agent-activity/messages` stores replies with reported
  metrics, activity schedules a debounced (5 s) evaluation of each owner's
  budget and the tenant budget; a PUT evaluates every budget it set. Crossing
  `warnAtPercent` and then 100% each emit one `budget_warning` (new
  operational notification type: explicit recipients, no prefs/mutes/
  collapse) through the shared notification emitter — to the member, or to
  the tenant's org admins for the tenant budget; jumping straight to 100%
  sends only the 100% warning. The link opens Settings / Team when activity
  has `BNBLOC_PUBLIC_URL` (now wired for activity in compose, prod overlay,
  Helm, both env docs).
- **Hard stop**: enforced at the job claim — the pre-drive call every bridge
  already makes, so no new bridge round trip or cache. A claimed job whose
  owner's hard-stop budget (or the tenant's) is reached is failed
  `budget_exhausted` (new final failure reason; web label "monthly budget
  reached") by the claiming bridge's lease, published, and the room gets a
  job notice "…monthly budget reached: the <agent> agent did not run…"; the
  bridge never sees it. A budget read error lets the job run (budgets are a
  cost control, not an access boundary).
- **Web**: Team → **Budgets** tab (org admins): organization budget, member
  budgets (member picker, tokens / cost / warn % / hard stop, burn bar), and
  this month's usage per member; one Save replaces the set. Settings →
  **Usage budget**: the member's budget and burn bar, or their burn when no
  budget is set, and the hard-stop notice. Helpers in `web/src/lib/budgets.ts`
  (node-tested), calls in `budgetsApi.ts`.
- Known limits: the month is UTC (not the member's timezone); burn only sees
  usage cores report; a run already driving when the budget is reached
  finishes (enforcement is per claim).
- Tests: store `budgetCases` (memory + MySQL: replace/list/get, tenant
  isolation, concurrent claim once, levels, month re-arm, limit-change
  re-arm); activity `budgets_test.go` (round trip with burn + validation,
  hard stop at claim for member and tenant incl. a borrowed job, warnings once
  per threshold/month to the member and the org admins, reply ingest triggers
  the evaluation); gateway route/permission test.

### R1

- `internal/store/migrations.lock` (text, one line per entry): `migration
  <id> <sha256>` for every entry of `var migrations`, where the hash is over
  the entry's Go **token stream** (go/scanner: comments, blank lines and gofmt
  layout excluded; any change to a literal, call or argument changes it), and
  `table <name> <sha256>` for every `schema.sql` table over its CREATE TABLE
  with `--` comments stripped and whitespace collapsed.
- `TestMigrationLock` (internal/store, plain `go test`, so `scripts/ci.sh go`
  runs it) fails when: a locked id's hash changed ("migration <id> is
  deployed-immutable; add a new migration instead"); a locked id was removed
  or moved before a later-locked one; an id appears twice; a `schema.sql`
  table changed / appeared / disappeared and no **unlocked** migration names
  it in a string literal (this is what catches the 2026-10-08 outbox
  incident — the edited migration's Go body was unchanged, only the
  `schema.sql` table it reads changed); or there are unlocked ids / table
  changes (instructions to update).
- Update: `go test ./internal/store -run TestMigrationLock -update` rewrites
  the header and existing `migration` lines verbatim, appends new ids in list
  order, and refreshes the `table` lines; it refuses (fails, writes nothing)
  while any violation stands, so the lock can only absorb new migrations.
- Verified against the incident in a scratch copy: a comment edit passes; a
  literal change in a locked body fails naming the id; a `schema.sql` column
  added without a migration fails naming the table; adding a migration that
  names the table then `-update` appends exactly that id and that table hash.
- Limit: a shared helper a locked body calls (`addColumnIfMissing`,
  `createSchemaTable`, ...) is not hashed; changing a helper's semantics needs
  the same care.
- The lock was generated at HEAD 87e1c0f (47 ids); migrations added
  concurrently in this batch are appended by the final `-update`.

### R2

- `internal/buildinfo`: `Commit` / `BuiltAt` set by `-ldflags -X`; a plain
  `go build` in a checkout falls back to the toolchain's `vcs.revision`.
  `Get(service)` adds `Instance` (host name = pod name). `Evaluate` decides
  consistency (short/full hashes of one commit match by prefix; an unknown
  commit, a missing web identity or an unreachable service is a problem).
  Wire shapes in `internal/wire/versions.go`.
- `GET /version` is mounted by `internal/obs` on every internal listener (not
  wrapped by svcauth, like `/healthz`); the gateway's public listener does
  not serve it (it reports itself in-process).
- Gateway `GET /v1/platform/versions` (super-admin): itself + embedded web
  bundle + 5 `/version` samples per backend (room, activity, realtime,
  shareagent, relay) on a keep-alive-free client so each sample opens a new
  connection (ClusterIP balances per connection); instances deduped. The relay
  is addressed through a new optional gateway env `BNBLOC_RELAY_BASE_URL`
  (compose, Helm peers, all-in-one entrypoint, both env docs).
- Web bundle identity: the Vite build emits `build.json` (`{version,
  commit}`) beside index.html; `webui.Build()` reads it from the embed.
- Build surfaces: root `Dockerfile` build stage takes `ARG BNBLOC_COMMIT` and
  stamps commit + `date -u` build time (generated `deploy/prod/<svc>/`
  Dockerfiles regenerated); all-in-one Dockerfile stamps every binary and its
  web stage now gets the commit and the desktop package version; compose.yml
  passes `BNBLOC_COMMIT` to every build (and the prod overlay's smoke
  image); the desktop sidecar script stamps the bridge (`BNBLOC_COMMIT`, else
  `git rev-parse --short=12 HEAD`) and the bridge logs its identity at start.
- CLI: `bnbloc version` (5 samples of the route merged, table, `SKEW` lines,
  non-zero exit); `bnbloc smoke` gained a `versions` step right after `auth`:
  FAIL on skew without stopping the chain (the run fails at the end), `WARN`
  (new non-failing step outcome) for a non-super-admin caller.
- Web: Platform console → Overview → **Deployment** card (super-admin): per
  service commit(s) and instances, web bundle, unreachable services, a skew
  warning with the problems, and a hint when the open tab runs an older bundle.
- Verified on the real runtime path: stamped room/relay/gateway binaries,
  `bnbloc version` against the gateway listed every instance with commit and
  build time; restarting the relay with another commit printed the skew and
  exited 1.

### R4

- Convention: before any handler 5xx write, `obs.ServerError(w, "<what
  failed>", err)` (nil when there is no underlying error, e.g. a feature not
  configured). It does not write the response, so each service keeps its body
  shape. Behind `obs.Middleware` the cause is attached to the request's log
  line; every 5xx request line is now Error level with
  `event=bnbloc.http.5xx`, `what`, `error` — and `what=unrecorded` when a
  handler skipped the convention (the runtime net). Without the middleware
  (handler tests) `ServerError` logs on its own. `obs.ProxyError` is the
  ReverseProxy error handler (gateway backend proxy, auth-provider proxy).
- Guard: `TestNoSilent5xx` (internal/obs) parses every non-test Go file under
  `services/` and `internal/` (internal/obs exempt) and fails on a call with a
  constant 5xx status argument not preceded in its block by a log call; when
  the branch is about an error (`if X != nil`, `errors.Is/As(X, …)`, a switch
  whose cases test X, or the function's `error` parameter) the log call must
  name X. Statuses held in variables are out of its reach (documented); the
  one such helper that produced 5xx (`postNotifierReply`) now returns the
  cause and the handler logs it.
- Fixed 212 silent sites in 50 files (gateway, room, realtime, activity,
  shareagent, relay, internal/notify), mechanically inserted and reviewed:
  "not configured" 503s log a nil cause; switch-default and helper branches
  name their error.
- Verified on the runtime path: a gateway proxy to a dead backend produced
  one Error line `status=502 event=bnbloc.http.5xx what="backend proxy
  failed" error="dial tcp …: connection refused"`.
