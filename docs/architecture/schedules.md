# Room schedules

A room's scheduled task runs a member's words at set times, or when an
event it subscribes to happens, as that member. This is the standing
reference; the designs are docs/prd/2026-10-15-app-identity-schedules-notify-ai.md
(AI tasks, notices), docs/prd/2026-10-20-event-triggered-schedules.md
(platform events) and docs/prd/2026-10-21-room-events.md (one event
envelope, room events, members' events, webhooks).

## Owners

| Rule | Owner |
|---|---|
| What a recurrence may say, its next run (DST, minimum interval) | `internal/schedule` (`Validate`, `Next`, `NextRun`, `MinInterval`) |
| What a recipient may be; text bounds; notice names | `internal/schedule` (`NormalizeRecipient`, `CheckText`, `NoticeVars`) |
| Whether a schedule may be enabled or run (never one waiting for an agent) | realtime's `runnable` (`schedule.WaitsForAgent`) |
| What an event is (the envelope, its bounds) | `wire.ValidEvent` (`wire.CloudEvent`) |
| What a trigger may say; which events a task takes; no self-trigger | `internal/schedule` (`validateEvent`, `Triggers`, `Takes`) |
| What a run's text carries of its event | `schedule.RunText` (fenced), `schedule.EventLine` (a notice's plain line) |
| Recording a platform event once, handing it to realtime | `services/gateway/platform_events.go` (`recordPlatformEvent`) |
| A room's own events | realtime's publish tap (`services/realtime/room_events.go`) |
| A member's event | realtime's `POST /v1/rooms/{room}/events` (`member_events.go`, `schedule.MemberEventOf`) |
| A webhook: its URL, secret, signature, the event it carries | `schedule.NewHookID`, `VerifyHookSignature`, `HookEvent`; realtime's `hooks.go` |
| Rate limits, drops, setting the runs of the tasks an event reaches | realtime's `dispatchEvent` (`services/realtime/events.go`), `internal/ratelimit` |
| Claiming and running a due slot | realtime's scheduler (`RunDueSchedules`, `runClaimedSchedule`) |
| Carrying out an AI task or a notice | the gateway's `POST /internal/v1/schedule-runs` (`schedule_run.go`, `schedule_notice.go`) |

## Recurrence kinds (`wire.ScheduleRecurrence`)

| Kind | Fields | Runs |
|---|---|---|
| `once` | `atMs` | at that instant |
| `daily` | `time` (HH:MM) | every day at that wall-clock time in the schedule's zone |
| `weekly` | `time`, `days` (0 = Sunday) | on those days |
| `cron` | `cron` (five fields) | on the expression, at least `MinInterval` (5 minutes) apart |
| `event` | `source` (`bnbloc/platform`, `bnbloc/room`, `bnbloc/member`, `webhook`), `types` (1 to 8; a trailing `*` is a prefix), `filter` (optional: `subject`, `subjectPrefix`, `data` field = value) | when an event its trigger takes happens; never on the clock |

Every kind keeps a time zone (`timezone`): the run's clock, a notice's
`{date}`. The recurrence is stored as JSON (`room_schedules.recurrence_json`).

## The run path

realtime owns schedules because it owns chat publish. Every replica runs
the scheduler: each minute (and when a platform event set a run,
`wakeScheduler`) it claims the due rows (`store.ClaimDueSchedules`, an
atomic claim with a lease, so a slot runs once across replicas) and runs
each through `runClaimedSchedule`:

- a message to the room or to an agent is posted as the creator through
  `PostChat` (the same membership, channel and agent rules as a typed
  message), by the room's owning replica, with the slot's client id so a
  re-claimed slot never posts twice;
- an AI task or a notice goes to the gateway, which runs it on the
  creator's assist budget and audits it.

A clock schedule's next run follows from its recurrence (`NextRun`); a
one-shot disables itself; an event schedule waits, enabled, for its next
event. "Run now" (`POST /v1/rooms/{room}/schedules/{id}/run`, creator only;
`bnbloc schedules run`) runs the same way and carries no event.

## Events

Every event is one CloudEvents 1.0 envelope (`wire.CloudEvent`:
`specversion`, `id`, `source`, `type`, `time`, `subject?`, `data?`),
checked by `wire.ValidEvent` (printable id, source and type without
spaces, an RFC 3339 time, `data` one JSON value of at most 16 KiB once
compacted). `data` is untrusted: a run reads the event fenced as data,
naming who sent it.

