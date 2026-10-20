# Scheduled tasks that run when the platform changes (2026-10-20)

Status: slice 1 built (§6). Owner: the schedules (`internal/schedule`,
realtime's scheduler), the platform's release events (gateway boot,
component and desktop publishing), the CLI. Standing reference:
docs/architecture/schedules.md.

A room's scheduled task can run on a platform event instead of the clock:
the platform deployed a new version, a component or default content was
published, a desktop version was published. A task can also be started by
a script (`bnbloc schedules run`). Nothing in the platform names what the
task does; a member writes that in the room.

## 1. Why

- The owner, 2026-10-20: an end-to-end test of bnbloc, written in a room,
  that runs every time the project changes. The test is a room (its
  instructions are the test plan, an agent with the browser runs it); the
  platform lacks only the trigger "when something changed".
- Today a task runs on a recurrence or by "Run now" (creator only,
  `POST /v1/rooms/{roomId}/schedules/{scheduleId}/run`); nothing fires on a
  deploy or a publish, and the CLI cannot start a task.
- Other uses: release notes drafted when a component is published, a
  digest when default content changes.

## 2. What it is (the end state)

### 2.1 Platform events (only the platform sees them)

| Event | When | Facts it carries |
|---|---|---|
| `platform.deployed` | the gateway starts on a build whose version differs from the last one recorded | version, previous version |
| `component.published` | a component (any kind) or platform content gets a new version | kind, name, version, rollout |
| `desktop.published` | `bnbloc updates publish` activates a version | version, targets |

- One owner records and dispatches them (the gateway), each once (a
  deploy with several replicas records one `platform.deployed`).

### 2.2 A task triggered by an event

- A schedule's recurrence gains the kind `event` with the event names it
  follows (one or more) and an optional filter (e.g. component name).
- When an event fires, every enabled task following it runs once, as it
  would on its clock (the same budget, audit and agent rules; agent tasks
  ask their agent as their owner). The run's text is the task's text plus
  one fenced line of the event's facts.
- Bounded: at most one run per task per event, coalesced if the same event
  repeats within the task's minimum interval (`schedule.MinInterval`).

### 2.3 Started by a script

- `bnbloc schedules run <room> <schedule>` calls the existing route as the
  task's creator (an API token of that member's subject); for CI or a
  release script ("run the room's tests after this publish").

### 2.4 AI

