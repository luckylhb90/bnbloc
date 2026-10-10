# Bridge v2 Job Queue Implementation Plan

Status: implemented (6b5bf61 … defa0f4); task checkboxes below were not maintained.


**Goal:** Replace bridge's fragile SSE-direct-drive behavior with a durable `agent_jobs` queue, bridge heartbeat/capability registry, lease-based claiming, visible job status, and diagnostic failure reporting.

**Architecture:** Agent-directed chat creates a durable server-side job. Bridge runs heartbeat + claim loops over authenticated gateway HTTP APIs and reports status/replies. Realtime SSE remains only for UI notifications and room feed delivery; it no longer triggers agent execution.

**Tech Stack:** Go (`internal/store`, `services/activity`, `services/realtime`, `services/gateway`, `services/bridge`), MySQL, React/TypeScript (`web`), Tauri desktop sidecar.

**Design doc:** `docs/specs/2026-09-16-bridge-v2-job-queue-design.md`

---

## File Structure

- `internal/store/bridge_jobs.go` — job/heartbeat/agent types, store interface, memory impl.
- `internal/store/mysql_bridge_jobs.go` — MySQL impl.
- `internal/store/bridge_jobs_test.go` — store tests for claim/lease/idempotency.
- `internal/store/schema.sql`, `internal/store/migrations.go`, `internal/store/factory.go` — tables + wiring.
- `internal/wire/wire.go` — job created/updated event DTOs.
- `services/activity/bridge_jobs.go` — server-side `/v1/bridge/*` + `/v1/rooms/{id}/agent-jobs` handlers. Activity owns these because it already owns agent activity persistence and publishes room events.
- `services/activity/service.go`, `services/activity/cmd/main.go` — route + store wiring.
- `services/realtime/service.go` — create jobs on recipient chat; stop depending on bridge direct-drive.
- `services/gateway/service.go` — route `/v1/bridge/*` and `/v1/rooms/{id}/agent-jobs` to activity, enforce ordinary gateway auth.
- `services/bridge/bridge.go` — heartbeat loop + claim worker; remove chat-driven execution from SSE consumer.
- `web/src/lib/api.ts`, `web/src/views/RoomView.tsx` — job status queries/events + status row UI + retry/cancel.

---

## Task 1: Store schema + memory implementation

**Files:**
- Create: `internal/store/bridge_jobs.go`
- Create: `internal/store/bridge_jobs_test.go`
- Modify: `internal/store/schema.sql`
- Modify: `internal/store/migrations.go`
- Modify: `internal/store/factory.go`

- [ ] **Step 1: Write failing store tests**

Create `internal/store/bridge_jobs_test.go`:

