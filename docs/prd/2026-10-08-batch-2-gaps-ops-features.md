# Batch 2: gap fixes, operations, collaboration features (2026-10-08)

Status: landed (G1–G4, O1–O2, N1–N4; G5 ships with desktop 0.1.13)

Constraint for every item (same as batch 1): additive, existing core flows
(login, rooms, chat, agent reply, borrow) keep their behaviour; new routes,
tables (both schema files + migrations.go), UI entries. Lands only after
`scripts/ci.sh all` and `bnbloc smoke` pass.

## Gaps

| ID | Item | Acceptance |
|---|---|---|
| G1 | Plugin hook delivery reaches desktop sidecars | Fix `docs/bugs/2026-10-08-plugin-hook-delivery-unreachable.md`: realtime stays the hook authority; hooks reach a member's desktop sidecar through the member's own authenticated channel (desktop/bridge), with delivery audited. No server→member-machine dialing. |
| G2 | Borrow billing carries cost | The bridge records the borrowed turn's tokens/cost (from the core's reported usage) on the borrow accountability record; billing reports and the team dashboard show cost when reported. |
| G3 | Notification deep links + reply summary | Mention / agent links open the room AND scroll to the message; agent_finished carries a short summary of the agent's reply. |
| G4 | Signed-in devices page | Settings lists the member's sessions (`/v1/my/sessions`) with client, created, last used, current; revoke one / all others. |
| G5 | Intel macOS updates | Publish a universal (or x86_64) build for `darwin-x86_64` alongside arm64. |

## Operations

| ID | Item | Acceptance |
|---|---|---|
| O1 | Nightly smoke with alert | Scheduled job runs `bnbloc smoke --agent …`; a failure alerts admins through the installed notifier (new super-admin "send alert" path + CLI). |
| O2 | Monitoring | Metrics for bridge heartbeats, notification delivery outcomes, SSE connections; Prometheus scrape + alert rules shipped in `deploy/` and documented. |

## Features

| ID | Item | Acceptance |
|---|---|---|
| N1 | Reply from the messenger | A member replies to a notification in the enterprise messenger; the reply is posted into the room as that member (addressed like the original thread: to the agent for agent results). Generic signed reply seam in public code; messenger specifics in the private notifier. |
| N2 | Scheduled prompts | A room member schedules a prompt (cron-like: daily/weekly/at time, timezone) addressed to an agent or the room; at the time it is posted as that member through the normal chat path (borrow rules apply). List/pause/delete; multi-replica safe. |
| N3 | Room templates | Create a room from a template (agents to invite/authorize hints, starter prompts, description, default members); save a room as a template; tenant-shared or private. |
| N4 | Browser push | Web Push (service worker + VAPID) as a built-in notification channel for browser members, honouring the same prefs/mutes. |

## Implementation notes

### G1

- Design + semantics recorded in
  `docs/bugs/2026-10-08-plugin-hook-delivery-unreachable.md` (now fixed).
  Realtime is hook authority + auditor only; the server→`localUrl` POST path
  (`pluginruntime.Sidecar`/`NewSidecar`, dispatcher `post`) is deleted.
- Transport: per-member SSE `GET /v1/my/plugin-events` (gateway → realtime,
  `PermRead`) held by the desktop sidecar manager while it has a plugin
  sidecar installed; frames `event: plugin_event` carrying
  `wire.PluginDelivery{id, plugin, envelope}`. The desktop
  (`apps/desktop/src-tauri/src/internal_ext/plugin_events.rs`) posts the
  envelope to the plugin's running loopback runtime URL `/events` and acks
  `POST /v1/my/plugin-events/{id}/ack {status: delivered|failed, detail}`.
  The bridge is not involved (it knows neither plugins nor their ports).
- Entitlement per member stream: rollout ≠ off, record tenant scope admits
  event tenant and session tenant, opt-in needs the member's
  `component_installs` row, member of the event's room. Per-member delivery:
  each entitled member's desktop gets the event; offline desktops miss it.
- Bounds: publish path is a non-blocking hand-off (queue 256); one fan-out
  worker (membership probes happen there, order preserved); per-stream
  buffer 64; pending-ack table 4096; unacked → audited `failed` after 60s;
  8 streams per member. Audit rows: `kind=plugin action=deliver
  actor=<member>`.
- Probe: plugin `sidecar.localUrl` must be loopback http
  (`pkgmeta.ValidateLoopbackHTTPURL`, now exported); the NetGuard
  localUrl-vs-`permissions.network` check is gone (no server-side client to
  guard). `routeLabel` learned `plugin-events`/`ack`.
- No schema change, no env var, no web UI change.

