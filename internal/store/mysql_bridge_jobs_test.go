package store

import (
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

// mysqlJobs opens the real MySQL job store. Like TestMySQLBackendsRoundTrip it is
// gated on BNBLOC_TEST_MYSQL_DSN so it runs in CI/compose and skips locally.
//
//	BNBLOC_TEST_MYSQL_DSN='bnbloc:bnbloc-pass@tcp(127.0.0.1:3306)/bnbloc?parseTime=true' go test ./internal/store -run MySQLBridgeJobs
func mysqlJobs(t *testing.T) (*MySQLBridgeJobs, func()) {
	t.Helper()
	dsn := os.Getenv("BNBLOC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set BNBLOC_TEST_MYSQL_DSN to run the MySQL bridge-job tests")
	}
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	jobs, ok := b.BridgeJobs.(*MySQLBridgeJobs)
	if !ok {
		t.Fatalf("BridgeJobs is %T, want *MySQLBridgeJobs", b.BridgeJobs)
	}
	return jobs, func() { b.DB.Close() }
}

// seedClaimedJob inserts one job, advertises a capable bridge, and claims it, so
// each test starts from a live lease held by bridgeID.
func seedClaimedJob(t *testing.T, m *MySQLBridgeJobs, bridgeID string, leaseMs int64) AgentJob {
	t.Helper()
	ctx := testCtx()
	now := time.Now().UnixMilli()
	suffix := itoaBase(now) + "-" + bridgeID
	owner := "owner-" + suffix
	// A bridge id is bound to its first owner, and the DB outlives a run: make
	// the id unique per run so a rerun against the same database does not hit
	// ErrBridgeOwnerMismatch. Callers use job.LeaseBridgeID.
	bridgeID = "bridge-" + suffix
	job := AgentJob{
		JobID:       "job-" + suffix,
		RoomID:      "room-" + suffix,
		ChannelID:   "chan-" + suffix,
		MessageSeq:  now,
		OwnerUserID: owner,
		Core:        "nimbus",
		Provider:    "nimbus",
		MessageText: "verify",
		CreatedAtMs: now,
		UpdatedAtMs: now,
	}
	if _, err := m.PutAgentJob(ctx, job); err != nil {
		t.Fatalf("PutAgentJob: %v", err)
	}
	if err := m.PutBridgeHeartbeat(ctx, BridgeHeartbeat{BridgeID: bridgeID, OwnerUserID: owner, Status: BridgeOnline, LastSeenAtMs: now}); err != nil {
		t.Fatalf("PutBridgeHeartbeat: %v", err)
	}
	if err := m.ReplaceBridgeAgents(ctx, bridgeID, []BridgeAgent{{OwnerUserID: owner, Core: "nimbus", Provider: "nimbus", LastSeenAtMs: now}}); err != nil {
		t.Fatalf("ReplaceBridgeAgents: %v", err)
	}
	claimed, err := m.ClaimAgentJobs(ctx, ClaimRequest{BridgeID: bridgeID, OwnerUserID: owner, MaxJobs: 1, NowMs: now, LeaseMs: leaseMs})
	if err != nil {
		t.Fatalf("ClaimAgentJobs: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d jobs, want 1", len(claimed))
	}
	return claimed[0]
}

