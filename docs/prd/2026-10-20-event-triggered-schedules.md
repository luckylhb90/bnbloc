# Scheduled tasks that run when the platform changes (2026-10-20)

Status: proposed. Owner: the schedules (`internal/schedule`, realtime's
scheduler), the platform's release events (gateway boot, component and
desktop publishing), the CLI.

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