### G2
- Where the usage is seen: the bridge's `reflow` consumes each completed
  assistant message and posts it to `agent-activity/messages` with its
  `metrics`; the same loop now folds each consumed reply's metrics into a
  per-turn `turnUsage` (`services/bridge/borrow_usage.go`), returned on
  `driveResult`. A reply whose post is retried is counted once (only on a
  successful or permanently-rejected post).
- The record moved from "right after the authorization check" to "after the
  drive ends" (any outcome — every authorized drive still counts one run, as
  before), on the bridge root context. Body additively carries `model` (the
  reported model, else the job's requested model), `inputTokens` /
  `outputTokens` (only when a reply reported usage) and `cost` (only when a
  reply reported `costUsd`). Quota is still enforced at record time.
- One reading of the metrics map: `wire.TurnUsageFromMetrics` (model,
  reported, tokens, costUsd) is shared by the bridge and the memory team
  dashboard aggregation (the MySQL one stays in SQL over the same keys).
- shareagent `.../authorization/record` accepts `inputTokens`/`outputTokens`
  (0..1e12, 400 otherwise) and clips an over-long model to the column; the
  audit row gained nullable `input_tokens`/`output_tokens` (migration
  `2026_10_g2_borrow_usage_tokens`; NULL = not reported).
- Usage report rows and totals gain optional `inputTokens`/`outputTokens` /
  `totalInputTokens`/`totalOutputTokens` (absent when no turn reported any);
  CSV appends `input_tokens,output_tokens` columns (empty when unreported).
- Web: Team → Usage & billing shows a Tokens column when the report carries
  tokens (and Cost as before); the Team agents tab already showed reply
  tokens/cost and now shares the `formatTokens` helper. Nothing is shown as 0
  when unreported.

### G4
- Web: Settings → **Signed-in devices** (`web/src/views/settings/SignedInDevices.tsx`,
  pure helpers in `web/src/lib/devices.ts`, calls in `devicesApi.ts`). Rows:
  client label (Browser / Desktop app / Command line; bridge sessions as
  "Desktop agent", nested under the device they were forked from), signed in /
  last used (relative, exact time on hover), user agent on hover, "This
  device" badge. Revoke per row with a confirm; revoking this device revokes
  it (and its agent) and runs the normal sign-out. "Sign out all other
  devices" with a confirm and a toast of the count.
- Server: a fork now records its parent family
  (`refresh_token_families.parent_family_id`, migration
  `2026_10_g4_session_parent`); `GET /v1/my/sessions` reports it as
  `parentId`. `DELETE /v1/my/sessions/{id}` also revokes the sessions forked
  from it (a device's agent). New `DELETE /v1/my/sessions?others=1` revokes
  every active sign-in except the caller's own family and its forks, answers
  `{"revoked": n}`; a bare DELETE is 400 (never "revoke all"), a bearer
  without a family is 409. Selection runs in the gateway over the member's
  listed families (no new store filter).
- Known limit: bridge sessions forked before this change have no parent, so
  they are listed as standalone "Desktop agent" rows and count as "other".
  Revocation stops refresh; an already-issued session bearer stays valid
  until it expires (at most 1 h), as with every H4 revocation.

### G3
- **Deep links** (`internal/notify/deeplink.go`): `url` is
  `<BNBLOC_PUBLIC_URL>/#/room/<roomId>?seq=<n>[&channel=<channelId>]` —
  mention → the mentioning message; `agent_finished` → the agent's reply;
  `agent_failed` / `borrow_request` → the request message (`job.messageSeq`).
  `notify.Event` gained `Seq`/`ChannelID`; seq 0 keeps the plain room link.
- **Web routing**: `web/src/lib/hashRoute.ts` (pure parse/serialize, node
  tested) adds the `?seq=&channel=` anchor to room routes; `App.tsx` hands an
  anchored route to `messageJumps` (the global-search jump: switch channel,
  page history, scroll, outline — `useMessageJump`) and replaces the URL with
  the canonical room hash so a reload does not jump again. Works whether the
  room is already open (same RoomView, subscription) or not yet mounted
  (taken on mount).
- **Reply summary** (`internal/notify/agentreply.go`, `summary.go`): the
  observer now also taps `agent_messages`. The reply is paired with its job
  through the activity session: the bridge's replied report carries
  `diagnostics.sessionId`, the reply batch its `agentSessionId`. The observer
  keeps each session's latest reply (TTL 10 min, bounded) and consumes it when
  the job's replied event arrives (a reply older than the job's start minus
  5 s is not this job's); a job whose reply has not arrived is parked up to
  20 s and completed by the reply, else sent without a summary. All of this
  runs on the observer goroutine — publish never waits. Job notice sessions
  (`job-<id>`) are not replies. `text` = `<summary>\n\nRequest: <request>`;
  the summary is the turn's last reply message as plain text (markdown
  dropped, link/image text kept, fenced code → `[code]`, whitespace
  collapsed, 280 runes). `agent_failed` keeps its failure reason.
- Tests: `TestReplySummary`, `TestMessageURL`,
  `TestObserverDeepLinksAndReplySummary` (reply-then-job, notice ignored,
  mention anchor), `TestObserverParksJobUntilReplyArrives`,
  `TestObserverEmitsWithoutReplyAfterWait` (stale reply rejected, timeout,
  failed-job link); web `hashRoute.test.ts`.

### N4
- **Channel**: built-in, not a component. Outbox items for it use the
  notifier value `builtin:web-push` (`notify.WebPushChannel`; ':' is not a
  valid component name). `Emitter.Push` (the subscription store, wired in
  realtime and shareagent) adds one item per event for the recipients with a
  subscription, after the unchanged actor/prefs/quiet-room/collapse filter;
  an event is now queued when a notifier is visible **or** a recipient has a
  subscription. `Worker.send` routes the item to `WebPush.Deliver`, whose
  per-recipient results go through the same `conclude` narrowing/backoff as
  notifier results (extracted from `send`, shared).
- **Delivery** (`internal/notify/webpush.go`, library
  `github.com/SherClockHolmes/webpush-go` v1.4.0, MIT; pulls
  `golang-jwt/jwt/v5` and `golang.org/x/crypto`, pinned at v0.45.0 — go.mod
  directive became `go 1.24.0`): RFC 8291 aes128gcm + VAPID, TTL 24 h,
  `Topic` per event; message `{id,type,roomId,title,body,url,path}`.
  404/410 → subscription deleted; 429/5xx/transport → retryable; other →
  final for that device. Endpoints are member-supplied: the client dials only
  public addresses (loopback/private/CGNAT/link-local/metadata refused at
  connect time, an address refusal drops the subscription), goes through
  `HTTPS_PROXY` when set, and never follows redirects.
- **Config**: `BNBLOC_WEB_PUSH_VAPID_PUBLIC_KEY` / `_PRIVATE_KEY` /
  `_SUBJECT` (realtime; subject defaults to an https `BNBLOC_PUBLIC_URL`).
  Neither key = off (routes 404, toggle hidden); half/mismatched pair or no
  subject = fatal at startup. Not required in production. `bnbloc keys
  gen-vapid` prints a pair. Wired in compose, prod overlay, Helm
  (`webPush.publicKey/subject/privateKeySecretRef`), aio notes,
  `.env.production.example`, both env docs, `docs/cli.md`.
- **Storage**: `web_push_subscriptions` (both schema files; migration
  `2026_10_n4_web_push_subscriptions`), key `(tenant_id, sha256(endpoint))`,
  owner-scoped delete, at most 20 per member (oldest evicted), deleted when a
  member is removed from the tenant (shareagent).
- **Routes** (realtime; gateway routes `/v1/push/*` and
  `/v1/my/push-subscriptions` there, PermRead): `GET /v1/push/vapid-public-key`,
  `POST`/`DELETE /v1/my/push-subscriptions`.
- **Web**: `web/public/push-sw.js` (push → `showNotification` with
  `tag`=event id; click → focus an open tab and post the hash route to it,
  else open one); the gateway serves it with `Cache-Control: no-cache` from
  the root (scope `/`; no CSP is set). `lib/webPushModel.ts` (pure, node
  tested) + `lib/webPush.ts` (registration, subscribe, re-register on
  Settings load, rotated-key replacement, sign-out unsubscribe);
  Settings → Notifications → "Browser notifications on this device"
  (`views/settings/BrowserPush.tsx`), hidden in the desktop shell (which never
  registers the worker) and when the channel is off.
- Tests: store contract (memory + MySQL), routes (disabled 404, validation,
  idempotent subscribe, owner-scoped delete, cap), emitter fan-out with prefs,
  worker delivery against a fake TLS push service that **decrypts** the
  RFC 8291 body with the browser keys and checks the VAPID header (410 drops,
  503 retries then succeeds), disabled-channel give-up, public-address
  check, config validation; gateway route/permission test; webui header
  test; web `webPushModel.test.ts`.
- Known limits: no real browser/push-service round trip in CI (the
  encryption is verified by decrypting); a subscription survives a VAPID key
  rotation server-side until its next delivery fails (the web replaces it on
  the next Settings visit).

### N1

- Contract recorded in `docs/notifications.md` ("Replies from the receiving
  system"). `mention`, `agent_finished`, `agent_failed` payloads carry
  `reply {channelId, recipient?}` and `recipients[].replyToken`; mentions
  answer to the room (human chat), agent outcomes to the same
  `{ownerId, provider, core}` the request used (borrow enforced by the bridge
  as for any chat). `borrow_request`/`test`/alerts take no reply.
- Token: server-side, not a signed token. Realtime mints it per recipient at
  delivery (never in the outbox) and holds no session-signing key; the token
  stays opaque and short; it is revocable (uninstall deletes a notifier's
  tokens) and independent of key rotation. Table
  `notification_reply_tokens` (SHA-256 of the token as PK, tenant, notifier,
  event, room, channel, member, addressing, expiry), migration
  `2026_10_n1_notification_reply_tokens`, both schema files. TTL 7 days, any
  number of replies until then; expired rows pruned by the delivery worker.
  Code: `internal/notify/replytoken.go`, `internal/store/notification_replies.go`.
- Route `POST /v1/notifier/replies` (gateway, no session;
  `services/gateway/notifier_replies.go`): `X-Bnbloc-Component` names the
  notifier, `X-Bnbloc-Signature` is verified against its stored secret
  (`notify.Verify`: constant-time, 5-minute window); then token (404
  unknown/foreign notifier, 410 expired), distribution at post time (403:
  uninstalled, rollout off, tenant out of scope, opt-in not chosen, tenant
  suspended, member left the tenant or read-only role), the member's chat
  rate limit (429), 16 KB (413). The text is posted through realtime's own
  `POST /v1/rooms/{room}/chat` as the member (svcauth-signed identity), so
  room membership at post time (403), clientMsgId idempotency
  (`clientMsgId = ntfr_ + sha256(notifier, member, idempotencyKey)`), agent
  job creation and borrow rules are the chat path's. Answer
  `{roomId, sequence}`. Every decision is audited (`kind=notifier
  action=reply`). No new env var: the notifier reaches the gateway over the
  private network.
- Tests: store contract (memory + MySQL); worker minting per recipient /
  addressing / expiry / prune (`internal/notify/replytoken_test.go`);
  gateway end-to-end against a real realtime chat endpoint — signature,
  stale timestamp, unknown component/token, foreign token, expiry, membership
  revoked, rollout off → auto, uninstall deletes tokens, idempotent retry,
  room vs agent addressing (`services/gateway/notifier_replies_test.go`).
- Private notifier (messenger side): inbound websocket per active bot,
  quote → card refID → token mapping, signed post to this route; documented
  in the private repo (notifier README).

### O1

- **Unattended credential: API tokens.** A refresh token rotates on every use
  (a CI secret cannot store the rotated value) and a session lives 1 h, so
  neither fits a scheduled job. New super-admin API tokens: table
  `api_tokens` (both schema files, migration `2026_10_o1_api_tokens`; only
  the SHA-256 of the secret is stored; platform-wide, not tenant-scoped),
  `store.APITokenStore` (memory + MySQL). Routes (gateway,
  `services/gateway/api_tokens.go`): `POST/GET /v1/platform/api-tokens`,
  `DELETE /v1/platform/api-tokens/{id}` (super-admin), and
  `POST /v1/auth/api-token {token}` (the token is the credential;
  IP-throttled like login) → a 1-hour session that re-runs tenant + role
  resolution at every exchange (no refresh family). Subject = the issuer or a
  `svc:<name>` service subject — never another person. Format
  `bnbloc_at_<32 hex id>_<secret>`; TTL 1–365 days (default 90). Issue,
  revoke and every exchange (allow/deny) go to the extension audit trail
  (`kind=api-token`, name = token id).
- **Admin alerts.** `POST /v1/platform/alerts {title, text?, url?,
  recipients?}` (super-admin) → notification type `admin_alert`
  (`wire.NotificationAdminAlert`) through the existing outbox/notifier
  delivery, produced by a producer-only `notify.Emitter` in the gateway
  (`Emitter.SendAlert`, `internal/notify/alert.go`). Default recipients = the
  gateway's `BNBLOC_SUPER_ADMINS` entries (the only definition of platform
  super-admins; they are configuration, not rows). `notify.Operational(type)`
  makes the emitter skip actor exclusion, member prefs, muted rooms and the
  collapse window; component distribution (tenant scope, rollout, opt-in)
  still applies. Queued in the caller's session tenant; 202
  `{id, recipients, notifiers}`, 409 when no notifier reaches a recipient,
  400 invalid / no recipients, 503 without a store. Audited
  (`kind=admin-alert`). Room-less payload: title without a room suffix, the
  sender's url (the gateway reads no public URL).
- **CLI.** `BNBLOC_API_TOKEN` (precedence: `--token` > `BNBLOC_TOKEN` >
  `BNBLOC_API_TOKEN` > stored sign-in; exchanged once per process);
  `bnbloc alert --title --text [--url] [--to]`;
  `bnbloc api-tokens create|list|revoke`; `bnbloc smoke --alert-on-failure
  [--alert-to]` sends one alert with the gateway and every `FAIL` line after
  cleanup; a failed alert prints `smoke alert NOT sent: <why>` on stderr and
  the run still exits non-zero.
- **Workflow.** `.github/workflows/nightly-smoke.yml` (schedule 02:41 UTC +
  `workflow_dispatch` with an `agent` input): builds `./cmd/bnbloc` and runs
  the smoke against `vars.BNBLOC_SMOKE_GATEWAY` (unset = job skipped) with
  `secrets.BNBLOC_SMOKE_API_TOKEN`; `vars.BNBLOC_SMOKE_AGENT`,
  `vars.BNBLOC_SMOKE_ALERT_TO`, runner labels from
  `vars.BNBLOC_CI_LINUX_RUNS_ON`. Docs: `docs/ci.md#nightly-smoke`,
  `docs/cli.md`, `docs/notifications.md#admin-alerts`,
  `docs/environment-variables.md` (CLI + `BNBLOC_SUPER_ADMINS`).
- Tests: `internal/store/api_tokens_test.go` (memory + MySQL),
  `internal/notify/alert_test.go`, `services/gateway/api_tokens_test.go`
  (lifecycle, impersonation refused, revoke, audit, alert queueing/defaults/
  409/503), `cmd/bnbloc/alert_test.go` (API token exchange once, alert on
  failure, alert failure reported). actionlint clean.

### O2

- **Registry.** `internal/obs` gained labeled domain series
  (`series.go`: `CounterInc/CounterAdd/GaugeSet/GaugeAdd/Describe`, rendered
  as `bnbloc_<name>{service=…,<labels>}` beside the request metrics) and
  `obs.WithMetrics(m)` so a service serves its own registry through
  `ServeWithHealth`. Label values come only from code-owned sets.
  `routeLabel` knows `api-tokens` (id collection), `api-token`, `alerts`.
- **Series.** realtime: `bnbloc_realtime_sse_connections{kind=member|bridge}`
  (`?presence=0` = bridge), `bnbloc_realtime_events_published_total{kind}`
  (unknown kinds → `other`), `bnbloc_notification_deliveries_total{outcome}`
  (worker), `bnbloc_notification_outbox_items{state=queued|retrying|delivered|failed}`
  and `bnbloc_notification_outbox_oldest_pending_age_seconds` (30 s snapshot
  via `NotificationOutboxStats`). activity: `bnbloc_bridge_heartbeats_total`
  (at `POST /v1/bridge/heartbeat`), `bnbloc_bridges_online` (15 s snapshot
  via `CountOnlineBridges`, same liveness rule as the team dashboard).
  shareagent: `bnbloc_borrow_decisions_total{decision,reason}` from the lease
  path (`Controller.audit`, release excluded) and the accountability record.
  Alert-relevant series are registered at zero at startup. DB snapshots are
  identical on every replica (`max()`), counters per replica (`sum()`).
- **Shipped config.** `deploy/monitoring/prometheus.yml` (gateway `:9090`,
  others on their main listeners) and `deploy/monitoring/alerts.yml`
  (service down, 5xx ratio, no online bridges 15 m, notification failures,
  outbox oldest pending > 15 m, optional no member streams in business
  hours); validated with Prometheus' own config loader + `rulefmt` parser.
  Docs: `docs/monitoring.md`, linked from `docs/production-deployment.md`.
  No new env var or deploy-surface change.
- Tests: `internal/obs/series_test.go`, `services/realtime/metrics_test.go`,
  `services/activity/metrics_test.go`, `internal/shareagent/metrics_test.go`,
  `internal/notify/alert_test.go` (worker metrics),
  `internal/store/api_tokens_test.go` (ops stats, memory + MySQL).

### N2

Scheduled prompts are addressing, not orchestration
(`2026-09-11-agent-collaboration-boundary-decision.md`): a member's own
message, posted at a time, as that member, through the normal chat path.

- **One chat path**: `handleChat` was split into `PostChat`
  (`services/realtime/chat_send.go`), called by the chat route and the
  scheduler alike. Persistence, `clientMsgId` idempotency, channel routing,
  agent-job creation (the bridge claims by recipient), plugin hooks and
  notifications therefore behave identically for a scheduled post. The post
  carries `payload.scheduled {scheduleId}`; the feed renders a clock badge.
- **Recurrence engine** `internal/schedule` (pure): `once` / `daily HH:MM` /
  `weekly days+HH:MM` / five-field cron-lite (`*`, lists, ranges, steps; cron
  day-of-month OR day-of-week rule), evaluated on the schedule's IANA zone wall
  clock. DST is explicit (`LocalInstant`): a time in the spring-forward gap
  runs at the shifted instant after it (02:30 → 03:30), an overlap time runs
  once at the earlier instant. Minimum spacing 5 minutes, enforced statically
  (circular day check) and at run time (`NextRun` never plans a run closer
  than 5 minutes to the previous, which DST could otherwise compress).
  Missed slots collapse (a scheduler that was down posts once).
- **Storage**: `room_schedules` (both schema files; migration
  `2026_10_n2_room_schedules`), deleted with the room (`DeleteRoom`).
  `ClaimDueSchedules` is one `UPDATE … WHERE enabled AND next_run_at_ms <= now
  AND claim free/expired ORDER BY next_run_at_ms LIMIT n` with a store-minted
  per-call token (2-minute lease), so two schedulers never run one slot;
  `CompleteScheduleRun` advances `next_run_at_ms`/auto-pause only while the
  claim still matches (an edit during a run wins).
- **Exactly-once posting**: `clientMsgId = sched:<scheduleId>:<slotMs>`, so a
  slot re-claimed after a crashed run dedupes in the chat store instead of
  posting twice.
- **Scheduler** (realtime, the singleton that owns chat publish): wakes at each
  minute boundary (+1s), claims due schedules, checks the creator is still a
  room member (else auto-pause with `lastError`: creator left / room deleted),
  posts via `PostChat`. A deleted channel or invalid text auto-pauses;
  a persistence failure keeps the slot for the next tick; a slot more than 1h
  late is skipped and recorded (`skipped`), not posted late.
- **Borrow**: an agent recipient is the same `recipient {ownerId, provider,
  core}` a typed message carries; the agent job's requester is the creator, and
  the bridge enforces the owner's per-room borrow authorization at drive time.
  The UI warns when scheduling another member's agent.
- **Routes** (realtime; gateway routes `/v1/rooms/{id}/schedules*` there):
  `GET/POST /v1/rooms/{id}/schedules`, `PATCH/DELETE …/{sid}`,
  `POST …/{sid}/run`. Any member lists; the creator edits, resumes and runs
  now (it posts as the creator); the room owner/admin may pause or delete any
  schedule but never resume one (that would post as someone else). Run-now
  is rate-limited like chat at the gateway.
- **Caps**: 50 schedules per room, 20 per member per room — protocol-level
  sanity bounds of a human-managed panel, argued in `schedules.go`; no env.
- **Web**: `lib/schedules.ts` + `schedulesApi.ts` (tested helpers: recurrence
  text, wall clock ↔ instant per zone, borrow check),
  `views/room/SchedulesDialog.tsx` (list with next/last run and status,
  pause/resume, run now, edit, delete; editor with the composer's agent rows +
  "the room", channel, once/daily/weekly/cron, timezone defaulting to the
  browser's, borrowed-agent warning), header Schedules button, feed clock
  badge (`payload.scheduled` → `FeedItem.scheduledId`).
- **Tests**: DST next-run (US spring gap and fall-back overlap, Sydney,
  Kolkata, Berlin weekly), cron-lite, validation/min interval, runtime spacing
  guard; store contract + concurrent-claim race (8 claimers, memory and MySQL),
  lease expiry, edit-wins-over-completion; realtime routes/permissions/caps,
  run posts as creator with marker and agent job, re-claimed slot posts once,
  creator removed → auto-pause, missed slot skipped, one-shot disables; MySQL
  end-to-end with two schedulers racing one tick; gateway routing/permission/
  rate limit; obs route labels.

### N3

- **Owner**: the room service (it owns rooms, members, channels). Room
  creation, member add and channel creation were each reduced to one helper
  (`createRoom`, `addMember` in `room_create.go`; `createChannel`), used by
  `POST /v1/rooms`, the handoff import, the member/channel routes and template
  instantiation alike. `POST /v1/rooms` is unchanged.
- **Storage**: `room_templates` (both schema files; migration
  `2026_10_n3_room_templates`): name, description, visibility
  `private|tenant`, author, `body_json` {roomDescription, channels,
  starterPrompts, suggestedAgents [{ownerId, provider, core}], defaultMembers,
  schedules [N2 shape with a channel NAME]}, use count. Bodies are normalized
  and bounded (20 channels/prompts/agents/schedules, 50 members); template
  schedules are validated with the N2 recurrence/recipient rules.
- **Routes** (room; gateway routes `/v1/room-templates*` and
  `/v1/tenant/room-templates/*` there): `GET/POST /v1/room-templates`,
  `GET/PATCH/DELETE /v1/room-templates/{id}` (author-only edit/delete),
  `DELETE /v1/tenant/room-templates/{id}` (org admin, manage_org at the
  gateway, tenant-visible only — the prompt-library moderation pattern),
  `POST /v1/room-templates/from-room/{roomId}` (room owner/admin; the server
  captures description, channels, members, schedules; the member picks starter
  prompts and suggested agents), `POST /v1/room-templates/{id}/instantiate`
  {roomId, name, visibility} — PermCreateRoom and room-creation rate limit.
  (`from-room/{x}` and `{id}/instantiate` overlap as mux patterns, so one
  two-segment POST dispatches on the literal.)
- **Instantiate**: creates the room via `createRoom` (caller = owner), the
  channels, default members via `addMember` (normal policy; a member that
  cannot be added is reported in `failedMembers`, not fatal), and the schedules
  DISABLED and owned by the caller (nothing posts until they enable one). Any
  later failure deletes the new room. Suggested agents are returned as hints
  only: their owners are not added and no grant is touched (the room service
  has no grant path).
- **Web**: Library page Prompts | Templates switch
  (`views/library/TemplatesTab.tsx`, `TemplateDialogs.tsx`: list, editor,
  create-room-from-template dialog); Plaza "New room" → "From template…";
  room header "Save as template" (owner/admin; `SaveTemplateDialog.tsx`);
  `TemplateStartCard.tsx` after instantiation: starter prompts into the
  composer, "Invite <owner>" for non-member agent owners, and for each agent
  the creator does not own a note that its owner must authorize them (the
  handshake icon) plus an optional draft @-mention — never an automatic
  request or grant. The instantiate result is kept in localStorage per room
  until dismissed.
- **Tests**: store contract (memory + MySQL; visibility, author-only edit,
  moderator deletes tenant-visible only); room routes: authorization,
  normalization/validation, instantiate creates room + channels + members +
  disabled schedules in the right channel, duplicate room id 409, suggested
  agents never added/authorized, from-room capture (admin yes, member no);
  gateway routing/permissions; web `roomTemplates.test.ts` (follow-ups never
  authorize, summary, start-card storage).
- **Known gap**: there is no borrower-side "request authorization" API (the
  quota request flow covers more runs on an existing grant only), so the
  start card guides the member to ask the owner instead.

## Revision: personal bots run in the desktop client (2026-10-08)

Problem: the N1/F5 design had the server-side notifier hold one websocket per
member's personal bot (capped at 200, LRU) and stored every member's bot
secret server-side. At 5000 members that cannot work, and the secrets never
needed to leave the member's machine.

Decision:
- Organization default bot stays server-side (one connection; works while the
  member is offline).
- A personal bot is a private plugin sidecar on the member's desktop. Its
  credentials are local settings (never sent to bnbloc). bnbloc routes a
  member's notifications to their desktop over the per-member plugin event
  stream (G1) via a new `notification.v1` hook; the sidecar sends them with the
  personal bot and listens on its websocket for replies, which it posts as the
  member through a local desktop host API (normal chat path, no reply token).
- Routing: a member with a connected desktop running a notification plugin
  gets it there; otherwise the server notifier (org default bot) delivers.
- Removed: server-side member notifier settings (schema, encryption,
  `memberSettings`), and in the private notifier the personal-bot listeners
  and their opt-in switch.

Status: landed (public + private components; desktop needs the next build).

### Implementation notes

- **Hook.** `notification.v1` joins the code-owned registry
  (`pkgmeta.NotificationHook`, event `notification`, no room event has that
  kind). Envelope payload `wire.PluginNotification`: `{id, type, userId,
  roomId, roomName, channelId, seq, actor, title, text, url, atMs, reply?:
  {roomId, channelId, to: room|agent, agent?: {ownerId, provider, core}}}` —
  the notification for that member only.
- **Routing** (`internal/notify/emitter.go`, `services/realtime/plugin_hooks.go`).
  Prefs, mutes and collapse are decided first (unchanged `filter`). Then
  `Emitter.Desktop` (`notify.DesktopRoute`, implemented by
  `PluginHookDispatcher`, wired in realtime's main) splits recipients: a
  member with a plugin stream in the event's tenant (`HasDesktop`) is handed
  to `DeliverNotification` and left out of every server notifier item;
  everyone else is enqueued as before. The dispatcher's fan-out worker
  offers the envelope to each of that member's streams (session tenant =
  event tenant, so a reply posts with a session of that tenant) for every
  entitled plugin declaring the hook (G1 entitlement: rollout, both tenant
  scopes, opt-in install). A `deliveryGroup` collects the outcomes (held
  open by the fan-out itself so an early ack cannot fire early): any
  `delivered` ack suppresses the fallback; otherwise the last outcome
  (`failed` ack, 60 s expiry, buffer/pending-table drop, nothing to offer,
  queue full) runs it once on its own goroutine. The fallback enqueues the
  server delivery for that member under `notify.FallbackRowID(eventID,
  member)` (the event's own row already holds the other recipients) with
  the payload's event id; the worker now always sends the payload id as
  `X-Bnbloc-Event-Id`. Browser push is unchanged (queued for every
  subscribed recipient). The early "nothing visible" return also considers
  a desktop route.
- **Scope limits (by design, documented).** Only realtime holds plugin
  streams, so borrow requests (shareagent) and admin alerts (gateway) use
  the server notifiers only. The fallback is in-memory: a realtime restart
  while an ack is pending loses that fallback.
- **Host API** (`apps/desktop/src-tauri/src/internal_ext/plugin_host.rs`).
  hyper http1 server on `127.0.0.1:0`, started when the first plugin with
  `permissions.host` launches; `POST /v1/chat` → gateway
  `POST /v1/rooms/{roomId}/chat` with the desktop's session; token per
  launch (32 random bytes, constant-time compare, revoked on exit),
  per-plugin token bucket (burst 10, 1/s), 16 KB text, `clientMsgId` =
  `plg_` + 48 hex of sha256(plugin, id). New Cargo deps are server features
  of crates already in the tree (hyper `server`/`http1`, hyper-util `tokio`,
  http-body-util, getrandom; adds only `httpdate`).
- **Local settings** (`internal_ext/plugin_settings.rs`): no keychain crate
  is in the tree, so values are a 0600 JSON object per plugin under the app
  config dir (`plugin-settings/<plugin>.json`, temp + rename), passed by
  path in `BNBLOC_PLUGIN_SETTINGS_FILE` (a file, not stdin: the supervised
  stdin pipe is the stop signal). Tauri commands `plugin_local_settings`,
  `plugin_local_settings_save` (empty secret keeps the stored value; unknown
  keys and missing required fields refused), `plugin_local_settings_clear`.
  A plugin missing required settings is not launched (status "needs its
  settings"); save/clear restart it; uninstall deletes the file. The member
  catalog (`GET /v1/components`) now carries `permissions` and
  `localSettings` for installed components. Web: Settings → Notifications →
  "Personal bot" (desktop forms; a browser shows that personal bots run in
  the desktop app); Delivery lists the org notifiers (opt-in toggle, Send
  test).
- **Removed (clean cutover).** `pkgmeta.MemberSetting`/`memberSettings`
  (notifier packages now reject the field; the schema validation moved to
  plugin `localSettings`), `internal/notify/membersettings.go` (Sealer,
  HKDF/AES), `store.MemberNotifierSettingsStore` + memory/MySQL code,
  `member_notifier_settings` in both schema files, the
  `2026_10_member_notifier_settings` migration (its table is dropped by the
  new `2026_10_drop_member_notifier_settings` on deployed databases),
  `PUT/DELETE /v1/my/notifiers/{name}/settings`, `recipients[].settings` in
  notifier payloads, `wire.MemberNotifierField`/`MemberNotifierSettingsUpdate`,
  the uninstall/member-removal cleanup of member settings, and the web
  personal-bot form on notifiers. Kept: `GET /v1/my/notifiers` (now
  `{name, description, optIn, installed}`) and `/test` (test the
  organization's delivery for yourself), the org notifier's signing secret
  and reply seam.
- **Tests.** pkgmeta (hook, host permission, localSettings, notifier
  refusals); notify (`TestEmitterRoutesDesktopRecipientsWithFallback`,
  member notifier list/test); realtime (`plugin_notifications_test.go`:
  routing to the member's desktop only, failed ack / expiry / no stream
  fallback, two desktops, opt-in/rollout, end-to-end mention → plugin and
  not the notifier, fallback delivery with the same event id); gateway
  (member catalog carries host permissions and local settings, route table);
  desktop Rust (host API auth/permission/limits/forwarding/passthrough, rate
  limit, settings merge/views/0600 file, catalog → launch metadata); web
  (package import of localSettings/host, local settings form helpers).
