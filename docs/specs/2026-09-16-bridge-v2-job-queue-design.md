# Bridge v2 Job Queue Design

Status: **Implemented** (6b5bf61 … defa0f4; approved 2026-09-16).

## Problem

The current bridge is an SSE consumer that drives agents directly from room chat
events. That model is fragile:

- It conflates UI subscriptions and worker subscriptions. UI streams should be
  channel-scoped and replayed; bridge streams need all channels and live-only.
- It has no durable job identity. Replay buffers, reconnects, multiple bridge
  processes, and duplicated subscriptions can drive the same chat more than once
  or miss it entirely.
- It has no visible task lifecycle. A user cannot tell whether a message is
  waiting for a bridge, running, reflowing, failed, or timed out.
- It has weak failure attribution: `signal: killed` surfaces as a generic
  message, but the product cannot distinguish runner-killed from no bridge,
  missing provider, borrow denial, or reply timeout.

This design replaces the implicit SSE-driven execution path with a durable
job-queue model. The repository is pre-release: no compatibility shim or dual
path is required. Delete the old SSE-direct-drive behavior during cutover.

## Decision

Adopt **durable `agent_jobs` + bridge worker lease**.

```
chat_message with recipient
  -> create agent_job
  -> bridge heartbeat advertises bridge_agents
  -> bridge claim loop leases matching jobs
  -> bridge runs local core turn
  -> bridge reports status/reply/failure
  -> activity/realtime broadcast job status and agent messages
```

SSE remains for UI events and roster/status notifications. It is **not** the
source of truth for agent execution.


## Trust and Storage Boundary

The durable queue tables are **server-side control-plane state**. The desktop
bridge runs on a user's machine and must never connect to MySQL, know database
schema, or write durable control-plane rows directly.

All bridge persistence flows through authenticated gateway HTTP APIs:

```
desktop bridge (user machine)
  -> gateway /v1/bridge/*
  -> server-side handler
  -> internal/store
  -> MySQL
```

Gateway handlers verify the bridge's session token before any DB mutation:

- `subject == owner_user_id` for ordinary bridge calls;
- token tenant matches the requested tenant;
- only a configured super-admin / explicit future service token may cross
  those boundaries.

If Bridge v2 later outgrows gateway handlers, the implementation may extract a
server-side queue service, but the trust boundary stays the same: bridge talks
HTTP to the public control plane, never to the database.

## Data Model

### `agent_jobs`

One row per requested agent turn.

| column | purpose |
| --- | --- |
| `job_id` PK | stable id, generated server-side |
| `tenant_id` | tenant scope |
| `room_id` | room/workspace |
| `channel_id` | topic stream the job belongs to |
| `message_seq` | originating chat message sequence |
| `client_msg_id` | originating client id if present |
| `requester_user_id` | chat author |
| `owner_user_id` | bridge owner that must execute the job |
| `core` | target core (`acme`, `nimbus`, ...) |
| `provider` | target provider/harness (`codex`, `claude-code`, ...) |
| `target_id` | fully resolved target id if known |
| `status` | `queued`, `claimed`, `running`, `reflowing`, `replied`, `failed`, `timed_out`, `cancelled` |
| `failure_reason` | nullable enum below |
| `attempt` | incremented on each claim |
| `lease_bridge_id` | current claimant |
| `lease_expires_at_ms` | lease expiry |
| `created_at_ms`, `updated_at_ms`, `started_at_ms`, `finished_at_ms` | lifecycle timestamps |
| `diagnostics_json` | developer-mode details |

Uniqueness:

```sql
UNIQUE KEY uq_agent_job_origin (
  tenant_id, room_id, message_seq, owner_user_id, core, provider
)
```

This makes chat-to-job creation idempotent.

### `bridge_heartbeats`

Current bridge liveness.

| column | purpose |
| --- | --- |
| `bridge_id` PK | stable per desktop bridge instance |
| `tenant_id` | tenant the bridge acts in |
| `owner_user_id` | member identity |
| `app_instance_id` | desktop instance id, when available |
| `version` | bridge build version |
| `status` | `online`, `offline`, `draining` |
| `last_seen_at_ms` | heartbeat timestamp |
| `metadata_json` | host/os/debug metadata |