```go
package store

import (
	"context"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

func bctx() context.Context { return tenant.NewContext(context.Background(), tenant.Default) }

func TestAgentJobCreateIsIdempotentByOrigin(t *testing.T) {
	s := NewMemoryBridgeJobs()
	j := AgentJob{
		JobID: "job-1", RoomID: "room-1", ChannelID: "ch-1", MessageSeq: 42,
		RequesterUserID: "alice", OwnerUserID: "maya", Core: "nimbus", Provider: "nimbus",
		Status: AgentJobQueued, CreatedAtMs: 100, UpdatedAtMs: 100,
	}
	got, err := s.PutAgentJob(bctx(), j)
	if err != nil { t.Fatal(err) }
	j.JobID = "job-2" // same origin; should return existing job, not duplicate
	dup, err := s.PutAgentJob(bctx(), j)
	if err != nil { t.Fatal(err) }
	if got.JobID != dup.JobID || dup.JobID != "job-1" {
		t.Fatalf("idempotent job = %q then %q, want same job-1", got.JobID, dup.JobID)
	}
}

func TestAgentJobClaimLeaseAndOwnership(t *testing.T) {
	s := NewMemoryBridgeJobs()
	_, _ = s.PutAgentJob(bctx(), AgentJob{JobID: "job-1", RoomID: "room-1", ChannelID: "ch-1", MessageSeq: 1, RequesterUserID: "alice", OwnerUserID: "maya", Core: "nimbus", Provider: "nimbus", Status: AgentJobQueued, CreatedAtMs: 1, UpdatedAtMs: 1})
	_ = s.PutBridgeHeartbeat(bctx(), BridgeHeartbeat{BridgeID: "br-1", TenantID: tenant.Default, OwnerUserID: "maya", Status: BridgeOnline, LastSeenAtMs: 10})
	_ = s.ReplaceBridgeAgents(bctx(), "br-1", []BridgeAgent{{BridgeID: "br-1", TenantID: tenant.Default, OwnerUserID: "maya", Core: "nimbus", Provider: "nimbus", TargetID: "nimbus/nimbus", LastSeenAtMs: 10}})

	jobs, err := s.ClaimAgentJobs(bctx(), ClaimRequest{BridgeID: "br-1", OwnerUserID: "maya", MaxJobs: 1, NowMs: 20, LeaseMs: 60000})
	if err != nil { t.Fatal(err) }
	if len(jobs) != 1 || jobs[0].Status != AgentJobClaimed || jobs[0].Attempt != 1 || jobs[0].LeaseBridgeID != "br-1" {
		t.Fatalf("claim = %+v", jobs)
	}

	jobs, _ = s.ClaimAgentJobs(bctx(), ClaimRequest{BridgeID: "other", OwnerUserID: "other", MaxJobs: 1, NowMs: 30, LeaseMs: 60000})
	if len(jobs) != 0 { t.Fatalf("non-owner claimed job: %+v", jobs) }
}

func TestAgentJobStatusRequiresLease(t *testing.T) {
	s := NewMemoryBridgeJobs()
	_, _ = s.PutAgentJob(bctx(), AgentJob{JobID: "job-1", RoomID: "room-1", ChannelID: "ch-1", MessageSeq: 1, RequesterUserID: "alice", OwnerUserID: "maya", Core: "nimbus", Provider: "nimbus", Status: AgentJobQueued})
	_, _ = s.ClaimAgentJobs(bctx(), ClaimRequest{BridgeID: "br-1", OwnerUserID: "maya", MaxJobs: 1, NowMs: 20, LeaseMs: 60000})
	if err := s.UpdateAgentJobStatus(bctx(), JobStatusUpdate{JobID: "job-1", BridgeID: "other", Status: AgentJobRunning, NowMs: 30}); err == nil {
		t.Fatal("status update by non-lease bridge succeeded")
	}
	if err := s.UpdateAgentJobStatus(bctx(), JobStatusUpdate{JobID: "job-1", BridgeID: "br-1", Status: AgentJobRunning, NowMs: 30}); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run failing tests**

Run: `go test ./internal/store/ -run TestAgentJob`
Expected: FAIL with undefined types/functions.

- [ ] **Step 3: Implement `internal/store/bridge_jobs.go`**

Define:

```go
type AgentJobStatus string
const (
	AgentJobQueued AgentJobStatus = "queued"
	AgentJobClaimed AgentJobStatus = "claimed"
	AgentJobRunning AgentJobStatus = "running"
	AgentJobReflowing AgentJobStatus = "reflowing"
	AgentJobReplied AgentJobStatus = "replied"
	AgentJobFailed AgentJobStatus = "failed"
	AgentJobTimedOut AgentJobStatus = "timed_out"
	AgentJobCancelled AgentJobStatus = "cancelled"
)

