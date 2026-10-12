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

## Follow-up (not in this change)

- A requeued job is claimed again within the bridge's 2 s claim tick. A
  sandbox that takes a minute to boot can use up all attempts. A per-job
  "not before" delay for `sandbox_starting` would fix that.

## Landing

- Commits: see `git log --grep "retried-job-hides-why"`.