A bridge is offline when `now - last_seen_at_ms > 30s`.

### `bridge_agents`

Advertised agent capabilities, persisted from heartbeat.

| column | purpose |
| --- | --- |
| `bridge_id` | bridge instance |
| `tenant_id` | tenant scope |
| `owner_user_id` | owner |
| `core` | core name |
| `provider` | provider name |
| `target_id` | core-qualified target id |
| `model_options_json` | model/reasoning options |
| `disabled` | user-disabled state |
| `last_seen_at_ms` | freshness |

Uniqueness: `(tenant_id, owner_user_id, core, provider, bridge_id)`.

## Job Ownership

Jobs are owner-exact:

- A job for `{ownerUserId, core, provider}` may only be claimed by that owner's
  bridge advertising that core/provider in the same tenant.
- Single-host dev (`serveAllOwners`) remains a dev-only override: it may claim
  any owner's job using the local bridge, but this must be explicit in the
  bridge heartbeat metadata.
- No same-tenant fallback in v2. Cross-member fallback would let someone else's
  machine execute another member's agent context; it requires a separate explicit
  trust/authorization design.

## API

### Chat creates jobs

`POST /v1/rooms/{roomId}/chat` remains the human entrypoint. When the body
contains `recipient`, realtime persists the chat message and creates/updates the
matching `agent_jobs` row. It also publishes `agent_job_created` to the room.

People-only messages create no job.

### Bridge heartbeat

```
POST /v1/bridge/heartbeat
{
  "bridgeId": "...",
  "tenantId": "default",
  "ownerUserId": "maya",
  "version": "...",
  "serveAllOwners": false,
  "agents": [
    {"core":"acme","provider":"codex","targetId":"acme/codex"}
  ],
  "metadata": {...}
}
```

This upserts `bridge_heartbeats` and replaces current `bridge_agents` for that
bridge.

### Claim jobs

```
POST /v1/bridge/jobs/claim
{
  "bridgeId": "...",
  "tenantId": "default",
  "ownerUserId": "maya",
  "maxJobs": 1
}
```

Server selects jobs:

- `tenant_id = tenantId`
- `owner_user_id = ownerUserId` (unless serveAllOwners)
- status is `queued`, or status is `claimed/running/reflowing` with expired
  lease and retryable state
- matching `bridge_agents` exists for `core/provider`
- borrow authorization passes for borrowed agents

Atomic claim:

```sql
UPDATE agent_jobs
SET status='claimed',
    lease_bridge_id=?,
    lease_expires_at_ms=?,
    attempt=attempt+1,
    updated_at_ms=?
WHERE job_id=?
  AND status IN ('queued','claimed','running','reflowing')
  AND (lease_expires_at_ms < ? OR lease_bridge_id IS NULL)
```

Return claimed jobs with payload needed to run.

### Report status

```
POST /v1/bridge/jobs/{jobId}/status
{
  "bridgeId": "...",
  "status": "running|reflowing|replied|failed|timed_out|cancelled",
  "failureReason": "runner_killed",
  "diagnostics": {...},
  "replyMessages": [...]
}
```

Rules:

- Status updates must match the current `lease_bridge_id` unless the job is
  already terminal.
- `replied` writes `agent_messages` to the originating room/channel.
- `failed` and `timed_out` write a visible failure message to the originating
  room/channel.
- Every status change publishes `agent_job_updated`.
- A status report is authorized against `bridge_heartbeats`: the caller must be
  the member who registered `bridgeId`. Without this any room member knowing a
  `bridgeId` could drive another member's job to a terminal state.
- A terminal job is never rewritten. A late report from a superseded worker is
  rejected rather than overwriting the recorded outcome.
- An unknown status value is rejected instead of being written through.
- `agent_job_updated` is published to the job's own `room_id`/`channel_id`, read
  from the stored job. Publishing without it strands the event and the composer
  never learns the turn ended.

### Renew a lease

```
POST /v1/bridge/jobs/{jobId}/lease
{ "bridgeId": "..." }
```

