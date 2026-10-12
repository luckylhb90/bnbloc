# A retried job hides why it was retried (2026-10-12)

## Symptom

On QA a job showed "collecting reply · attempt 2" in the room's job
indicator for minutes. Attempt 1 had failed with a retryable reason and the
job went back on the queue. The most likely cause was the agent sandbox VM
still booting after a desktop restart. Nobody could confirm it: the job
record keeps no trace of attempt 1. The run detail
(`GET /v1/rooms/{room}/jobs/{job}`) shows only the current state, and the
next claim's `running` report clears `failure_reason`. To a member,
"attempt 2" with no reason looks like a bug.

## Root cause

- **A requeue overwrites the only record of the failure.** A retryable
  failure (`UpdateAgentJobStatus` with a `Retryable()` reason) puts the job
  back to `queued` and keeps the reason in `failure_reason`. The next
  attempt's status reports overwrite that column, so a live retried job has
  no reason left.
- **A lapsed lease leaves no trace at all.** When a bridge stops renewing
  (the desktop quit or restarted), the claim of another bridge takes the
  job over directly (the claimable set included live jobs with an expired
  lease) and bumps `attempt`. No reason is recorded. The sweep only failed
  a lapsed job on its last attempt (`turn_timeout`). A lapsed job with
  attempts left and no bridge to take it stayed "claimed" until a bridge
  came back.
- **A sandbox that is not ready yet is misclassified.** A failed
  `prepareExec` (space ensure, exec-token mint, quiesce) was classified like
  any prepare error: `core_unavailable` when the error looked retryable
  ("the agent's core was unreachable", which is wrong), else `runner_failed`
  (final, though nothing ran).

## Fix

- **Every requeue records its attempt.** `agent_jobs.attempts_json`
  (migration `2026_10_agent_jobs_attempts`) keeps the last
  `store.MaxRecordedAttempts` requeued attempts as `{attempt, reason, atMs}`.
  The record is written in the same UPDATE as the requeue, so it can't race.
  The one owner is `AgentJob.requeued` (Go, memory) with its SQL twin
  `sqlRequeue` (MySQL), in `internal/store/bridge_jobs.go`. A terminal
  failure keeps the history.
- **The sweep owns lease loss.** A live job whose lease lapsed goes back to
  the queue with the retryable reason `lease_expired` (formerly
  `turn_timeout`, renamed: one reason for one event). On its last attempt it
  fails with that reason. Only queued jobs are claimable now.
- **`sandbox_starting`.** A sandbox prepare failure that means the sandbox
  is not up yet is classified as `sandbox_starting` (retryable). That covers
  a refused connection, 429/503, `backend_unavailable`, `invalid_state`,
  `machine_not_found`, or a re-probe that finds the machine not running.
  The classification lives in `services/bridge/job_failure.go`.
- **Wire and web.** `wire.AgentJob` and `wire.AgentRun` carry
  `attempts: [{attempt, reason, atMs}]`, reason codes only (decision O4).
  While a retried job is live, the room's job indicator says why, e.g.
  "retrying — the agent sandbox was starting". The run detail lists each
  earlier attempt with its reason and time. The text comes from the web's
  one reason table (`AGENT_JOB_FAILURES`, `views/room/agentJobLabels.ts`).

## Follow-up: a retry delay (landed)

A requeued job was claimed again within the bridge's 2 s claim tick, so a
sandbox that takes a minute to boot used up all 3 attempts in seconds and
the member saw a failed job. Now every requeue sets `not_before_ms` in the
same statement, and claims skip the job until then. The delay comes from
one backoff table, `store.retryBackoffs`, which also decides what retries:
`sandbox_starting` 15 s, 30 s, 60 s (cap); `core_unavailable`,
`runner_start_failed`, `budget_unavailable` 10 s, 20 s, 40 s, 60 s;
`lease_expired` 5 s. A queued job's timeout counts from when its delay
ends. The indicator counts down: "retrying in 12s — the agent sandbox was
starting".

## Landing

Status: fixed.

- `2872a8b` store: every requeue records its attempt; the sweep owns lease
  loss (migration `2026_10_agent_jobs_attempts`, wire `attempts`)
- `f37795f` bridge: a sandbox not up yet at prepare is `sandbox_starting`
- `b169c98` web: a retried job says why; the run detail lists its attempts
- `cac0754` ci: the isolated web build's cleanup trap fires once (found
  while verifying: `scripts/ci.sh web` exited 1 after every check passed)

Tests: `TestContractBridgeJobStoreAttemptHistory`,
`TestContractBridgeJobStoreLapsedLeaseRequeuedBySweep`, the updated sweep,
retry and renewal contract cases (memory and MySQL), `TestJobRequeueOwned`,
`TestAgentJobViewCarriesAttempts`, `TestRoomRunDetailResolvesReplySeq`,
`TestSweepRequeuesLapsedLeaseWithoutNotice`,
`TestSandbox_machineStillStartingIsSandboxStarting`,
`TestDriveRefusalsAreFinal`, and `web/src/views/room/agentJobLabels.test.ts`.
Each was broken once to check it fails.
