package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

func bctx() context.Context { return tenant.NewContext(context.Background(), tenant.Default) }

func TestAgentJobCreateIsIdempotentByOrigin(t *testing.T) {
	s := NewMemoryBridgeJobs()
	j := AgentJob{
		JobID:           "job-1",
		RoomID:          "room-1",
		ChannelID:       "ch-1",
		MessageSeq:      42,
		RequesterUserID: "alice",
		OwnerUserID:     "maya",
		Core:            "nimbus",
		Provider:        "nimbus",
		Status:          AgentJobQueued,
		CreatedAtMs:     100,
		UpdatedAtMs:     100,
	}
	got, err := s.PutAgentJob(bctx(), j)
	if err != nil {
		t.Fatal(err)
	}
	j.JobID = "job-2" // same origin; should return existing job, not duplicate
	dup, err := s.PutAgentJob(bctx(), j)
	if err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Status != AgentJobClaimed || jobs[0].Attempt != 1 || jobs[0].LeaseBridgeID != "br-1" {
		t.Fatalf("claim = %+v", jobs)
	}

	jobs, _ = s.ClaimAgentJobs(bctx(), ClaimRequest{BridgeID: "other", OwnerUserID: "other", MaxJobs: 1, NowMs: 30, LeaseMs: 60000})
	if len(jobs) != 0 {
		t.Fatalf("non-owner claimed job: %+v", jobs)
	}
}

func TestAgentJobClaimIncludesPromptPayload(t *testing.T) {
	s := NewMemoryBridgeJobs()
	_, _ = s.PutAgentJob(bctx(), AgentJob{JobID: "job-1", RoomID: "room-1", ChannelID: "ch-1", MessageSeq: 1, RequesterUserID: "alice", OwnerUserID: "maya", Core: "nimbus", Provider: "nimbus", TargetID: "target-1", MessageText: "fix the flaky test", Model: "gpt-5.5", ReasoningEffort: "high", Status: AgentJobQueued, CreatedAtMs: 1, UpdatedAtMs: 1})
	_ = s.PutBridgeHeartbeat(bctx(), BridgeHeartbeat{BridgeID: "br-1", TenantID: tenant.Default, OwnerUserID: "maya", Status: BridgeOnline, LastSeenAtMs: 10})
	_ = s.ReplaceBridgeAgents(bctx(), "br-1", []BridgeAgent{{BridgeID: "br-1", TenantID: tenant.Default, OwnerUserID: "maya", Core: "nimbus", Provider: "nimbus", TargetID: "target-1", LastSeenAtMs: 10}})

	jobs, err := s.ClaimAgentJobs(bctx(), ClaimRequest{BridgeID: "br-1", OwnerUserID: "maya", MaxJobs: 1, NowMs: 20, LeaseMs: 60000})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("claim returned %d jobs, want 1", len(jobs))
	}
	if jobs[0].MessageText != "fix the flaky test" || jobs[0].Model != "gpt-5.5" || jobs[0].ReasoningEffort != "high" {
		t.Fatalf("claimed payload = text %q model %q reasoning %q", jobs[0].MessageText, jobs[0].Model, jobs[0].ReasoningEffort)
	}
}

func TestAgentJobStatusRequiresLease(t *testing.T) {
	s := NewMemoryBridgeJobs()
	_, _ = s.PutAgentJob(bctx(), AgentJob{JobID: "job-1", RoomID: "room-1", ChannelID: "ch-1", MessageSeq: 1, RequesterUserID: "alice", OwnerUserID: "maya", Core: "nimbus", Provider: "nimbus", Status: AgentJobQueued})
	_ = s.PutBridgeHeartbeat(bctx(), BridgeHeartbeat{BridgeID: "br-1", TenantID: tenant.Default, OwnerUserID: "maya", Status: BridgeOnline, LastSeenAtMs: 10})
	_ = s.ReplaceBridgeAgents(bctx(), "br-1", []BridgeAgent{{BridgeID: "br-1", TenantID: tenant.Default, OwnerUserID: "maya", Core: "nimbus", Provider: "nimbus", TargetID: "nimbus/nimbus", LastSeenAtMs: 10}})
	_, _ = s.ClaimAgentJobs(bctx(), ClaimRequest{BridgeID: "br-1", OwnerUserID: "maya", MaxJobs: 1, NowMs: 20, LeaseMs: 60000})
	if _, err := s.UpdateAgentJobStatus(bctx(), JobStatusUpdate{JobID: "job-1", BridgeID: "other", Status: AgentJobRunning, NowMs: 30}); err == nil {
		t.Fatal("status update by non-lease bridge succeeded")
	}
	if _, err := s.UpdateAgentJobStatus(bctx(), JobStatusUpdate{JobID: "job-1", BridgeID: "br-1", Status: AgentJobRunning, NowMs: 30}); err != nil {
		t.Fatal(err)
	}
}