A worker renews while its turn is still running. Agent turns routinely outlive
the lease window, and without renewal the job becomes claimable again and the
same prompt executes twice. Renewal is subject to the same ownership and
terminal-state rules as a status report.

### Cancel a job

```
POST /v1/rooms/{roomId}/agent-jobs/{jobId}/cancel
```

Cancellation is a room action, not a worker report, so it is authorized by room
membership rather than bridge ownership: a member can stop a runaway turn while
another member's bridge holds the lease. It succeeds whether the job is queued or
already running, clears the lease so the running worker's next report is refused,
and is refused on an already-terminal job.

Cancelling must also stop the work, not just the bookkeeping. Leaving the agent
running to completion with its result discarded means the member stopped the
request but kept paying for it:

1. cancel clears the lease and publishes `agent_job_updated`
2. the owning bridge sees that event on the room stream and aborts the job
3. the bridge interrupts the core turn through `core.TurnInterrupter`
4. the core cancels the turn's run context, which kills the CLI process tree

The room stream is the fast path (measured ~0.3s end to end). When it is down,
the next failed lease renewal aborts the same way, bounded by the renewal
interval. Both paths converge on one abort that runs at most once per job.

`core.TurnInterrupter` is optional. A core that does not implement it keeps its
previous behavior: the job is terminal server-side and the turn's result is
discarded, but the local process runs to completion. Today only installed sidecar
cores implement it; manifest and other cores need an interrupt endpoint on their own
daemons before they can honor a stop.

### Queue clock

`created_at_ms` and `updated_at_ms` are server time, assigned when the job is
enqueued. The chat body's `atUnixMs` is client-supplied and may be zero or
skewed; queue timeout and lease decisions must never depend on it. Both stores
substitute server time for a non-positive value, so no caller can enqueue a job
that is already stale.

### Sweeping unfinishable jobs

A periodic sweep moves jobs no worker can finish to a terminal failure and
notifies their rooms:

- queued longer than the queue timeout -> `no_bridge_online`
- expired lease past max attempts -> `turn_timeout`

Without the sweep a member waits forever on a job whose bridge never appeared.

A swept job also records a visible assistant message in its room, written in the
job's own tenant. The sweep loop runs on a background context with no tenant, and
the activity store is tenant-scoped, so writing in the caller's scope is silently
discarded and the turn ends with no explanation at all.

### Retry classification

A reported failure is not automatically terminal. A cause the next attempt could
survive returns to the queue while attempts remain; a cause no retry can change
is terminal on the first report, because re-running it only burns a worker slot
on a turn certain to fail the same way.

| Retryable | Terminal on first report |
| --- | --- |
| `runner_start_failed` (the start request provably never took effect) | `runner_failed` (the turn started, or may have), `reply_timeout` |
| `core_unavailable` (unreachable before any agent work) | `borrow_not_authorized`, `agent_not_advertised`, `sandbox_required`, `budget_exhausted` |
| `turn_timeout` | `content_unavailable`, `team_unsupported`, `invalid_job`, `no_bridge_online`, `cancelled` |

A job is retried only when no agent work can have started: a started or
ambiguous turn retried would run the member's prompt twice (2026-10-12; the
bridge classifies a failure by the drive stage it reached,
`services/bridge/job_failure.go`, and `runner_killed` is gone).

A requeued job keeps its `failure_reason` while sitting back in `queued`, so the
UI shows it as retrying rather than as a fresh wait or a plain failure.

### Bridge liveness

Advertised capabilities outlive the bridge process, so both claiming and the
roster require a heartbeat within `BridgeOfflineAfterMs` (30s, two missed beats
at the 10s heartbeat interval). Without it a stopped bridge keeps appearing in
the composer and keeps winning claims, and the member waits out the full queue
timeout on a job nothing can run.

`bridge_agents` is keyed `(bridge_id, core, provider, target_id)`. One bridge
advertises several targets under the same core/provider — codex, claude-code and
opencode all run on one multi-harness core — so omitting `target_id` makes every such
heartbeat fail on a duplicate key, and the bridge never registers at all.

### Query jobs

```
GET /v1/rooms/{roomId}/agent-jobs?channelId=
```

Returns non-terminal and recent terminal jobs for the room/channel so UI can
recover after reload.

