# Events a room subscribes to (2026-10-21)

Status: slice 1 built (§6). Supersedes the event part of
`docs/prd/2026-10-20-event-triggered-schedules.md` (its three platform
events become one source of this mechanism). Owner: one event bus (the
gateway records, realtime dispatches), the schedules (a subscription is a
task's trigger), webhook endpoints, component packages (declared events).

Anything can tell a room that something happened, and a room's task runs
because of it: the platform (a deploy, a publish), the room itself (a new
file, a new member, an app's data changed), a component (a repository got
a commit), or any outside system through a webhook. The room subscribes;
the platform delivers. It names no use case.

## 1. Why

- The owner, 2026-10-21: "事件机制要做成通用方案，不能只针对我们这个平台，
  而且事件也要支持用户通过 room 订阅，这样也可以自动触发".
- Slice 1 (2026-10-20) built three platform events and an `event`
  recurrence; a room's end-to-end tests then ran after every deploy. The
  same tests should run on a commit or a finished CI build, and other
  rooms want other triggers (an alert, a form submission, a file landing).

## 2. What it is (the end state)

### 2.1 One event shape

- CloudEvents 1.0 fields: `source` (who: `bnbloc/platform`,
  `bnbloc/room/<id>`, `component/<name>`, `webhook/<subscription>`),
  `type` (what, e.g. `platform.deployed`, `room.file.added`,
  `com.github.push`), `id`, `time`, `subject` (optional), `data` (JSON,
  bounded, e.g. 16 KiB).
- One validator, one size bound; `data` is always untrusted: a task sees
  it fenced as data, never as instructions.

### 2.2 Sources

| Source | Who emits | Examples |
|---|---|---|
| Platform | the gateway (slice 1) | `platform.deployed`, `component.published`, `desktop.published` |
| Room | the room service | `room.file.added`, `room.member.joined`, `room.app.data.changed` |
| Component | a component that declares `events` in its package (name, description, data schema), emitting through its credential (endpoint/sidecar) or the room relay (MCP) | a repository component's `push`, a monitor's `alert` |
| Member | a room's member (a script, a CI job with their API token, `bnbloc events send`) | "the work is done", "a build passed" |
| Webhook | an outside system POSTing to a subscription's URL | CI, GitHub, a form service |

### 2.3 Subscriptions live in the room

- A subscription is a task's trigger: the `event` recurrence gains
  `source` and `type` patterns and an optional filter on `subject` /
  top-level `data` fields (equality, prefix; no expressions).
- A webhook subscription gets its own URL and secret (shown once,
  rotatable, revocable); requests are verified by HMAC signature (or the
  provider's own scheme where a component handles it); unknown or
  unsigned requests are refused and counted.
- Set in the schedules dialog or by talking (`room_setup add_schedule`
  with the event trigger; budgets unchanged, details in `room_help`).

### 2.4 Delivery

- Each event reaches the subscriptions that match it in the tenants it
  may reach (platform events: every tenant; room events: that room;
  component events: the rooms that installed it; webhook: its room).
- A matching task runs once through the scheduler's run path (budget,
  audit, agent rules), its text plus the event fenced as data; coalesced
  per task within its minimum interval.
- **No self-trigger:** an event caused by a run of a room's own task (the
  run's job id travels on what it does) does not trigger that room's
  tasks.
- Rate limits per subscription and per source; events over the limit are
  dropped with a counted notice to the room's owner.

### 2.5 AI

A task's own words decide what an event means; the platform adds an AI
assist to draft a subscription from words ("run the tests when CI
finishes on main" → source, type, filter), and the webhook setup shows
what a sample payload would produce.

## 3. Design review

| Lens | Answer |
|---|---|
| Component or platform | Platform: the envelope, delivery, webhook verification, consent and limits. Meanings and uses are the room's and the components'. |
| One owner | One validator, one dispatcher, one subscription model (the task trigger). |
| Abuse and untrusted input | Signed webhooks, revocable secrets, size and rate bounds, data fenced, no self-trigger. |
| Boundaries | Events reach only the tenants and rooms they may; a webhook reaches its own room. |
| Load on agents | None new; `room_setup` gains fields, briefs within budget. |
| Release | Slice 1's events and recurrence keep working as one source. |
| AI capability | Subscription drafting; the task decides. |

## 4. Pre-mortem

1. **A webhook URL leaks.** Rotate/revoke; HMAC signatures; per-subscription
   limits; audit.
2. **Loops** (a task's action emits the event it follows). The self-trigger
   rule; coalescing.
3. **Storms** (CI emits per job). Filters and rate limits; counted drops.

## 5. Build order

1. The envelope, room events, webhook subscriptions, the no-self-trigger
   rule (slice 1's platform events moved onto it).
2. Component-declared events; the subscription-drafting assist.

## 6. Slice 1 as built

- **One envelope** (`internal/wire/events.go`): `wire.CloudEvent`
  (`specversion` 1.0, `id`, `source`, `type`, `time`, `subject?`, `data?`),
  one validator `wire.ValidEvent` (printable id, source and type without
  spaces, RFC 3339 time normalized to UTC, subject at most 256, data one
  JSON value at most 16 KiB once compacted). The slice-1 platform events
  are this envelope with source `bnbloc/platform` (data
  `PlatformDeployedData`, `ComponentPublishedData` with the name as
  subject, `DesktopPublishedData`); `wire.PlatformEvent`, the `events` /
  `component` recurrence fields and `POST /internal/v1/platform-events`
  are gone (no compat, pre-launch): the gateway hands `wire.EventDispatch`
  to `POST /internal/v1/events`.
- **The subscription is the trigger** (`internal/schedule/event.go`): the
  `event` recurrence is `{source, types, filter?}`; sources
  `bnbloc/platform`, `bnbloc/room` (its own room), `bnbloc/member` (its
  room's members) and `webhook` (its own URL); 1 to 8 type patterns (a
  trailing `*` is a prefix; the platform's and the room's must take a type
  of their closed lists; a member trigger never names a reserved prefix);
  the filter is `subject` (equals), `subjectPrefix` and `data` (top-level
  field = value, a string, number or boolean as its text; at most 8).
  `schedule.Triggers` is the one match (source, type, filter, no
  self-trigger).
- **One dispatcher** (`services/realtime/events.go` `dispatchEvent`), for
  platform, room, member and webhook events alike; rate limits per source
  (5/s, burst 60) and per task (1/s, burst 20), each replica's share, from
  `internal/ratelimit` (the gateway's bucket moved there, one owner); a
  drop is counted on the task (`room_schedules.events_dropped`) and the
  first since its last edit notifies the room's owner
  (`wire.NotificationScheduleNotice`, operational; `drop_noticed`).
- **Room events** (`services/realtime/room_events.go`), from realtime's
  publish tap: `room.file.added` (an `artifact_updated` with a new
  version, not a thumbnail; subject the file name or title),
  `room.member.joined` (subject the person), `room.app.data.changed`
  (coalesced per app and cause: the first change at once, the rest of each
  minute as one event with `changes`; at most 10,000 open windows a
  replica), each reaching only that room's tasks.
- **No self-trigger**: a version's `sourceJobId` now rides
  `wire.ArtifactUpdated`; an agent's app write carries its job in the
  signed app scope (`svcauth.AppScope.Job`, `X-Bnbloc-App-Job`, inside the
  scope's HMAC) to `wire.AppChanged.SourceJobID` (never relayed to an
  app's change feed); a dispatch names `CauseJob`. `causeRoom` walks the
  job and its dispatch chain to a run slot's post; that room's tasks do
  not take the event. Publishes under a job grant: today no job grant can
  reach a platform or organization publish (`errJobWidens`, the
  member's own click), so the gateway's producers name no cause yet; the
  dispatch field and realtime's rule are in place and tested for when one
  can.
- **Webhooks** (`internal/schedule/hook.go`, `services/realtime/hooks.go`,
  `services/gateway/hooks.go`): `POST/DELETE /v1/rooms/{room}/schedules/{id}/hook`
  (make or rotate by the creator, the URL kept on rotation; revoke by the
  creator, owner or admin), secret `whsec_…` shown once, sealed with
  `BNBLOC_SECRET_STORE_KEY` bound to the hook id (columns `hook_id` unique,
  `hook_secret`, `hook_created_at_ms`, `hook_refused`,
  `hook_refused_at_ms`; migration `2026_10_room_schedules_hooks`, add
  only). The open route `POST /v1/hooks/{id}` spends the client IP's
  public budget and proxies to realtime, which bounds the body (512 KiB),
  verifies `X-Bnbloc-Signature` or `X-Hub-Signature-256`
  (`sha256=<hex HMAC-SHA256 of the raw body>`), builds the event
  (`schedule.HookEvent`: type from `X-Bnbloc-Event`,
  `com.github.<X-GitHub-Event>` or the body's `type`; a CloudEvent body
  gives its own attributes; data over 16 KiB keeps the smaller top-level
  fields and names the rest in `_omitted`) and dispatches to that task
  only; refusals are counted on the hook, a request over the task's limit
  answers 429.
- **Members' events** (added on the owner's ask, 2026-10-21):
  `POST /v1/rooms/{room}/events` (`services/realtime/member_events.go`,
  `schedule.MemberEventOf`): a member who may chat (never a visitor),
  with a session or an API token; body `{type, subject?, data?}`; source
  `bnbloc/member/<subject>`; types starting `platform.`, `room.`,
  `component.`, `desktop.` refused (`wire.ReservedEventTypePrefixes`);
  the member's chat budget at the gateway, then the dispatcher's limits;
  audited (`event` kind: type, subject, tasks set; never data); reaches
  only that room. CLI `bnbloc events send <room> <type> [--subject]
  [--data json|@file]`.
- **room_setup** `add_schedule` takes `recurrence.source`, `types`,
  `filter`; the old `name`-as-component rule is gone. Budgets held:
  tools/list 1,784 (budget 1,790), session prefix 2,518 (2,520), by
  shortening five tool briefs and listing `source` without an enum.
- **Web**: the Schedules dialog's "When something happens" editor
  (`ScheduleEventFields`: source, the closed types or typed ones, subject,
  data lines), and for a webhook task `ScheduleHookRow` (the URL, refused
  and dropped counts, make/new secret shown once with how to sign,
  revoke). Pure helpers in `lib/schedules.ts` (`triggerFromForm`,
  `describeTrigger`, `hookURL`, `hookInstructions`) with node tests.
- **CLI**: `bnbloc schedules list` prints a trigger as
  `schedule.TriggerText` and a webhook's URL.
- Not in this slice: component-declared events and the drafting assist
  (§5 step 2); the AI schedule drafter (`DRAFT_SCHEDULE`) still drafts
  clock recurrences only.