// The lease is the concurrency guard that keeps one prompt from running twice.
// Two bridges claiming the same queued job concurrently must produce exactly one
// winner, enforced by the claim transaction rather than by luck.
func TestMySQLBridgeJobsConcurrentClaimYieldsOneWinner(t *testing.T) {
	m, closeDB := mysqlJobs(t)
	defer closeDB()
	ctx := testCtx()
	now := time.Now().UnixMilli()
	suffix := itoaBase(now) + "-race"
	owner := "owner-" + suffix
	if _, err := m.PutAgentJob(ctx, AgentJob{JobID: "job-" + suffix, RoomID: "room-" + suffix, MessageSeq: now, OwnerUserID: owner, Core: "nimbus", Provider: "nimbus", CreatedAtMs: now, UpdatedAtMs: now}); err != nil {
		t.Fatalf("PutAgentJob: %v", err)
	}
	for _, id := range []string{"race-a-" + suffix, "race-b-" + suffix, "race-c-" + suffix} {
		if err := m.PutBridgeHeartbeat(ctx, BridgeHeartbeat{BridgeID: id, OwnerUserID: owner, Status: BridgeOnline, LastSeenAtMs: now}); err != nil {
			t.Fatalf("PutBridgeHeartbeat: %v", err)
		}
		if err := m.ReplaceBridgeAgents(ctx, id, []BridgeAgent{{OwnerUserID: owner, Core: "nimbus", Provider: "nimbus", LastSeenAtMs: now}}); err != nil {
			t.Fatalf("ReplaceBridgeAgents: %v", err)
		}
	}

	var mu sync.Mutex
	var winners []string
	var wg sync.WaitGroup
	for _, id := range []string{"race-a-" + suffix, "race-b-" + suffix, "race-c-" + suffix} {
		for range 3 {
			wg.Add(1)
			go func(bridgeID string) {
				defer wg.Done()
				got, err := m.ClaimAgentJobs(testCtx(), ClaimRequest{BridgeID: bridgeID, OwnerUserID: owner, MaxJobs: 5, NowMs: time.Now().UnixMilli(), LeaseMs: 60_000})
				if err != nil {
					return // lock contention is acceptable; a lost claim is not a win
				}
				mu.Lock()
				defer mu.Unlock()
				for _, j := range got {
					winners = append(winners, j.LeaseBridgeID)
				}
			}(id)
		}
	}
	wg.Wait()

	if len(winners) != 1 {
		t.Fatalf("job claimed %d times by %v, want exactly 1", len(winners), winners)
	}
}

// A terminal job must never be rewritten: the worker that already recorded an
// outcome, or a superseded worker reporting late, cannot flip the result.
func TestMySQLBridgeJobsTerminalStatusIsFinal(t *testing.T) {
	m, closeDB := mysqlJobs(t)
	defer closeDB()
	job := seedClaimedJob(t, m, "final-b", 60_000)

	got, err := m.UpdateAgentJobStatus(testCtx(), JobStatusUpdate{JobID: job.JobID, BridgeID: job.LeaseBridgeID, Status: AgentJobReplied, NowMs: time.Now().UnixMilli()})
	if err != nil {
		t.Fatalf("replied update: %v", err)
	}
	if got.Status != AgentJobReplied || got.RoomID != job.RoomID {
		t.Fatalf("replied job = %+v, want replied with room %s", got, job.RoomID)
	}
	if got.LeaseExpiresAtMs != 0 {
		t.Fatalf("lease_expires_at_ms = %d, want 0 on a terminal job", got.LeaseExpiresAtMs)
	}
	if _, err := m.UpdateAgentJobStatus(testCtx(), JobStatusUpdate{JobID: job.JobID, BridgeID: job.LeaseBridgeID, Status: AgentJobFailed, NowMs: time.Now().UnixMilli()}); err == nil {
		t.Fatal("terminal job was overwritten")
	}
}

// Only the lease holder may move a job. Without this guard a stale worker could
// drive another bridge's job to a terminal state.
func TestMySQLBridgeJobsStatusRequiresLease(t *testing.T) {
	m, closeDB := mysqlJobs(t)
	defer closeDB()
	job := seedClaimedJob(t, m, "lease-owner", 60_000)

	if _, err := m.UpdateAgentJobStatus(testCtx(), JobStatusUpdate{JobID: job.JobID, BridgeID: "someone-else", Status: AgentJobRunning, NowMs: time.Now().UnixMilli()}); err == nil {
		t.Fatal("status update by non-lease bridge succeeded")
	}
	if err := m.RenewAgentJobLease(testCtx(), LeaseRenewal{JobID: job.JobID, BridgeID: "someone-else", NowMs: time.Now().UnixMilli()}); err == nil {
		t.Fatal("lease renewal by non-lease bridge succeeded")
	}
}