func claimedJob(t *testing.T) *MemoryBridgeJobs {
	t.Helper()
	s := NewMemoryBridgeJobs()
	_, _ = s.PutAgentJob(bctx(), AgentJob{JobID: "job-1", RoomID: "room-1", ChannelID: "ch-1", MessageSeq: 1, RequesterUserID: "alice", OwnerUserID: "maya", Core: "nimbus", Provider: "nimbus", Status: AgentJobQueued, CreatedAtMs: 1})
	_ = s.PutBridgeHeartbeat(bctx(), BridgeHeartbeat{BridgeID: "br-1", OwnerUserID: "maya", Status: BridgeOnline, LastSeenAtMs: 10})
	_ = s.ReplaceBridgeAgents(bctx(), "br-1", []BridgeAgent{{OwnerUserID: "maya", Core: "nimbus", Provider: "nimbus", LastSeenAtMs: 10}})
	if jobs, _ := s.ClaimAgentJobs(bctx(), ClaimRequest{BridgeID: "br-1", OwnerUserID: "maya", MaxJobs: 1, NowMs: 20, LeaseMs: 1000}); len(jobs) != 1 {
		t.Fatalf("setup claim = %d jobs", len(jobs))
	}
	return s
}

// A late report from the lease holder must not rewrite a recorded outcome.
func TestAgentJobTerminalStatusIsFinal(t *testing.T) {
	s := claimedJob(t)
	got, err := s.UpdateAgentJobStatus(bctx(), JobStatusUpdate{JobID: "job-1", BridgeID: "br-1", Status: AgentJobReplied, NowMs: 30})
	if err != nil || got.RoomID != "room-1" || got.FinishedAtMs != 30 {
		t.Fatalf("replied update = %+v, %v", got, err)
	}
	if _, err := s.UpdateAgentJobStatus(bctx(), JobStatusUpdate{JobID: "job-1", BridgeID: "br-1", Status: AgentJobFailed, NowMs: 40}); err == nil {
		t.Fatal("terminal job was overwritten")
	}
}

// A renewed lease keeps a long turn from being re-claimed by another bridge.
func TestAgentJobLeaseRenewalBlocksReclaim(t *testing.T) {
	s := claimedJob(t)
	// br-2 must be alive to be a candidate for takeover at all.
	_ = s.PutBridgeHeartbeat(bctx(), BridgeHeartbeat{BridgeID: "br-2", OwnerUserID: "maya", Status: BridgeOnline, LastSeenAtMs: 2000})
	_ = s.ReplaceBridgeAgents(bctx(), "br-2", []BridgeAgent{{OwnerUserID: "maya", Core: "nimbus", Provider: "nimbus"}})
	if err := s.RenewAgentJobLease(bctx(), LeaseRenewal{JobID: "job-1", BridgeID: "br-1", NowMs: 900, LeaseMs: 1000}); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := s.ClaimAgentJobs(bctx(), ClaimRequest{BridgeID: "br-2", OwnerUserID: "maya", MaxJobs: 1, NowMs: 1500}); len(jobs) != 0 {
		t.Fatalf("renewed job re-claimed: %+v", jobs)
	}
	if jobs, _ := s.ClaimAgentJobs(bctx(), ClaimRequest{BridgeID: "br-2", OwnerUserID: "maya", MaxJobs: 1, NowMs: 2000}); len(jobs) != 1 {
		t.Fatal("expired lease not re-claimable")
	}
	if err := s.RenewAgentJobLease(bctx(), LeaseRenewal{JobID: "job-1", BridgeID: "br-1", NowMs: 2001}); err == nil {
		t.Fatal("superseded bridge renewed lease")
	}
}