| Source | Emitted by | Reaches | Types |
|---|---|---|---|
| `bnbloc/platform` | the gateway (below) | every organization, or the one that published | `platform.deployed`, `component.published`, `desktop.published` |
| `bnbloc/room/<id>` | realtime's publish tap, from the room's own feed | that room's tasks | `room.file.added` (an artifact's new version; subject its file name), `room.member.joined` (subject the person), `room.app.data.changed` (subject the app; the first change at once, the rest of each minute as one event counting them) |
| `bnbloc/member/<subject>` | `POST /v1/rooms/{room}/events` (a member who may chat; `bnbloc events send`) | that room's tasks | any type not starting `platform.`, `room.`, `component.`, `desktop.` |
| `webhook/<hook>` | `POST /v1/hooks/{hook}` (an outside system) | that one task | `X-Bnbloc-Event`, `com.github.<X-GitHub-Event>`, or the JSON body's `type` |

**Delivery.** `dispatchEvent` lists the enabled event tasks the event may
reach (`ListEventSchedules` by tenants and room), keeps those whose
trigger takes it (`schedule.Triggers`) and may run (`runnable`), spends
the source's (5/s, burst 60) and each task's (1/s, burst 20) budget per
replica's share, and sets each run (`TriggerEventSchedule`, coalesced by
`MinInterval` as below). An event over a limit is dropped and counted on
the task (`droppedEvents`); the first drop since the task was last edited
notifies the room's owner (`schedule_notice`, operational).

**No self-trigger.** What a scheduled run does carries its job: a file
version's `sourceJobId`, an agent's app write (`svcauth.AppScope.Job`, the
app scope's signed job), a publish under a job grant (`EventDispatch.CauseJob`).
`causeRoom` walks the job, and the dispatch chain above it, to a post whose
client id is a run slot's; that room's tasks never take the event (a lookup
that fails reads as caused: a missed trigger beats a loop).

**Webhooks.** A task whose trigger is `webhook` gets its own URL from its
creator (`POST /v1/rooms/{room}/schedules/{id}/hook`, which also rotates
the secret and keeps the URL; `DELETE` revokes, by the creator, the room's
owner or an admin). The secret (`whsec_…`) is answered once and sealed at
rest with `BNBLOC_SECRET_STORE_KEY` (`room_schedules.hook_secret`, bound to
the hook id); without the key webhooks are off (503). The gateway's open
route spends the client IP's public budget and proxies the body (at most
512 KiB) to realtime, which verifies `X-Bnbloc-Signature` or
`X-Hub-Signature-256` (`sha256=<hex HMAC-SHA256 of the raw body>`), builds
the event (`schedule.HookEvent`: data over 16 KiB keeps its smaller
top-level fields and names the rest in `_omitted`), and dispatches it to
the hook's task. Refused requests (unsigned, wrong signature, too large,
not JSON, no type) are counted on the hook (`refused`, `lastRefusedAtMs`);
one over the task's limit answers 429.

## Platform events

| Event | When | Facts |
|---|---|---|
| `platform.deployed` | a gateway starts on a build (its commit, `buildinfo`) other than the last recorded | version, previous |
| `component.published` | an extension install (`POST /v1/platform/components`), platform content, or an organization's publish (`orgPublish`) brings a new version or new bytes | kind, name, version, rollout |
| `desktop.published` | `PUT /v1/platform/update-config` serves a new version (updates on, a target's file) | version, targets |

1. **Recorded once.** Each event has a mark in platform settings
   (`events.<event>[.<subject>]`: the deploy, the desktop, or the
   component's scope, kind and name) moved by `store.MovePlatformSetting`, a
   compare-and-set: of several gateway replicas booting one build, or two
   installs of one package, exactly one moves the mark; the same mark (the
   same build, version and bytes) records nothing.
2. **Handed to realtime.** The recorder posts the event (source
   `bnbloc/platform`; the component's name as subject, its kind, name,
   version and rollout as data) to `POST /internal/v1/events` (svcauth,
   gateway only) in the
   background, retrying with backoff while realtime starts (a deploy
   restarts it too); a hand-off that never lands is logged as lost.
3. **Runs set.** realtime's `dispatchEvent` lists the enabled event
   schedules of the organizations the event reaches (`ListEventSchedules`:
   every one, or only the publishing organization for its own publish),
   keeps those whose trigger takes it and may run (`Triggers`, `runnable`),
   and sets each one's run
   (`TriggerEventSchedule`) with the event on the row
   (`room_schedules.pending_event_json`, the whole envelope). Coalesced per schedule by
   `MinInterval`: a pending run keeps its time and takes the latest event;
   one in flight is followed by one more an interval after its slot; one
   soon after a run waits for the interval.
4. **Run.** The scheduler claims the row like any due slot. The run's text
   is the task's text and the event fenced as data (`schedule.RunText`:
   type, source, subject, time, id and data, the data cut, and the cut
   said, to keep the task's words room); a notice sent to people ends with
   the plain line (`EventLine`).
   Completing the claimed run clears the event.

Facts are public (names, versions, targets); the organizations an event
reaches are not among them, and a run spends its creator's budget like any
other.