// Renewal must actually extend the stored lease, otherwise a turn longer than
// the lease window becomes claimable again and the same prompt runs twice.
func TestMySQLBridgeJobsLeaseRenewalBlocksReclaim(t *testing.T) {
	m, closeDB := mysqlJobs(t)
	defer closeDB()
	// A 1ms lease is already expired, so without renewal the job is reclaimable.
	job := seedClaimedJob(t, m, "renew-owner", 1)

	if err := m.RenewAgentJobLease(testCtx(), LeaseRenewal{JobID: job.JobID, BridgeID: job.LeaseBridgeID, NowMs: time.Now().UnixMilli(), LeaseMs: 120_000}); err != nil {
		t.Fatalf("RenewAgentJobLease: %v", err)
	}
	stolen, err := m.ClaimAgentJobs(testCtx(), ClaimRequest{BridgeID: job.LeaseBridgeID, OwnerUserID: job.OwnerUserID, MaxJobs: 5, NowMs: time.Now().UnixMilli(), LeaseMs: 60_000, ServeAllOwners: true})
	if err != nil {
		t.Fatalf("ClaimAgentJobs: %v", err)
	}
	for _, j := range stolen {
		if j.JobID == job.JobID {
			t.Fatal("renewed job was re-claimed before its lease expired")
		}
	}
}

// A cancelled job must stay cancelled and lock its worker out, so a stopped turn
// cannot post an answer afterwards.
func TestMySQLBridgeJobsCancelLocksOutWorker(t *testing.T) {
	m, closeDB := mysqlJobs(t)
	defer closeDB()
	job := seedClaimedJob(t, m, "cancel-owner", 60_000)

	got, err := m.CancelAgentJob(testCtx(), CancelRequest{JobID: job.JobID, RoomID: job.RoomID, RequestedByUser: "alice", NowMs: time.Now().UnixMilli()})
	if err != nil {
		t.Fatalf("CancelAgentJob: %v", err)
	}
	if got.Status != AgentJobCancelled || got.FailureReason != FailureCancelled {
		t.Fatalf("cancelled job = %+v, want cancelled/cancelled", got)
	}
	if _, err := m.UpdateAgentJobStatus(testCtx(), JobStatusUpdate{JobID: job.JobID, BridgeID: job.LeaseBridgeID, Status: AgentJobReplied, NowMs: time.Now().UnixMilli()}); err == nil {
		t.Fatal("worker drove a cancelled job to replied")
	}
	// Room scope is enforced: the same job id under another room is not found.
	if _, err := m.CancelAgentJob(testCtx(), CancelRequest{JobID: job.JobID, RoomID: "other-room", NowMs: time.Now().UnixMilli()}); err == nil {
		t.Fatal("cancel succeeded across room scope")
	}
}