The task is AI (an AI task or an agent's request); the room's own words
decide what it does with the event. Setting one up by talking:
`room_setup add_schedule` accepts `event` recurrences.

### 2.5 Later: running agents in the cloud

The owner, 2026-10-20: a core that runs agents remotely (in the cloud)
would let such tasks run without a member's computer awake. Until then an
agent task runs on the machine whose agent it names. Separate PRD.

## 3. Design review

| Lens | Answer |
|---|---|
| Component or platform | Platform: events only it sees, and the trigger. The test plan, the report and what to do are the room's. |
| One owner | Events recorded and dispatched in one place; a triggered run goes through the scheduler's one run path. |
| Scale and cost | One run per following task per event; coalesced; each task's own budget. |
| Abuse | A member's task follows public facts (versions); no event carries secrets; the run spends the task owner's budget. |
| Load on agents | None new; `room_setup` takes one more recurrence kind. |
| Release | Additive recurrence kind; events start recording on the first deploy. |
| AI capability | The task itself. |

## 4. Pre-mortem

1. **A deploy fires many runs.** One event per deploy, one run per task.
2. **The agent's computer sleeps.** The run waits for its agent as today
   (an agent request queued until the agent is online); §2.5 removes it.
3. **Event storms from publishing many packages.** Coalesced per task by
   its minimum interval.

## 5. Build order

1. The three events, the `event` recurrence, the dispatch, the CLI command,
   `room_setup` support.
2. The bnbloc end-to-end room on QA (content, set up by talking): its plan,
   its agent task following all three events.

## 6. Slice 1 as built

Landed in dcd5af2d (2026-10-20).

- **Events** (`wire.PlatformEventNames`, the closed list; `wire.PlatformEvent`).
  One owner, `services/gateway/platform_events.go`: `RecordDeploy` at boot
  (the build's commit, `buildinfo`; compared with `buildinfo.SameCommit`, so
  a short and a full stamp of one build are one version; an unstamped build
  records nothing; the first recorded deploy has no previous version),
  `componentPublished` from the extension install (`handleInstallComponent`,
  every kind: cores, MCP tools, plugins, previewers, notifiers, auth
  providers) and platform content (`installPlatformContent`),
  `orgPublished` from `orgPublish` (that organization only), and
  `desktopPublished` from `PUT /v1/platform/update-config` (updates on, a
  version, at least one target's file). "Once" and "new versions only" are
  one rule: a mark per event and subject in platform settings
  (`events.<event>[.<scope>.<kind>.<name>]`, value the version, plus the
  bytes' digest for a component) moved by `store.MovePlatformSetting` (a
  compare-and-set): of replicas booting one build exactly one moves it, and
  the same version and bytes published again move nothing. No new table.
- **Dispatch.** The recorder hands the event to realtime's
  `POST /internal/v1/platform-events` (svcauth, gateway only) in the
  background, retried with backoff (2 s doubling to 1 min, 20 tries: a
  deploy restarts realtime too); a hand-off that never lands is logged as
  lost (no outbox in this slice). realtime lists the enabled event
  schedules of the organizations the event reaches
  (`ListEventSchedules`), keeps those that follow it and may run
  (`schedule.Follows`, `runnable`: never one waiting for an agent) and sets
  each one's run (`TriggerEventSchedule`, the event on the row in the added
  nullable column `room_schedules.pending_event_json`), then wakes the
  scheduler. The scheduler claims and runs it through `runClaimedSchedule`
  like any slot; an event schedule stays enabled with no next run after
  it. Coalescing (per schedule, `MinInterval`): a pending run keeps its
  time and takes the latest event's facts; an event during a run sets one
  more an interval after that run's slot; an event soon after a run waits
  for the interval.
- **Scope.** Deploys, desktop versions and platform publishes reach every
  organization; an extension restricted to some organizations
  (`EnabledTenants`, platform content's tenants) reaches only them; an
  organization's own publish only that organization.
- **Run text.** `schedule.RunText`: the task's text, then one line of the
  facts (`schedule.EventLine`, e.g. `component.published kind=mcp
  name=browser version=1.2.0 rollout=auto`) fenced as data
  (`roomlog.Fence`), for a message, an agent request, an AI task and a
  composed notice; a notice sent as written ends with the plain line (people
  read it). "Run now" carries no event.
- **Recurrence** `{"kind":"event","events":[…],"component":"…"}`, one to
  all three events, normalized to the list's order; `component` only with
  `component.published` (a package name). Validated in `schedule.Validate`
  (room packages too); a time zone is still required (the run's clock).
- **CLI**: `bnbloc schedules list <room>`, `bnbloc schedules run [--dry]
  <room> <schedule>` (docs/cli.md "Room schedules").
- **room_setup** `add_schedule` takes `recurrence.kind: "event"` with
  `recurrence.events`; the component filter is the tool's existing `name`
  parameter (no new listed parameter). The listing stays within its
  budgets: the room_setup brief lost its parenthetical list of what the
  setup covers (the actions and room_help say it), tools/list 1,787 →
  1,781 tokens, the session prefix 2,520 → 2,515.
- **Web**: the Schedules dialog's "When the platform changes" kind, the
  three events and the component filter (`ScheduleEventFields`).
- **Known gap (release).** During a mixed deploy an older realtime replica
  that claims a triggered event schedule cannot compute its next run and
  pauses it after the run; the owner turns it on again. The window is one
  rollout.
