# Room schedules

A room's scheduled task runs a member's words at set times, or when the
platform changes, as that member. This is the standing reference; the
designs are docs/prd/2026-10-15-app-identity-schedules-notify-ai.md (AI
tasks, notices) and docs/prd/2026-10-20-event-triggered-schedules.md
(platform events).

## Owners

| Rule | Owner |
|---|---|
| What a recurrence may say, its next run (DST, minimum interval) | `internal/schedule` (`Validate`, `Next`, `NextRun`, `MinInterval`) |
| What a recipient may be; text bounds; notice names | `internal/schedule` (`NormalizeRecipient`, `CheckText`, `NoticeVars`) |
| Whether a schedule may be enabled or run (never one waiting for an agent) | realtime's `runnable` (`schedule.WaitsForAgent`) |
| Which schedules a platform event reaches | `schedule.Follows` |
| What a run's text carries of its event | `schedule.RunText` (fenced), `schedule.EventLine` (a notice's plain line) |
| Recording a platform event once, handing it to realtime | `services/gateway/platform_events.go` (`recordPlatformEvent`) |
| Setting the runs of the schedules following an event | realtime's `DispatchPlatformEvent` (`services/realtime/platform_events.go`) |
| Claiming and running a due slot | realtime's scheduler (`RunDueSchedules`, `runClaimedSchedule`) |
| Carrying out an AI task or a notice | the gateway's `POST /internal/v1/schedule-runs` (`schedule_run.go`, `schedule_notice.go`) |

## Recurrence kinds (`wire.ScheduleRecurrence`)

| Kind | Fields | Runs |
|---|---|---|
| `once` | `atMs` | at that instant |
| `daily` | `time` (HH:MM) | every day at that wall-clock time in the schedule's zone |
| `weekly` | `time`, `days` (0 = Sunday) | on those days |
| `cron` | `cron` (five fields) | on the expression, at least `MinInterval` (5 minutes) apart |
| `event` | `events` (one or more of `wire.PlatformEventNames`), `component` (optional, with `component.published` only) | when the platform changes; never on the clock |

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
2. **Handed to realtime.** The recorder posts the event to
   `POST /internal/v1/platform-events` (svcauth, gateway only) in the
   background, retrying with backoff while realtime starts (a deploy
   restarts it too); a hand-off that never lands is logged as lost.
3. **Runs set.** realtime lists the enabled event schedules of the
   organizations the event reaches (`ListEventSchedules`: every one, or
   only the publishing organization for its own publish), keeps those that
   follow it and may run (`Follows`, `runnable`), and sets each one's run
   (`TriggerEventSchedule`) with the event on the row
   (`room_schedules.pending_event_json`). Coalesced per schedule by
   `MinInterval`: a pending run keeps its time and takes the latest event;
   one in flight is followed by one more an interval after its slot; one
   soon after a run waits for the interval.
4. **Run.** The scheduler claims the row like any due slot. The run's text
   is the task's text and one line of the event's facts fenced as data
   (`schedule.RunText`); a notice sent to people ends with the plain line.
   Completing the claimed run clears the event.

Facts are public (names, versions, targets); the organizations an event
reaches are not among them, and a run spends its creator's budget like any
other.