// The sweeper must fail only jobs no worker can finish, and must not touch a
// freshly queued job.
func TestMySQLBridgeJobsSweepFailsOnlyStuckJobs(t *testing.T) {
	m, closeDB := mysqlJobs(t)
	defer closeDB()
	ctx := testCtx()
	now := time.Now().UnixMilli()
	suffix := itoaBase(now) + "-sweep"
	owner := "owner-" + suffix
	stuckID := "job-stuck-" + suffix
	freshID := "job-fresh-" + suffix
	// stuck: queued well past the timeout; fresh: queued right now.
	if _, err := m.PutAgentJob(ctx, AgentJob{JobID: stuckID, RoomID: "room-" + suffix, MessageSeq: now, OwnerUserID: owner, Core: "c", Provider: "p", CreatedAtMs: now - 600_000, UpdatedAtMs: now - 600_000}); err != nil {
		t.Fatalf("PutAgentJob stuck: %v", err)
	}
	if _, err := m.PutAgentJob(ctx, AgentJob{JobID: freshID, RoomID: "room-" + suffix, MessageSeq: now + 1, OwnerUserID: owner, Core: "c", Provider: "p", CreatedAtMs: now, UpdatedAtMs: now}); err != nil {
		t.Fatalf("PutAgentJob fresh: %v", err)
	}

	swept, err := m.SweepAgentJobs(ctx, SweepRequest{NowMs: now, QueuedTimeoutMs: 120_000, MaxAttempts: 3})
	if err != nil {
		t.Fatalf("SweepAgentJobs: %v", err)
	}
	var sawStuck bool
	for _, j := range swept {
		if j.JobID == freshID {
			t.Fatal("sweeper failed a freshly queued job")
		}
		if j.JobID == stuckID {
			sawStuck = true
			if j.Status != AgentJobFailed || j.FailureReason != FailureNoBridgeOnline {
				t.Fatalf("swept job = %+v, want failed/no_bridge_online", j)
			}
		}
	}
	if !sawStuck {
		t.Fatalf("sweeper did not fail the stuck job %s", stuckID)
	}
}

// LookupBridge is the authorization source for worker reports: it must return
// the registering owner and report a clear miss for an unknown bridge.
func TestMySQLBridgeJobsLookupBridge(t *testing.T) {
	m, closeDB := mysqlJobs(t)
	defer closeDB()
	ctx := testCtx()
	now := time.Now().UnixMilli()
	id := "lookup-" + itoaBase(now)
	if err := m.PutBridgeHeartbeat(ctx, BridgeHeartbeat{BridgeID: id, OwnerUserID: "maya", Status: BridgeOnline, LastSeenAtMs: now, ServeAllOwners: true}); err != nil {
		t.Fatalf("PutBridgeHeartbeat: %v", err)
	}
	hb, err := m.LookupBridge(ctx, id)
	if err != nil {
		t.Fatalf("LookupBridge: %v", err)
	}
	if hb.OwnerUserID != "maya" || !hb.ServeAllOwners {
		t.Fatalf("heartbeat = %+v, want owner maya with serveAllOwners", hb)
	}
	if _, err := m.LookupBridge(ctx, "no-such-bridge-"+itoaBase(now)); err == nil {
		t.Fatal("LookupBridge returned a bridge that was never registered")
	}
}

// The latest heartbeat of an owner across their bridges, and none for an
// owner whose desktop never connected.
func TestMySQLBridgeJobsLatestBridgeForOwner(t *testing.T) {
	m, closeDB := mysqlJobs(t)
	defer closeDB()
	ctx := testCtx()
	now := time.Now().UnixMilli()
	owner := "latest-" + itoaBase(now)
	for i, seen := range []int64{now - 60_000, now, now - 3_600_000} {
		hb := BridgeHeartbeat{BridgeID: owner + "-" + itoaBase(int64(i)), OwnerUserID: owner, Status: BridgeOnline, LastSeenAtMs: seen}
		if err := m.PutBridgeHeartbeat(ctx, hb); err != nil {
			t.Fatalf("PutBridgeHeartbeat: %v", err)
		}
	}
	hb, found, err := m.LatestBridgeForOwner(ctx, owner)
	if err != nil || !found || hb.LastSeenAtMs != now || hb.BridgeID != owner+"-1" || !BridgeLive(hb, now) {
		t.Fatalf("LatestBridgeForOwner = %+v found=%v err=%v, want the bridge seen now", hb, found, err)
	}
	if BridgeLive(hb, now+BridgeOfflineAfterMs+1) {
		t.Fatal("a heartbeat older than BridgeOfflineAfterMs must not be live")
	}
	if _, found, err := m.LatestBridgeForOwner(ctx, "never-"+itoaBase(now)); err != nil || found {
		t.Fatalf("an owner with no bridge: found=%v err=%v", found, err)
	}
}