func TestSweepFailsStuckJobs(t *testing.T) {
	s := NewMemoryBridgeJobs()
	_, _ = s.PutAgentJob(bctx(), AgentJob{JobID: "q", RoomID: "r", MessageSeq: 1, OwnerUserID: "maya", Core: "c", Provider: "p", CreatedAtMs: 1})
	_, _ = s.PutAgentJob(bctx(), AgentJob{JobID: "fresh", RoomID: "r", MessageSeq: 2, OwnerUserID: "maya", Core: "c", Provider: "p", CreatedAtMs: 1500})
	out, err := s.SweepAgentJobs(context.Background(), SweepRequest{NowMs: 2000, QueuedTimeoutMs: 1000, MaxAttempts: 3})
	if err != nil || len(out) != 1 || out[0].JobID != "q" || out[0].FailureReason != FailureNoBridgeOnline {
		t.Fatalf("sweep = %+v, %v", out, err)
	}
}

// A job enqueued without a timestamp must still be treated as new. Regression:
// job creation copied the client-supplied chat time, which could be 0, and the
// sweeper then read "queued since the epoch" and failed the job immediately with
// no_bridge_online before any bridge could claim it.
func TestSweepKeepsJobEnqueuedWithoutTimestamp(t *testing.T) {
	s := NewMemoryBridgeJobs()
	stored, err := s.PutAgentJob(bctx(), AgentJob{JobID: "no-clock", RoomID: "r", MessageSeq: 1, OwnerUserID: "maya", Core: "c", Provider: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if stored.CreatedAtMs <= 0 {
		t.Fatalf("stored CreatedAtMs = %d, want server time", stored.CreatedAtMs)
	}
	swept, err := s.SweepAgentJobs(context.Background(), SweepRequest{NowMs: stored.CreatedAtMs + 1000, QueuedTimeoutMs: DefaultQueuedTimeoutMs})
	if err != nil {
		t.Fatal(err)
	}
	if len(swept) != 0 {
		t.Fatalf("swept a job queued 1s ago: %+v", swept)
	}
}

// A cause the next attempt could survive must go back on the queue; a cause it
// cannot must be terminal immediately. Retrying an unauthorized borrow burns a
// worker slot on a turn that is certain to fail the same way.
func TestRetryableFailureRequeuesAndNonRetryableIsFinal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reason  AgentJobFailureReason
		want    AgentJobStatus
		claimed int
	}{
		{"runner start failure retries", FailureRunnerStartFailed, AgentJobQueued, 1},
		{"a started turn is final", FailureRunnerFailed, AgentJobFailed, 0},
		{"no reply is final", FailureReplyTimeout, AgentJobFailed, 0},
		{"core unavailable retries", FailureCoreUnavailable, AgentJobQueued, 1},
		{"turn timeout retries", FailureTurnTimeout, AgentJobQueued, 1},
		{"unauthorized borrow is final", FailureBorrowNotAuthorized, AgentJobFailed, 0},
		{"unadvertised agent is final", FailureAgentNotAdvertised, AgentJobFailed, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := claimedJob(t)
			got, err := s.UpdateAgentJobStatus(bctx(), JobStatusUpdate{JobID: "job-1", BridgeID: "br-1", Status: AgentJobFailed, FailureReason: tc.reason, NowMs: 30})
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.want {
				t.Fatalf("status after %s = %s, want %s", tc.reason, got.Status, tc.want)
			}
			// A requeued job must be picked up again; a final one must not.
			again, _ := s.ClaimAgentJobs(bctx(), ClaimRequest{BridgeID: "br-1", OwnerUserID: "maya", MaxJobs: 1, NowMs: 40, LeaseMs: 1000})
			if len(again) != tc.claimed {
				t.Fatalf("re-claimed %d jobs after %s, want %d", len(again), tc.reason, tc.claimed)
			}
		})
	}
}

