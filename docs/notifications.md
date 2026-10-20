# Member notifications and notifier components

bnbloc tells members about room activity that needs them — a mention, the
outcome of an agent request they made, a borrow request for their agent — by
posting a signed JSON request to every **notifier component** installed on the
platform. A notifier is an endpoint component (like an auth-provider): an
externally supervised service that turns the request into a message for the
recipients (a chat bot, a mail relay, a paging tool). bnbloc ships no
messenger integration itself: the notifier contract is the boundary.

A notifier is the organization's delivery (whatever the service is
configured with, e.g. an organization bot). A member may instead be reached
through their **own** bot: that is a plugin running in their desktop app
(the `notification.v1` hook, [Personal bots](#personal-bots-desktop-notification-plugins)),
whose credentials never leave their machine. While their desktop runs one,
their notifications go there; otherwise, or when it fails, the notifiers
deliver them.

## What produces a notification

| Type | When | Recipients | Actor |
| --- | --- | --- | --- |
| `mention` | A chat message @-mentions a room member (`@<memberId>`, as the composer inserts it). Agent handles (`@owner/provider`) are not member mentions. | The mentioned members of the room. | The author. |
| `agent_finished` | An agent job reaches `replied`. `text` starts with a plain-text summary of the agent's reply (below), then the request. | The member who addressed the agent (the job's requester). | `owner/provider`. |
| `agent_failed` | An agent job ends `failed` or `timed_out` (not a cancel). | The job's requester. | `owner/provider`. |
| `borrow_request` | A borrower asks an owner for more runs (borrow quota request), or a borrower's message to an owner's agent fails because the owner has not authorized borrowing in that room. | The agent owner. | The borrower. |
| `test` | A super-admin or a member presses "Send test". | The caller only. | The caller. |
| `admin_alert` | A super-admin (or an unattended job with a super-admin API token, e.g. the nightly smoke) sends an operational alert ([Admin alerts](#admin-alerts)). No room. | Exactly the listed recipients; default every platform super-admin. | The sender. |
| `budget_warning` | A monthly usage budget crosses its warning threshold, then 100% — each once per month (R12, `docs/prd/2026-10-08-batch-3-reliability-and-scale.md`). No room; `url` opens Settings (member budget) or Team (organization budget) when `BNBLOC_PUBLIC_URL` is set on activity. | The member (member budget) or the organization's org admins (organization budget). | None. |
| `content_shared` | An author shares an expert, skill pack or prompt pack with a member, or moves their copy to a newer version (`docs/prd/2026-10-12-experts-and-teams.md`, step 4); also a member who joins a room with a standing room share and gets a copy then (realtime, "Gap closure" 2). One version notifies a member once. No room; `url` opens the Library. | Each recipient whose copy was created or updated. | The author. |
| `content_review` | An author asks to publish a content component to the organization, or saves a new version of one the organization uses (the request opens by itself; the admins are notified only when no request was pending); an org admin approves or declines it. No room; `url` opens the Organization console (request) or the Library (decision). | The organization's org admins (request); the author (decision). | The author (request); the deciding admin (decision). |
| `app_notice` | An interactive app's page calls `app.notify` (`docs/prd/2026-10-15-app-identity-schedules-notify-ai.md` §5). The title is the app's title, then the title the app gave; `url` opens the app in its room unless the app gave a link on this deployment. At most 30 an hour per app and 10 per sender; the same notice within 10 minutes is one. | The room's members (`to: "room"`), the logins the app's owners name that belong to the organization, or a group chat connected to the room (`to: "group:<name>"`, owners only; below). | The member who sent it (never notified). |

Rules applied before anything is queued (`admin_alert` and `budget_warning`
are operational and skip the first three: they reach exactly their
recipients, including the sender when listed, whatever their preferences or
mutes):

- the actor is never notified about their own action;
- each member's preferences (below) and muted rooms are honoured;
- bursts collapse: a member gets at most one notification of a type per room
  within a window (30 s for mentions and agent outcomes, 5 min for borrow
  requests);
- nothing is queued while no notifier is visible to the event's tenant, no
  recipient has a browser push subscription ([Browser push](#browser-push))
  and no desktop notification plugin is distributed to the tenant.

Only then is each remaining recipient routed: to their desktop notification
plugin when they run one, else to the notifiers
([Routing](#routing-desktop-plugin-or-server-notifiers)). Every type is
routed the same way, whichever service produced it.

Detection runs off the chat publish path (a bounded hand-off queue in
realtime; a full queue drops observations rather than slowing chat).

## Admin alerts

`POST /v1/platform/alerts` (super-admin; `bnbloc alert`):

```json
{ "title": "bnbloc smoke failed on https://bnbloc.acme.example",
  "text": "FAIL chat echo: no chat_message echo within 30s",
  "url": "https://bnbloc.acme.example",
  "recipients": ["ops@acme.example"] }
```

- `title` is required (≤ 200 characters), `text` ≤ 500, `url` an absolute
  http(s) URL (the alert's link; omitted, the payload carries no `url`),
  `recipients` at most 100
  member ids. Omitted `recipients` = every entry of the gateway's
  `BNBLOC_SUPER_ADMINS`, used as-is as `userId` (configure it with the ids
  the notifier can map, typically emails).
- The alert is queued in the tenant the request acts in (the caller's
  session tenant, or the acting tenant they switched to) like every
  notification: through every notifier visible to that tenant, to a
  recipient's desktop notification plugin when they run one
  ([Routing](#routing-desktop-plugin-or-server-notifiers)), and to their
  browser push subscriptions. Component distribution still applies (tenant
  scope, rollout `off`, and an opt-in notifier reaches only recipients who
  opted in); member preferences, muted rooms and burst collapsing do not —
  an operator cannot mute an operational alert.
- `202 {id, recipients, notifiers}` once queued; `notifiers` lists the
  notifier components plus `builtin:desktop` (a notification plugin is
  distributed to the tenant) and `builtin:web-push` (a recipient has a
  browser subscription) when they apply. Delivery and retries are the
  outbox's (below). `409` when none of these reaches any recipient, `400`
  for an invalid body or no recipients at all, `503` when the gateway has no
  notification store. Each accepted alert is recorded in the extension audit
  trail (kind `admin-alert`).
- The payload is the usual request with `type: "admin_alert"`, no `roomId`
  and `title` without a room suffix. A notifier that renders by type should
  treat it as high priority.

## Which notifiers receive an event

Every installed notifier component whose distribution reaches the event's
tenant, with the same rules as every component:

- **tenant scope** (`PUT /v1/platform/components/{name}/tenants`): a notifier
  scoped to other tenants receives nothing from this one;
- **rollout** `off`: nothing is delivered; `auto`: every recipient;
  `optin`: only recipients who turned the notifier on (a `component_installs`
  row — `POST /v1/components/{name}/install?kind=notifier`, offered in
  Settings → Notifications). An event whose recipients did not opt in is not
  queued for that notifier.

Each (event, notifier) pair is its own outbox item with its own retries.

## Installing a notifier (super-admin)

A notifier package:

```json
{
  "kind": "bnbloc.notifier.package/v1",
  "name": "corp-notify",
  "version": "0.1.0",
  "publisher": "acme",
  "runtime": "endpoint",
  "contract": "bnbloc.notifier.contract/v1",
  "endpoint": { "baseUrl": "http://notify.internal:8097/notifier" },
  "permissions": { "network": ["notify.internal"] }
}
```

- `runtime` must be `endpoint` (no artifacts: bnbloc never downloads or runs
  the service), `endpoint.baseUrl` is required and may carry a path prefix,
  and its host must be in `permissions.network`. Every request bnbloc makes to
  the notifier is held to that allowlist (`extgov.NetGuard`), and the
  connection is refused at connect time when the resolved address is
  link-local or a cloud-metadata address.
- A notifier has no per-member settings: a member's own delivery is a
  desktop plugin (member-scope `settings` on the plugin package), so no
  member credential is ever stored or sent server-side.

**Groups.** A notifier that can post into group chats declares
`"capabilities": ["groups"]` in its package. The room's members connect
a group chat to the room: they ask for a one-time code (`POST
/v1/rooms/{room}/groups/connect {name}`, 10 minutes) and post it in the
chat; the notifier's bot sees it and reports it, signed like a reply:

```http
POST /v1/notifier/group-binds
X-Bnbloc-Component: corp-notify
X-Bnbloc-Signature: t=…,v1=…

{"code": "bnbloc-7K3QXMPA", "address": "<the notifier's own id of the chat>"}
```

bnbloc answers `{roomName, name}` (for the bot's confirmation in the chat),
404 for an unknown or expired code. A notification for that group (an
`app_notice` sent `to: "group:<name>"`) then goes to that notifier only,
as one payload whose `group` is the name, `groupAddress` the address it
reported, and `recipients` empty; members' preferences do not apply (the
sender's rate limits and the event id do). The notifier needs no
configuration of groups. A failure is `{userId: "", ok: false, error,
retryable}` (retryable retries the whole notification). An unconnected
name is refused (`groups_unavailable`).

Install it like any component: Platform → Components (upload the package),
or `bnbloc cores install --package corp-notify.json --signature
corp-notify.json.sig`. The gateway probes `GET <baseUrl>/discover` (must
answer 200 `{name, contract: "bnbloc.notifier.contract/v1"}`) before the
record is saved; realtime activates it on its next reconcile (at most 30 s,
no restart).

**Signing secret.** The notifier's signing secret is its component
credential (every endpoint component beside the gateway holds one:
[architecture/endpoint-components.md](./architecture/endpoint-components.md)).
The first install generates it server-side and returns it **once** (the
install response's `secret`; the web shows it with a copy button, the CLI
prints it). Configure it on the notifier. It is stored in platform settings
sealed with `BNBLOC_SECRET_STORE_KEY` (the gateway creates and verifies it,
realtime opens it to sign deliveries, so both need the key; without it on
the gateway a notifier install is refused), never in the package and never
in any list response. A secret stored before credentials were sealed (in
clear) keeps working until the first rotation replaces it.

- `POST /v1/platform/components/{name}/secret/rotate?kind=notifier`
  (super-admin) replaces it and returns the new one once
  (`bnbloc components rotate-secret <name>`; "Rotate secret" in the web).
  The receiver accepts a comma list of secrets: add the new one beside the
  old, then remove the old.
- The same secret signs the notifier's own calls to the gateway: its replies
  (below) and its ask for its settings (`GET /v1/component-settings`): a
  notifier's settings (its messenger's API origin and keys, its tenant
  allowlist) are declared in its package and set in Platform → Components →
  Settings, and the notifier reads them itself.
- `POST /v1/platform/components/{name}/test?kind=notifier` (super-admin)
  sends one signed `test` notification to the caller through the notifier's
  default delivery and answers `{delivered, status, error, eventId, results}`
  (`bnbloc components test <name>`; "Send test notification" in the web).
- Reinstalling (an upgrade) keeps the secret. Uninstalling deletes the secret
  and the notifier's reply tokens.

## Member preferences

`GET /v1/my/notification-prefs` / `PUT /v1/my/notification-prefs` (any member,
own settings; `PUT` replaces the whole object):

```json
{
  "mentions": true,
  "agentFinished": true,
  "agentFailed": true,
  "borrowRequests": true,
  "contentShared": true,
  "contentReview": true,
  "appNotices": true,
  "quietRooms": ["room-a"]
}
```

One switch per type a member can turn off: `mentions`, `agentFinished`,
`agentFailed`, `borrowRequests` (`borrow_request`), `contentShared`
(`content_shared`), `contentReview` (`content_review`, the requests an
org admin reviews and the decisions on the member's own requests) and
`appNotices` (`app_notice`).
A member who never saved preferences has everything on and no quiet rooms
(the stored columns default on, so prefs saved before a switch existed
keep it on).
`quietRooms` (max 500) mutes every type for those rooms; the web client
manages it from the room header's bell button.

## The member's notifier list

Settings → Notifications → Delivery lists every notifier visible to the
member (`GET /v1/my/notifiers` → `{notifiers: [{name, description, optIn,
installed}]}`): an opt-in notifier can be turned on or off there
(`POST`/`DELETE /v1/components/{name}/install?kind=notifier`), and
`POST /v1/my/notifiers/{name}/test` sends one `test` notification to the
caller only through that notifier and returns its answer
(`{delivered, status, error, eventId, results}`) — the member sees at once
whether the organization's delivery reaches them. Both routes are personal
(a viewer may use them) and served by realtime.

## Personal bots (desktop notification plugins)

A member's own delivery — e.g. their own messenger bot — is a **plugin
package** that runs as a sidecar in their desktop app, not a server-side
setting. Its credentials are member-scope settings: they stay on the
member's desktop and never reach bnbloc's servers, and bnbloc keeps one
connection per desktop, not one per member on the server.

### The plugin package

```json
{
  "kind": "bnbloc.plugin.package/v1",
  "name": "personal-bot",
  "version": "0.1.0",
  "publisher": "acme",
  "runtime": "sidecar",
  "contract": "bnbloc.plugin.contract/v1",
  "hooks": ["notification.v1"],
  "sidecar": {
    "localUrl": "http://127.0.0.1:7193",
    "listenEnv": "PERSONAL_BOT_LISTEN",
    "supervisedEnv": "PERSONAL_BOT_SUPERVISED"
  },
  "artifacts": { "darwin-arm64": { "url": "https://<gateway>/v1/artifacts/sha256/…", "sha256": "…" } },
  "permissions": { "network": ["127.0.0.1"], "host": ["chat"] },
  "settings": [
    { "key": "apiBase", "label": "Bot API URL", "type": "url", "scope": "platform",
      "required": true, "deliver": { "env": "BNBLOC_PERSONAL_BOT_API_BASE" } },
    { "key": "appId", "label": "App ID", "type": "text", "scope": "member", "required": true },
    { "key": "appKey", "label": "App key", "type": "secret", "scope": "member", "required": true,
      "help": "Stored on this computer only." }
  ]
}
```

- `hooks: ["notification.v1"]` subscribes to member notifications (the hook
  registry is code-owned: `core/pkgmeta`).
- `permissions.host` (plugin packages only; the only capability is `chat`)
  lets the sidecar post chat as the member through the desktop host API
  (below). Without it the sidecar gets no host URL or token.
- `settings` are the plugin's component settings
  (docs/prd/2026-10-19-component-settings.md; `core/pkgmeta/settings.go`):
  member-scope ones are the form the member fills in on their desktop
  (the Components page); a `secret` is never shown back. Deployment data
  the plugin needs (e.g. its service's API origin) is a platform-scope
  setting the super admin sets (Platform console → Components → Settings),
  or an organization one an org admin sets, never baked into `sidecar.args`
  or hard-coded in the plugin. A setting's `deliver.env` may use the
  plugin's own `BNBLOC_<NAME>_` prefix.

Install, rollout (`auto` / `optin` / `off`) and tenant scope are those of
every component; an opt-in plugin runs only for members who turned it on.

### Routing: desktop plugin or server notifiers

Every notification type takes this path, whichever service produced it
(mentions and agent outcomes in realtime, borrow requests in shareagent,
admin alerts in the gateway): the producer only queues, and realtime — which
holds the members' plugin streams — routes.

While a plugin declaring `notification.v1` is distributed to the event's
tenant (rollout not `off`, tenant in scope), the event is queued **once on
the built-in desktop channel** (outbox notifier `builtin:desktop`, a name no
component can have) with every recipient the preference, mute and collapse
rules kept. Otherwise it is queued directly to the server notifiers, as if
nobody ran a plugin. Realtime's outbox worker then routes each recipient:

1. A member whose desktop holds the plugin event stream as a plugin
   desktop (`GET /v1/my/plugin-events?plugins=1`, sent while it runs a
   plugin sidecar; session in the event's tenant) gets the
   notification there: realtime hands it to every entitled plugin on that
   member's streams that declares `notification.v1` (rollout not `off`,
   tenant scope admits the event's tenant and the session's, opt-in only
   when the member installed it — the [G1 entitlement](./bugs/2026-10-08-plugin-hook-delivery-unreachable.md)).
   That member is **not** included in the server notifier items.
2. **Fallback.** If no plugin takes it — none entitled or running on that
   desktop, dropped (bounded buffers), acknowledged `failed`, or not
   acknowledged within 60 s — the server delivery is queued for that member
   (every visible notifier, opt-in rules as usual) with the same event id.
   One successful plugin delivery (on any of the member's desktops) is
   enough.
3. Everyone else (no plugin stream when the worker routes the item) goes to
   the server notifiers under the event id. Browser push is a device
   channel of its own and is queued for every subscribed recipient either
   way.

**Durability.** The routing is recorded on the desktop channel's outbox
item before anything is sent, and each member's outcome is recorded as it
arrives, so the pending set survives a realtime restart: an item whose
outcomes did not all arrive within 2 minutes is claimed again and falls
back for the members still pending, once (every server item id is derived
from the event and member, and queueing is idempotent). A member whose
plugin delivered just before a restart may be notified by both channels;
nobody is left without a notification.

### What the plugin receives

The desktop posts each delivery to the sidecar's `POST {localUrl}/events`
(10 s timeout) and acks the outcome; the plugin answers 2xx **only once it
delivered the notification** (any other answer or a timeout triggers the
fallback):

```json
{
  "event": "notification", "version": "1", "tenantId": "acme", "roomId": "design",
  "payload": {
    "id": "ntf_3f2a…", "type": "agent_finished", "userId": "alice@acme.example",
    "roomId": "design", "roomName": "Design", "channelId": "ch_1", "seq": 42,
    "actor": "carol@acme.example/codex",
    "title": "carol@acme.example/codex finished your request in Design",
    "text": "…", "url": "https://bnbloc.acme.example/#/room/design?seq=42&channel=ch_1",
    "atMs": 1791431276123,
    "reply": { "roomId": "design", "channelId": "ch_1", "to": "agent",
               "agent": { "ownerId": "carol@acme.example", "provider": "codex", "core": "local" } }
  }
}
```

The payload is the notification for that member only (`userId`; the same
title, text and deep link the notifiers get). `reply` is present when the
member may answer (`mention`: `to: "room"`; `agent_finished`/`agent_failed`:
`to: "agent"` with the agent the request addressed); `seq`, `channelId`,
`url` and `roomId` are omitted when not applicable. Wire type:
`wire.PluginNotification` (`internal/wire/plugins.go`).

### Launch environment

| Variable | When | Meaning |
| --- | --- | --- |
| `<sidecar.listenEnv>` / `<sidecar.supervisedEnv>` | as declared | As for every sidecar (listen address; `1` = exit when stdin closes). |
| `BNBLOC_PLUGIN_HOST_URL` | `permissions.host` declared | The desktop host API, `http://127.0.0.1:<random port>`. |
| `BNBLOC_PLUGIN_HOST_TOKEN` | `permissions.host` declared | Bearer token for it: random per launch, revoked when the process exits. |
| `BNBLOC_COMPONENT_SETTINGS_FILE` | `settings` declared | Path of an owner-only (0600) JSON object `{key: value}` with every value of its settings (platform and organization values read from `GET /v1/components/{name}/settings/resolved`, the member's from this computer; a default where none is set), rewritten at every launch. |
| `<settings[].deliver.env>` | as declared | A setting's value, when it has one. |

The desktop does not start a plugin whose required settings are not all set,
in any scope (its status and the Components page say which; its
notifications fall back to the server notifiers). Saving the member's
values restarts the sidecar, and so does a change of the platform or
organization values (the catalog says which are set); an uninstalled
plugin's files are deleted (every catalog refresh sweeps the files of
plugins no longer installed in that member's tenant). The member's values
are stored per gateway, member and tenant under the app config dir
(`members/<hash of gateway origin, subject and tenant>/component-settings/plugin@<plugin>.json`),
so another member signing in on the same desktop never gets them and a
tenant switch never deletes them; a member or tenant switch restarts the
plugin with the new scope's file.

### The desktop host API

`POST {BNBLOC_PLUGIN_HOST_URL}/v1/chat` with `Authorization: Bearer
<BNBLOC_PLUGIN_HOST_TOKEN>` (compared in constant time) and

```json
{ "roomId": "design", "channelId": "ch_1", "text": "Also add a regression test",
  "recipient": { "ownerId": "carol@acme.example", "provider": "codex", "core": "local" },
  "clientMsgId": "<the plugin's stable id of the reply, e.g. the messenger message id>" }
```

posts the text as the member through the gateway's normal chat route
(`POST /v1/rooms/{roomId}/chat`) with the desktop's own session — room
membership, the member's chat rate limit, agent jobs and borrow rules are
the chat path's. Omit `recipient` for room chat (a mention reply).
`clientMsgId` (required, at most 200 bytes) is namespaced per plugin by the
desktop, so a retry returns the original sequence and never posts twice.
Answer `200 {"sequence": 43}`.

| Status | Meaning |
| --- | --- |
| `400` | Malformed body, empty text, missing `roomId`/`clientMsgId`, incomplete `recipient`. |
| `401` | Missing or wrong token. |
| `403` | The package does not declare `permissions.host: ["chat"]`, or the gateway refused (e.g. no longer a member of the room). |
| `404` / `405` | Unknown route / method. |
| `413` | Text over 16 KB (the chat limit). |
| `429` | The plugin's local limit (burst 10, one per second) or the member's chat rate limit. |
| `502` | Gateway unreachable or failing. |
| `503` | No member session on this desktop (signed out, or the session expired). |

No reply token is involved: the plugin acts with the member's own desktop
session, so only the org notifier uses the signed reply seam
([Replies](#replies-from-the-receiving-system)).

## The request

```
POST <baseUrl>/v1/notifications
Content-Type: application/json
User-Agent: bnbloc-notifications/1
X-Bnbloc-Event-Id: ntf_3f2a…
X-Bnbloc-Signature: t=1791431276,v1=5d0c…
```

```json
{
  "id": "ntf_3f2a9c0e8b7d6a5f4e3d2c1b0a998877",
  "type": "agent_finished",
  "tenantId": "acme",
  "roomId": "design",
  "roomName": "Design",
  "recipients": [
    { "userId": "alice@acme.example", "replyToken": "bnr_9c1e…" },
    { "userId": "bob@acme.example", "replyToken": "bnr_47d0…" }
  ],
  "actor": "carol@acme.example/codex",
  "title": "carol@acme.example/codex finished your request in Design",
  "text": "Request: fix the flaky build",
  "url": "https://bnbloc.acme.example/#/room/design",
  "atMs": 1791431276123,
  "reply": { "channelId": "ch_1", "recipient": { "ownerId": "carol@acme.example", "provider": "codex", "core": "local" } }
}
```

- `reply` and `recipients[].replyToken` are present when the notification
  accepts a reply (see [Replies](#replies-from-the-receiving-system)).

- `recipients[].userId` are bnbloc member ids (the verified subject the
  member signs in with — typically the IdP email). Mapping them to the
  receiving system's user ids is the notifier's job.
- **Transport:** the body carries reply tokens (credentials for posting as
  the member), so the notifier endpoint must be on the deployment's private
  network (or behind TLS); the package's `permissions.network` already
  restricts where realtime may send.
- `text` is at most 500 characters, `title` at most 200; `url` is omitted when
  the deployment has no `BNBLOC_PUBLIC_URL`.
- **Deep links.** `url` points at the message the notification is about:
  `<origin>/#/room/<roomId>?seq=<n>[&channel=<channelId>]` — the mentioning
  message, the agent's reply (`agent_finished`), or the request a failed job
  answered (`agent_failed`, `borrow_request`). The web client opens the room,
  switches to the channel, pages history until the message is loaded, then
  scrolls to and highlights it (the global-search jump). A message in the
  room's people chat (the agent-invisible side conversation) links the same
  way with the people chat's channel id — `?seq=<n>&channel=<peopleChannelId>`
  — and the client opens the people-chat panel at the message instead of
  switching the main feed. Mentions there notify like any mention. Without a
  message it is `<origin>/#/room/<roomId>`.
- **Reply summary.** `agent_finished` `text` is `<summary>\n\nRequest: <request>`.
  The summary is the agent's final reply message of that turn rendered as
  plain text — markdown markup dropped, link and image text kept, fenced
  code blocks elided as `[code]`, whitespace collapsed — cut at 280
  characters. Realtime pairs the reply with its job through the reply's
  agent session (the job's `diagnostics.sessionId`); the bridge posts the
  reply before reporting the job replied, and a job whose reply has not been
  seen waits up to 20 s for it before it is sent without a summary (the link
  then points at the request). This never delays the chat path.
- `id` is stable for one logical event (the same for every notifier) and
  repeated in `X-Bnbloc-Event-Id`: retries carry the same id. De-duplicate on
  `(id, userId)`.

### Verifying the signature

`v1 = hex(HMAC-SHA256(secret, t + "." + rawBody))`, where `t` is the unix time
in seconds from the header and `secret` is the notifier's signing secret. A
receiver must:

1. compute the HMAC over the **raw** body bytes (before JSON parsing) and
   compare in constant time;
2. reject a `t` more than 5 minutes from its own clock (replay window);
3. skip a `(id, userId)` it has already delivered.

```go
mac := hmac.New(sha256.New, []byte(secret))
mac.Write([]byte(t + "."))
mac.Write(body)
ok := hmac.Equal(mac.Sum(nil), mustHex(v1))
```

### Responses and retries

A `2xx` accepts the request. Its body may report per-recipient outcomes:

```json
{ "results": [
  { "userId": "alice@acme.example", "ok": true },
  { "userId": "bob@acme.example", "ok": false, "error": "bot API 503", "retryable": true },
  { "userId": "dave@acme.example", "ok": false, "error": "no bot for recipient" }
] }
```

- recipients with `retryable: true` are retried — the same event id is
  re-sent with `recipients` narrowed to them;
- `ok: false` without `retryable` is final (recorded in the outbox item's
  last error and the audit trail);
- a `2xx` without `results` (or a recipient missing from it) counts as
  delivered.

Anything other than `2xx` — including a redirect (never followed) or a
timeout (10 s) — retries the whole item. Retries back off exponentially (10 s
doubling, ±20 % jitter, capped at 30 min) for up to 8 attempts, then give up.
Items whose notifier was uninstalled, switched off, or re-scoped away from
the tenant meanwhile are given up immediately. Delivered and given-up items
are pruned after 7 days.

## Replies from the receiving system

A member can answer a notification where they received it (e.g. replying
to the bot message in a messenger); the notifier posts the answer back and
bnbloc publishes it into the room **as that member**, through the normal
chat path (realtime publish, so bridges claim it like any typed message).

**Which notifications accept a reply.** `mention`, `agent_finished` and
`agent_failed` carry `reply` (where the answer goes) and one `replyToken` per
recipient. `borrow_request`, `test` and admin alerts carry neither.

| Type | The reply is posted… |
| --- | --- |
| `mention` | to the room channel as human chat (`recipient=room`: no agent is driven). |
| `agent_finished`, `agent_failed` | to the same agent the member's request addressed — `reply.recipient` `{ownerId, provider, core}` — so it continues that agent conversation. Borrowing rules apply unchanged: when the agent belongs to another member, the bridge enforces that owner's room authorization exactly as for a typed message. |

**Reply tokens.** Opaque (`bnr_` + 64 hex), minted by realtime per
recipient on every delivery attempt, valid **7 days**, usable for any number
of replies until then. bnbloc stores only their SHA-256 (table
`notification_reply_tokens`: tenant, notifier, event id, room, channel,
member, reply addressing, expiry); an expired token is refused and pruned
by the delivery worker; uninstalling the notifier deletes its tokens. A
token is bound to the notifier it was delivered through. Treat it like a
credential: never log it, keep it server-side (it is not needed in the
message the member sees).

### Posting a reply

```
POST <gateway>/v1/notifier/replies
Content-Type: application/json
X-Bnbloc-Component: corp-notify
X-Bnbloc-Signature: t=1791431276,v1=5d0c…
```

```json
{ "replyToken": "bnr_9c1e…", "text": "Also add a regression test", "idempotencyKey": "<the receiving system's message id>" }
```

- **Authentication** is the notifier's signing secret, not a session: the
  same `X-Bnbloc-Signature` scheme as deliveries (`HMAC-SHA256(secret, t +
  "." + rawBody)`, 5-minute window), computed over the reply body, with the
  component named in `X-Bnbloc-Component`. During a secret rotation sign
  with the newest secret bnbloc has stored.
- **Idempotency.** `idempotencyKey` (required, at most 200 bytes) is the
  receiving system's id of the reply message. It becomes the chat
  `clientMsgId` (derived from notifier + member + key), so a retry returns
  the original sequence and never posts twice.
- **Answer.** `200 {"roomId":"design","sequence":42}`.

| Status | Meaning |
| --- | --- |
| `400` | Malformed JSON, missing `idempotencyKey`, empty text, or not exactly one of `replyToken` / `{ref, sender}`. |
| `401` | Missing component header, bad or stale signature, or no signing secret stored. |
| `403` | Notifier not installed, rollout `off`, tenant out of scope, opt-in not chosen by the member, tenant suspended, member no longer in the tenant (or a read-only role), or **no longer a member of the room** (checked when the reply is posted). |
| `404` | Unknown reply token (or one delivered through another notifier), or no reference registered for this `{ref, sender}`. |
| `410` | Reply token expired. |
| `413` | Text over 16 KB (the chat limit). |
| `429` | The member's chat rate limit (shared with their own typing). |

**Network.** The route is served by the gateway (the public edge), but the
notifier normally reaches it over the deployment's private network (e.g.
`http://gateway:8080`) — the same network realtime uses to deliver to it. No
session token is involved; the signature is the only credential, so keep
the signing secret on the notifier only. Every accepted or refused reply is
recorded in the extension audit trail (`kind=notifier action=reply`, member
as actor, room and event id; never the text or the token).

### Reply references (stateless notifiers)

A notifier that recognizes a reply by the message the member answered
(e.g. a quote of the bot message in a messenger) need not remember which of
its sent messages carried which token: it registers that with bnbloc right
after sending, and then any of its replicas — or the same one after a
restart — can post the reply.

```
POST <gateway>/v1/notifier/reply-refs
X-Bnbloc-Component: corp-notify
X-Bnbloc-Signature: t=1791431276,v1=…

{ "replyToken": "bnr_9c1e…", "ref": "<the sent message's id>", "recipient": "<the account it was sent to>" }
```

- `ref` and `recipient` are the receiving system's ids, opaque to bnbloc,
  at most 128 characters each, matched exactly. One message sent to several
  members is one `ref` registered once per `recipient` (each with that
  recipient's token).
- Authentication is the same as for replies. Answers: `204` registered
  (re-registering the same binding is a no-op), `400` missing or over-long
  `ref`/`recipient`, `404` unknown token or one delivered through another
  notifier, `409` this `(ref, recipient)` is already bound to another token,
  `410` token expired.
- Storage: table `notification_reply_refs` (notifier, ref, recipient →
  token hash), keyed `(notifier, ref, recipient)`. A reference expires with
  its token, is pruned with the tokens, and is deleted when the notifier is
  uninstalled.

The reply then names the reference instead of the token:

```json
{ "ref": "<the message the member answered>", "sender": "<the account that answered>", "text": "Also add a regression test", "idempotencyKey": "<the reply's message id>" }
```

bnbloc resolves the token registered for exactly that `(ref, sender)`:
**checking that the reply comes from the member the message was sent to is
bnbloc's**, not the notifier's — the notifier passes the replying account as
the receiving system identifies it, and a message forwarded to and answered
by someone else resolves to nothing (`404`). Everything after the token is
resolved — distribution, membership, rate limit, idempotency, audit — is the
same as for `replyToken`.

## Delivery internals

- Producers enqueue into `notification_outbox`, one row per (event,
  notifier), keyed `(tenant_id, id, notifier)` (re-observing an event is a
  no-op). The row holds the payload with recipient ids only. Realtime
  produces mentions and agent outcomes, shareagent borrow quota requests,
  the gateway admin alerts — all through the same emitter
  (`notify.NewEmitter`), reading the notifier and plugin install records
  (5 s snapshot) to decide targets. A desktop channel row
  (`builtin:desktop`) carries the payload plus the message anchor; the
  worker routes it (above) and the server rows it queues are the event's
  own row (members without a plugin stream) and, per member that fell
  back, `<event id>-<16 hex of sha256(member)>` (`notify.FallbackRowID`),
  whose payload keeps the event id (the `X-Bnbloc-Event-Id` header is always
  the payload's id).
- A routed desktop row stays claimed by the worker that routed it until its
  members' outcomes arrive (its `next_attempt_at_ms` is the 2-minute outcome
  deadline; it counts as pending, never as a delivery attempt); it finishes
  `delivered` with `last_error` naming the members that fell back.
- Realtime's component pipeline hosts plugins and notifiers: boot rehydrates
  the install records and a reconcile every 30 s activates new or upgraded
  notifiers (discover probe, guarded HTTP client) and drops uninstalled or
  switched-off ones. The worker delivers only through an activated notifier
  (an active record not yet activated in this process is retried).
- The worker leases due rows with a single `UPDATE … LIMIT` claim (lease =
  pushed-out `next_attempt_at_ms` plus a claim token), mints each
  recipient's reply token, signs with the notifier's secret, posts, and
  records the outcome only while it still holds the lease — safe with more
  than one worker on the same database.
- Every delivery attempt, test send and secret rotation is recorded in the
  extension audit trail (`kind=notifier`, `name=<component>`).

## Browser push

Web Push is a **built-in** notification channel (not a component): a member
turns on **Settings → Notifications → Browser notifications on this device**,
the browser asks for permission, and the web subscribes on the app's one
service worker `/push-sw.js` (served by the gateway from the web root, scope
`/`, `Cache-Control: no-cache`; the gateway sets no CSP that would restrict
it) and stores the browser's `PushSubscription`. The same worker carries the
installed app's offline shell and is registered on every browser page load
(see [mobile.md](mobile.md)). The desktop shell never registers the worker
and shows no toggle. On iPhone/iPad, push exists only in the app added to the
Home Screen (iOS 16.4+); a Safari tab shows how to install it instead.

- **Configuration.** The deployment's VAPID key pair
  (`BNBLOC_WEB_PUSH_VAPID_PUBLIC_KEY` / `_PRIVATE_KEY`, generated with
  `bnbloc keys gen-vapid`) and contact `BNBLOC_WEB_PUSH_VAPID_SUBJECT`
  (`mailto:…`; defaults to an https `BNBLOC_PUBLIC_URL`), read by realtime.
  No keys = the channel is off: `GET /v1/push/vapid-public-key` and
  `POST /v1/my/push-subscriptions` answer 404 and the web hides the toggle.
  Half a pair or a mismatched pair is fatal at realtime startup. Browsers
  only offer push on https origins (and localhost).
- **Routes** (realtime, any member, personal — a viewer may use them):
  `GET /v1/push/vapid-public-key` → `{publicKey}`;
  `POST /v1/my/push-subscriptions` with `PushSubscription.toJSON()`
  (`{endpoint, keys: {p256dh, auth}}`; https endpoint, a valid P-256 key and
  a 16-byte auth secret, else 400) → 204; `DELETE /v1/my/push-subscriptions`
  with `{endpoint}` → 204. Subscriptions are stored per member in
  `web_push_subscriptions` keyed `(tenant, sha256(endpoint))` — a browser
  re-subscribed by another member moves to them; at most 20 per member (the
  least recently refreshed make room). The web re-registers a live
  subscription whenever Settings opens; one made with a rotated VAPID key is
  replaced. Removing a member from a tenant deletes theirs there.
- **Delivery.** The emitter applies the same actor, preference, quiet-room
  and collapse rules, then queues one more outbox item per event — notifier
  `builtin:web-push` (a name no component can have) — for the recipients
  with at least one subscription. The worker encrypts the message (RFC 8291
  `aes128gcm`, VAPID RFC 8292, TTL 24 h, `Topic` per event) to every
  subscription of each recipient:
  `{id, type, roomId, title, body, url, path}` with the same title, text and
  deep link (`path` is its hash route, opened on the worker's own origin;
  the notification's `tag` is the event id, so a retried delivery replaces
  the first). 404/410 drops that subscription; 429/5xx/network errors retry
  the recipient with the outbox backoff; other answers are final for that
  device. Push endpoints are member-supplied, so realtime only connects to
  public addresses for them (or through the operator's `HTTPS_PROXY`) and
  never follows redirects. Items queued while the channel was on are given
  up once it is turned off.
- Clicking a notification focuses an open bnbloc tab and opens the deep
  link there (or opens a new tab).
- **Shared browsers.** A subscription follows the member who last turned the
  toggle on in that browser. Signing out unsubscribes the browser at its push
  service, so the next delivery answers 410 and the row is dropped.

## Operator settings

| Variable | Service | Meaning |
| --- | --- | --- |
| `BNBLOC_PUBLIC_URL` | realtime, shareagent | Public web origin for deep links. |
| `BNBLOC_WEB_PUSH_VAPID_PUBLIC_KEY`, `BNBLOC_WEB_PUSH_VAPID_PRIVATE_KEY`, `BNBLOC_WEB_PUSH_VAPID_SUBJECT` | realtime | Browser push channel (unset = off). |

See [environment-variables.md](./environment-variables.md).