## Bridge Worker

Bridge has two loops.

### Heartbeat loop

- Runs every 10s.
- Discovers available local core targets.
- Posts heartbeat + agent capabilities.
- A bridge not seen for 30s is treated offline.

### Claim loop

- Polls every 2s, and re-claims immediately while the queue keeps handing out
  work so a burst drains at execution speed.
- Claims only as many jobs as the worker has free execution slots, so it never
  holds a lease on a job it cannot start while another bridge could run it.
- Each claimed job runs on its own goroutine and renews its own lease, so one
  long turn never blocks the other agents the bridge serves.
- Same `(room, core, provider)` stays serial, preserving current session safety.
- Different rooms/providers may run concurrently.

The old `consumeRoom -> handleChat -> claim` SSE direct-drive path is removed.
Bridge no longer drives work from replayed events.

## Job State Machine

```
queued
  -> claimed
  -> running
  -> reflowing
  -> replied
```

Failure/cancel paths:

```
queued/claimed/running/reflowing
  -> failed(reason)
  -> queued (retryable and attempt < maxAttempts)
  -> timed_out
  -> cancelled
```

Retry policy is defined once in [Retry classification](#retry-classification).
`no_bridge_online` is terminal rather than retryable: it is produced by the sweep
after the queue timeout has already elapsed with nothing able to claim the job,
so an immediate retry would fail identically.

## Failure Taxonomy

User-visible `failure_reason` values:

- `no_bridge_online`
- `agent_not_advertised`
- `borrow_not_authorized`
- `core_unavailable`
- `runner_start_failed`
- `runner_failed`
- `turn_timeout`
- `reply_timeout`
- `cancelled`
- `budget_exhausted`
- `sandbox_required`
- `content_unavailable`
- `team_unsupported`
- `invalid_job`

Developer diagnostics (`diagnostics_json`) may include:

- bridgeId, attempt, lease owner/expiry
- HTTP op/path/status/error
- core/provider/target id
- workspace/session/turn ids
- process exit code/signal and stderr tail
- last consumed seq and last published seq

## UI

Default UI (B mode): each agent-directed user message shows a status row.

Examples:

- `acme/codex · waiting for maya's bridge`
- `acme/codex · running on maya's bridge`
- `acme/codex · collecting reply…`
- `acme/codex · failed: runner was killed`

Actions:

- queued/claimed/running/reflowing: stop (cancels the job and interrupts the turn)
- replied: the status row hides once the reply message is visible

A retryable failure requeues itself, so no manual retry control ships; a terminal
failure is terminal by classification and re-running it is a new message.

Developer diagnostics (C mode): toggled by `⌘⇧D` / `Ctrl+Shift+D`, stored in
localStorage. Expands each job with jobId, bridgeId, lease, attempt,
core/session/turn ids, last seqs, HTTP errors, and stderr tail.

Agent picker/roster should display online/offline, lastSeen, bridgeId, current
job count, and capabilities.

## Rollout Plan

Pre-release clean cutover: no compatibility shims and no dual direct-drive path.

1. Store + migrations: `agent_jobs`, `bridge_heartbeats`, `bridge_agents`.
2. Backend APIs: heartbeat, claim, status, room job query.
3. Chat creation: recipient chat creates durable job; direct SSE drive removed.
4. Bridge worker: heartbeat loop + claim loop + status report.
5. Realtime/activity: publish job created/updated and agent messages from job
   reports.
6. UI: job status row, retry/cancel, diagnostics mode, roster health.
7. Delete old code: `consumeRoom` no longer drives chat; replay/allChannels
   workaround removed if no longer needed.

## Success Criteria

- Sending an agent-directed message immediately creates a visible queued job.
- If the owner bridge is offline, UI shows `no_bridge_online` instead of silence.
- Restarting bridge does not lose the job.
- Two bridges cannot execute the same job twice.
- A `runner_killed` failure is visible, retryable, and includes diagnostics.
- SSE replay can never drive old messages.
- People-only chat is unchanged.

## Non-goals

- No cross-member fallback execution in v2.
- No cloud-hosted agent runner service.
- No sub-token streaming unless the local core exposes a streaming API.