// The attempt budget still bounds retries, so a job that kills every worker ends
// up terminal instead of looping forever.
func TestRetryableFailureBecomesFinalAtAttemptLimit(t *testing.T) {
	s := claimedJob(t)
	got, err := s.UpdateAgentJobStatus(bctx(), JobStatusUpdate{JobID: "job-1", BridgeID: "br-1", Status: AgentJobFailed, FailureReason: FailureRunnerStartFailed, NowMs: 30, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != AgentJobFailed {
		t.Fatalf("status = %s, want failed once the attempt budget is spent", got.Status)
	}
}

// A bridge that stopped heartbeating must not keep winning work, and its
// advertised agents must disappear from the roster members pick from.
func TestDeadBridgeNeitherClaimsNorAdvertises(t *testing.T) {
	s := NewMemoryBridgeJobs()
	now := time.Now().UnixMilli()
	_, _ = s.PutAgentJob(bctx(), AgentJob{JobID: "job-1", RoomID: "room-1", MessageSeq: 1, OwnerUserID: "maya", Core: "nimbus", Provider: "nimbus", CreatedAtMs: now})
	// Last seen well beyond the offline threshold.
	_ = s.PutBridgeHeartbeat(bctx(), BridgeHeartbeat{BridgeID: "dead", OwnerUserID: "maya", Status: BridgeOnline, LastSeenAtMs: now - BridgeOfflineAfterMs - 1_000})
	_ = s.ReplaceBridgeAgents(bctx(), "dead", []BridgeAgent{{OwnerUserID: "maya", Core: "nimbus", Provider: "nimbus"}})

	if jobs, _ := s.ClaimAgentJobs(bctx(), ClaimRequest{BridgeID: "dead", OwnerUserID: "maya", MaxJobs: 1, NowMs: now}); len(jobs) != 0 {
		t.Fatalf("dead bridge claimed %d jobs", len(jobs))
	}
	if agents := s.ListBridgeAgents(bctx(), "room-1"); len(agents) != 0 {
		t.Fatalf("dead bridge still advertises %+v", agents)
	}

	// A fresh heartbeat brings it back.
	_ = s.PutBridgeHeartbeat(bctx(), BridgeHeartbeat{BridgeID: "dead", OwnerUserID: "maya", Status: BridgeOnline, LastSeenAtMs: now})
	if agents := s.ListBridgeAgents(bctx(), "room-1"); len(agents) != 1 {
		t.Fatalf("revived bridge advertises %d agents, want 1", len(agents))
	}
	if jobs, _ := s.ClaimAgentJobs(bctx(), ClaimRequest{BridgeID: "dead", OwnerUserID: "maya", MaxJobs: 1, NowMs: now}); len(jobs) != 1 {
		t.Fatal("revived bridge could not claim")
	}
}

// A bridge id is bound to its first owner: a heartbeat from another owner is
// refused instead of re-owning the bridge.
func TestBridgeHeartbeatCannotChangeOwner(t *testing.T) {
	s := NewMemoryBridgeJobs()
	if err := s.PutBridgeHeartbeat(bctx(), BridgeHeartbeat{BridgeID: "br-1", OwnerUserID: "maya", Status: BridgeOnline, LastSeenAtMs: 10}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutBridgeHeartbeat(bctx(), BridgeHeartbeat{BridgeID: "br-1", OwnerUserID: "mallory", Status: BridgeOnline, LastSeenAtMs: 11}); !errors.Is(err, ErrBridgeOwnerMismatch) {
		t.Fatalf("re-own heartbeat err = %v, want ErrBridgeOwnerMismatch", err)
	}
	if hb, err := s.LookupBridge(bctx(), "br-1"); err != nil || hb.OwnerUserID != "maya" {
		t.Fatalf("bridge = %+v, %v; want owner maya", hb, err)
	}
	if err := s.PutBridgeHeartbeat(bctx(), BridgeHeartbeat{BridgeID: "br-1", OwnerUserID: "maya", Status: BridgeOnline, LastSeenAtMs: 12}); err != nil {
		t.Fatalf("owner refresh: %v", err)
	}
}

// Claiming through a bridge registered to another owner yields nothing, even
// in serve-all mode.
func TestClaimThroughForeignBridgeYieldsNothing(t *testing.T) {
	s := NewMemoryBridgeJobs()
	_, _ = s.PutAgentJob(bctx(), AgentJob{JobID: "job-1", RoomID: "room-1", OwnerUserID: "maya", Core: "nimbus", Provider: "nimbus", Status: AgentJobQueued, CreatedAtMs: 1, UpdatedAtMs: 1})
	_ = s.PutBridgeHeartbeat(bctx(), BridgeHeartbeat{BridgeID: "br-1", OwnerUserID: "maya", Status: BridgeOnline, LastSeenAtMs: 10})
	_ = s.ReplaceBridgeAgents(bctx(), "br-1", []BridgeAgent{{OwnerUserID: "maya", Core: "nimbus", Provider: "nimbus", LastSeenAtMs: 10}})
	jobs, err := s.ClaimAgentJobs(bctx(), ClaimRequest{BridgeID: "br-1", OwnerUserID: "mallory", MaxJobs: 1, NowMs: 20, LeaseMs: 60000, ServeAllOwners: true})
	if err != nil || len(jobs) != 0 {
		t.Fatalf("foreign-bridge claim = %+v, %v; want none", jobs, err)
	}
}