// One bridge advertises several targets under the same core/provider (codex,
// claude-code and opencode all run on the nimbus core). Regression: the
// bridge_agents primary key omitted target_id, so such a heartbeat failed with a
// duplicate-key error, the bridge never registered, and its jobs stayed
// unclaimable. Re-advertising must also be a refresh, not a failure.
func TestMySQLBridgeJobsHeartbeatAcceptsMultipleTargetsPerProvider(t *testing.T) {
	m, closeDB := mysqlJobs(t)
	defer closeDB()
	ctx := testCtx()
	now := time.Now().UnixMilli()
	id := "multi-" + itoaBase(now)
	owner := "owner-" + id
	// The roster only serves live bridges, so register liveness before asserting
	// what this bridge advertises.
	if err := m.PutBridgeHeartbeat(ctx, BridgeHeartbeat{BridgeID: id, OwnerUserID: owner, Status: BridgeOnline, LastSeenAtMs: now}); err != nil {
		t.Fatalf("PutBridgeHeartbeat: %v", err)
	}
	agents := []BridgeAgent{
		{OwnerUserID: owner, Core: "nimbus", Provider: "nimbus", TargetID: "nimbus/nimbus", LastSeenAtMs: now},
		{OwnerUserID: owner, Core: "nimbus", Provider: "nimbus", TargetID: "nimbus/codex", LastSeenAtMs: now},
		{OwnerUserID: owner, Core: "nimbus", Provider: "nimbus", TargetID: "nimbus/claude-code", LastSeenAtMs: now},
	}
	if err := m.ReplaceBridgeAgents(ctx, id, agents); err != nil {
		t.Fatalf("first heartbeat: %v", err)
	}
	// A second heartbeat carries the same targets again.
	if err := m.ReplaceBridgeAgents(ctx, id, agents); err != nil {
		t.Fatalf("repeat heartbeat: %v", err)
	}
	var got int
	for _, a := range m.ListBridgeAgents(ctx, "") {
		if a.BridgeID == id {
			got++
		}
	}
	if got != len(agents) {
		t.Fatalf("advertised %d targets, want %d", got, len(agents))
	}
}

// Retry classification must hold in the production store: a cause the next
// attempt could survive returns to the queue, a cause it cannot is terminal at
// once. Retrying the latter burns worker slots on certain failures.
func TestMySQLBridgeJobsRetryClassification(t *testing.T) {
	m, closeDB := mysqlJobs(t)
	defer closeDB()
	for _, tc := range []struct {
		name   string
		reason AgentJobFailureReason
		want   AgentJobStatus
	}{
		{"runner-start-failed", FailureRunnerStartFailed, AgentJobQueued},
		{"runner-failed", FailureRunnerFailed, AgentJobFailed},
		{"borrow-not-authorized", FailureBorrowNotAuthorized, AgentJobFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := seedClaimedJob(t, m, "retry-"+tc.name, 60_000)
			got, err := m.UpdateAgentJobStatus(testCtx(), JobStatusUpdate{JobID: job.JobID, BridgeID: job.LeaseBridgeID, Status: AgentJobFailed, FailureReason: tc.reason, NowMs: time.Now().UnixMilli()})
			if err != nil {
				t.Fatalf("UpdateAgentJobStatus: %v", err)
			}
			if got.Status != tc.want {
				t.Fatalf("status after %s = %s, want %s", tc.reason, got.Status, tc.want)
			}
			if tc.want == AgentJobQueued && got.LeaseBridgeID != "" {
				t.Fatalf("requeued job still holds lease %q", got.LeaseBridgeID)
			}
		})
	}
}