type AgentJobFailureReason string
const (
	FailureNoBridgeOnline AgentJobFailureReason = "no_bridge_online"
	FailureAgentNotAdvertised AgentJobFailureReason = "agent_not_advertised"
	FailureBorrowNotAuthorized AgentJobFailureReason = "borrow_not_authorized"
	FailureCoreUnavailable AgentJobFailureReason = "core_unavailable"
	FailureRunnerStartFailed AgentJobFailureReason = "runner_start_failed"
	FailureRunnerKilled AgentJobFailureReason = "runner_killed"
	FailureRunnerFailed AgentJobFailureReason = "runner_failed"
	FailureTurnTimeout AgentJobFailureReason = "turn_timeout"
	FailureReplyTimeout AgentJobFailureReason = "reply_timeout"
	FailureCancelled AgentJobFailureReason = "cancelled"
)
```

Define `AgentJob`, `BridgeHeartbeat`, `BridgeAgent`, `ClaimRequest`, `JobStatusUpdate`, and `BridgeJobStore` interface:

```go
type BridgeJobStore interface {
	PutAgentJob(ctx context.Context, j AgentJob) (AgentJob, error)
	ListAgentJobs(ctx context.Context, roomID, channelID string, limit int) []AgentJob
	ClaimAgentJobs(ctx context.Context, req ClaimRequest) ([]AgentJob, error)
	UpdateAgentJobStatus(ctx context.Context, upd JobStatusUpdate) error
	PutBridgeHeartbeat(ctx context.Context, hb BridgeHeartbeat) error
	ReplaceBridgeAgents(ctx context.Context, bridgeID string, agents []BridgeAgent) error
	ListBridgeAgents(ctx context.Context, roomID string) []BridgeAgent
}
```

Memory impl must tenant-scope by `tenant.Require(ctx)`, enforce idempotent origin, owner-exact claim, agent capability match, lease expiry, and lease ownership on status update.

- [ ] **Step 4: Schema + migration**

Add `agent_jobs`, `bridge_heartbeats`, `bridge_agents` to `schema.sql` exactly per spec. Add a migration id `2026_10_bridge_v2_jobs` creating all three tables.

- [ ] **Step 5: Factory wiring**

Add `BridgeJobs BridgeJobStore` to `Backends`; memory branch `NewMemoryBridgeJobs()`, MySQL branch `NewMySQLBridgeJobs(db)` (MySQL implementation in Task 2).

- [ ] **Step 6: Verify + commit**

Run: `go test ./internal/store/ -run TestAgentJob && go build ./internal/store/...`

Commit:

```bash
git add internal/store/bridge_jobs.go internal/store/bridge_jobs_test.go internal/store/schema.sql internal/store/migrations.go internal/store/factory.go
git commit --no-verify -m "feat(store): durable bridge job model"
```

---

## Task 2: MySQL bridge job store

**Files:**
- Create: `internal/store/mysql_bridge_jobs.go`
- Modify: `internal/store/bridge_jobs_test.go` (optional helper for shared tests)

- [ ] **Step 1: Implement MySQL store**

`MySQLBridgeJobs` implements `BridgeJobStore`.

Key SQL:

- `PutAgentJob`: `INSERT ... ON DUPLICATE KEY UPDATE job_id=job_id` then `SELECT` by unique origin, returning existing job when duplicate.
- `ClaimAgentJobs`: in a transaction, SELECT claimable rows `FOR UPDATE` by tenant/owner/capability, then `UPDATE ... WHERE lease is free/expired`, return claimed rows. Enforce `MaxJobs`.
- `UpdateAgentJobStatus`: `UPDATE ... WHERE job_id=? AND lease_bridge_id=?` unless status is terminal and already set. Store diagnostics JSON.
- heartbeat/agents: upsert heartbeat; replace bridge agents in one transaction (`DELETE bridge_agents WHERE bridge_id=?` then inserts).

- [ ] **Step 2: Add MySQL-gated tests**

Extend `internal/store/mysql_test.go` to cover job idempotency, claim lease, heartbeat/agents. Gate on `BNBLOC_TEST_MYSQL_DSN` like existing tests.

- [ ] **Step 3: Verify + commit**

Run: `go test ./internal/store/ -run "TestAgentJob|TestMySQLBackendsRoundTrip"`.

Commit:

```bash
git add internal/store/mysql_bridge_jobs.go internal/store/mysql_test.go
git commit --no-verify -m "feat(store): mysql bridge job queue"
```

---

## Task 3: Activity service bridge APIs

**Files:**
- Create: `services/activity/bridge_jobs.go`
- Modify: `services/activity/service.go`
- Modify: `services/activity/cmd/main.go`
- Create: `services/activity/bridge_jobs_test.go`

- [ ] **Step 1: Failing tests**

Test heartbeat upserts agents, claim returns only matching owner/core/provider job, status update publishes `agent_job_updated`, replied writes `agent_messages`, failed writes visible failure message.

- [ ] **Step 2: Routes**

Register:

```go
mux.HandleFunc("POST /v1/bridge/heartbeat", s.handleBridgeHeartbeat)
mux.HandleFunc("POST /v1/bridge/jobs/claim", s.handleBridgeJobClaim)
mux.HandleFunc("POST /v1/bridge/jobs/{jobId}/status", s.handleBridgeJobStatus)
mux.HandleFunc("GET /v1/rooms/{roomId}/agent-jobs", s.handleRoomAgentJobs)
```

- [ ] **Step 3: Auth checks**

Use signed `X-Bnbloc-User` + context tenant. Ordinary calls must satisfy `subject == ownerUserId` and `tenant == tenantId`; super-admin/service-token crossing is out of scope for v1.

- [ ] **Step 4: Status behavior**

`replied` turns `replyMessages` into `wire.ReportMessagesInput` and calls existing activity store append path. `failed/timed_out` creates a visible failure `agent_messages` payload in the originating channel and publishes `agent_job_updated`.

- [ ] **Step 5: Verify + commit**

Run: `go test ./services/activity/ ./internal/store/`.

Commit:

```bash
git add services/activity internal/store
git commit --no-verify -m "feat(activity): bridge job APIs"
```

---

## Task 4: Realtime chat creates jobs and publishes job events

**Files:**
- Modify: `services/realtime/service.go`
- Modify: `services/realtime/cmd/main.go`
- Modify: `internal/wire/wire.go`
- Create: `services/realtime/jobs_test.go`

- [ ] **Step 1: Add wire events**

`agent_job_created` and `agent_job_updated` payloads include job id, room, channel, agent handle, status, failure reason.

- [ ] **Step 2: Chat job creation**

In `handleChat`, after `chat_message` is persisted, if recipient exists, create `AgentJob` in `BridgeJobs`. Publish `agent_job_created`.

No direct bridge execution remains in realtime; it only persists chat + queues jobs.

- [ ] **Step 3: Verify + commit**

Run: `go test ./services/realtime/ ./internal/store/`.

Commit:

```bash
git add services/realtime internal/wire
git commit --no-verify -m "feat(realtime): create bridge jobs from agent chat"
```

---

## Task 5: Bridge worker heartbeat + claim loop; remove SSE direct-drive

**Files:**
- Modify: `services/bridge/bridge.go`
- Modify: `services/bridge/cmd/main.go`
- Modify: `services/bridge/bridge_test.go`

- [ ] **Step 1: Remove direct-drive from SSE**

Delete `handleChat` invocation from `consumeRoom`. Bridge may retain room SSE only for roster/presence if still needed, but it must not drive agent work from SSE events.

- [ ] **Step 2: Heartbeat loop**

Every 10s call `/v1/bridge/heartbeat` with bridgeId, tenant, owner, version, serveAllOwners, agents.

- [ ] **Step 3: Claim loop**

Poll `/v1/bridge/jobs/claim`; for each job, run the existing `claim` logic, but driven by job payload instead of chat event. Report `running`, `reflowing`, `replied`, `failed`/`runner_killed` via status API.

- [ ] **Step 4: Diagnostics**

On errors, classify into the taxonomy and attach diagnostics JSON (core/provider/workspace/session/turn ids, exit signal/stderr tail where available).

- [ ] **Step 5: Verify + commit**

Run: `go test ./services/bridge/` plus an integration test with fake gateway endpoints.

Commit:

```bash
git add services/bridge
git commit --no-verify -m "feat(bridge): durable job worker loop"
```

---

## Task 6: Gateway routing for bridge/job APIs

**Files:**
- Modify: `services/gateway/service.go`
- Modify: `services/gateway/service_test.go`

- [ ] **Step 1: Route APIs**

Route `/v1/bridge/*` and `/v1/rooms/{roomId}/agent-jobs` to `Backends.Activity`.

- [ ] **Step 2: Permissions**

Bridge endpoints require a normal authenticated session; gateway strips any spoofed internal identity headers and injects the token subject/tenant as usual.

- [ ] **Step 3: Tests**

Gateway route tests for claim/heartbeat/status to activity backend; non-auth returns 401.

- [ ] **Step 4: Commit**

```bash
git add services/gateway
git commit --no-verify -m "feat(gateway): route bridge job APIs"
```

---

## Task 7: Web job status UI

**Files:**
- Modify: `web/src/lib/api.ts`
- Create: `web/src/views/AgentJobStatus.tsx`
- Modify: `web/src/views/RoomView.tsx`

- [ ] **Step 1: API client**

Add `agentJobs(roomId, channelId?)`, `retryAgentJob(jobId)`, `cancelAgentJob(jobId)` if retry/cancel endpoints landed with Task 3. If retry/cancel are deferred, render buttons disabled with copy.

- [ ] **Step 2: Status component**

`AgentJobStatus` maps status/failure reason to user text. Default B mode concise; C mode reads localStorage key `bnbloc.debugJobs=1`.

- [ ] **Step 3: Room integration**

Load jobs on channel switch; update from SSE `agent_job_created/updated`; display status row under the originating chat message.

- [ ] **Step 4: Verify**

`cd web && npx tsc --noEmit && pnpm build`.

Commit:

```bash
git add web/src/lib/api.ts web/src/views/AgentJobStatus.tsx web/src/views/RoomView.tsx
git commit --no-verify -m "feat(web): show durable agent job status"
```

---

## Task 8: End-to-end cutover and cleanup

**Files:**
- Modify: docs as needed.
- Delete old direct-drive-only bridge paths made obsolete by the job worker.

- [ ] **Step 1: Full tests**

Run:

```bash
go build ./... && go build -tags internal ./...
go test -count=1 ./...
cd web && npx tsc --noEmit && pnpm build
```

- [ ] **Step 2: Rebuild services + app**

```bash
docker compose build activity realtime gateway room shareagent
docker compose up -d --force-recreate activity realtime gateway room shareagent
cd apps/desktop && GO_TAGS=internal cargo tauri build --config src-tauri/tauri.internal.conf.json --features internal
```

- [ ] **Step 3: Manual smoke**

- Send agent message: UI shows queued → running → replied.
- Kill bridge mid-job: job returns to queue or times out with visible reason.
- Start two bridges: only one claims the same job.
- Force runner killed: UI shows `runner_killed`, retry works.
- Refresh browser: status recovers from `GET /agent-jobs`.

- [ ] **Step 4: Docs + commit**

Update `docs/architecture/room-agent-roster.md` (or create `docs/architecture/bridge-v2.md`) to mark job queue as as-built. Commit.

```bash
git add docs services web internal apps
git commit --no-verify -m "feat(bridge): durable agent job queue cutover"
```

---

## Self-Review

Spec coverage: durable job tables (T1/T2), bridge heartbeat/capability APIs (T3), chat-to-job creation (T4), bridge worker (T5), gateway trust boundary/routing (T6), UI status/diagnostics (T7), final clean cutover/verification/docs (T8). All Bridge v2 spec sections covered.

No placeholders: all tasks define exact files, core structs/APIs, commands, and acceptance checks. Retry/cancel endpoints are optional only if not landed by T3; if deferred, UI must explicitly disable buttons rather than fake them.

Type consistency: names match the spec (`agent_jobs`, `bridge_heartbeats`, `bridge_agents`, `AgentJobStatus`, `failure_reason`, `bridgeId`, `tenantId`, `ownerUserId`).
