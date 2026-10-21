# Events a room subscribes to (2026-10-21)

Status: proposed. Supersedes the event part of
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