// A bridge that stopped heartbeating must not win work, and its advertisement
// must drop off the roster members pick from. Its rows outlive the process, so
// without the liveness join a dead bridge keeps taking jobs it can never run.
func TestMySQLBridgeJobsDeadBridgeNeitherClaimsNorAdvertises(t *testing.T) {
	m, closeDB := mysqlJobs(t)
	defer closeDB()
	ctx := testCtx()
	now := time.Now().UnixMilli()
	id := "dead-" + itoaBase(now)
	owner := "owner-" + id
	if _, err := m.PutAgentJob(ctx, AgentJob{JobID: "job-" + id, RoomID: "room-" + id, MessageSeq: now, OwnerUserID: owner, Core: "nimbus", Provider: "nimbus", CreatedAtMs: now, UpdatedAtMs: now}); err != nil {
		t.Fatal(err)
	}
	// Last seen beyond the offline threshold.
	if err := m.PutBridgeHeartbeat(ctx, BridgeHeartbeat{BridgeID: id, OwnerUserID: owner, Status: BridgeOnline, LastSeenAtMs: now - BridgeOfflineAfterMs - 1_000}); err != nil {
		t.Fatal(err)
	}
	if err := m.ReplaceBridgeAgents(ctx, id, []BridgeAgent{{OwnerUserID: owner, Core: "nimbus", Provider: "nimbus", TargetID: "nimbus/nimbus", LastSeenAtMs: now}}); err != nil {
		t.Fatal(err)
	}

	claimed, err := m.ClaimAgentJobs(ctx, ClaimRequest{BridgeID: id, OwnerUserID: owner, MaxJobs: 1, NowMs: now, LeaseMs: 60_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 0 {
		t.Fatalf("dead bridge claimed %d jobs", len(claimed))
	}
	for _, a := range m.ListBridgeAgents(ctx, "") {
		if a.BridgeID == id {
			t.Fatal("dead bridge still advertises its agents")
		}
	}

	// A fresh heartbeat brings it back.
	if err := m.PutBridgeHeartbeat(ctx, BridgeHeartbeat{BridgeID: id, OwnerUserID: owner, Status: BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	revived, err := m.ClaimAgentJobs(ctx, ClaimRequest{BridgeID: id, OwnerUserID: owner, MaxJobs: 1, NowMs: time.Now().UnixMilli(), LeaseMs: 60_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(revived) != 1 {
		t.Fatalf("revived bridge claimed %d jobs, want 1", len(revived))
	}
}

// A bridge id is bound to its first owner in MySQL too: the upsert must not
// re-own it, and claims through it by another owner yield nothing.
func TestMySQLBridgeHeartbeatCannotChangeOwner(t *testing.T) {
	m, closeDB := mysqlJobs(t)
	defer closeDB()
	job := seedClaimedJob(t, m, "bind-owner", 1)
	now := time.Now().UnixMilli()
	err := m.PutBridgeHeartbeat(testCtx(), BridgeHeartbeat{BridgeID: job.LeaseBridgeID, OwnerUserID: "mallory", Status: BridgeOnline, LastSeenAtMs: now})
	if !errors.Is(err, ErrBridgeOwnerMismatch) {
		t.Fatalf("re-own heartbeat err = %v, want ErrBridgeOwnerMismatch", err)
	}
	hb, err := m.LookupBridge(testCtx(), job.LeaseBridgeID)
	if err != nil || hb.OwnerUserID != job.OwnerUserID {
		t.Fatalf("bridge = %+v, %v; want owner %s", hb, err, job.OwnerUserID)
	}
	stolen, err := m.ClaimAgentJobs(testCtx(), ClaimRequest{BridgeID: job.LeaseBridgeID, OwnerUserID: "mallory", MaxJobs: 5, NowMs: now + 10, LeaseMs: 60_000, ServeAllOwners: true})
	if err != nil || len(stolen) != 0 {
		t.Fatalf("foreign claim through bridge = %+v, %v; want none", stolen, err)
	}
}
