package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// runIDList renders job ids in order for assertions.
func runIDList(js []AgentJob) string {
	ids := make([]string, len(js))
	for i, j := range js {
		ids[i] = j.JobID
	}
	return strings.Join(ids, ",")
}

// exerciseAgentRuns runs the shared AgentRunReader contract against one
// store: filters, newest-first order with a same-millisecond tie broken by
// job id, cursor paging, tenant isolation, and the claim timestamp.
func exerciseAgentRuns(t *testing.T, s interface {
	BridgeJobStore
	AgentRunReader
}, sfx string) {
	t.Helper()
	ctx := testCtx()
	other := tenant.NewContext(context.Background(), "other-"+sfx)
	room := "room-" + sfx
	alice, bob := "alice-"+sfx, "bob-"+sfx
	put := func(c context.Context, id, owner, requester string, seq, created int64) {
		t.Helper()
		if _, err := s.PutAgentJob(c, AgentJob{JobID: id + "-" + sfx, RoomID: room, MessageSeq: seq, OwnerUserID: owner, RequesterUserID: requester,
			Core: "c1", Provider: "codex", MessageText: "hi", CreatedAtMs: created}); err != nil {
			t.Fatalf("PutAgentJob %s: %v", id, err)
		}
	}
	base := time.Now().UnixMilli()
	put(ctx, "a", alice, alice, 1, base)
	put(ctx, "b", alice, bob, 2, base+10)
	put(ctx, "c", bob, bob, 3, base+10) // same millisecond as b: job id breaks the tie
	put(other, "x", alice, alice, 4, base+20)

	want := func(q AgentRunQuery, ids ...string) {
		t.Helper()
		q.RoomID = room
		got, err := s.ListAgentRuns(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		for i := range ids {
			ids[i] += "-" + sfx
		}
		if runIDList(got) != strings.Join(ids, ",") {
			t.Fatalf("ListAgentRuns(%+v) = %s, want %v", q, runIDList(got), ids)
		}
	}
	want(AgentRunQuery{}, "c", "b", "a")
	want(AgentRunQuery{Participant: bob}, "c", "b")
	want(AgentRunQuery{Owner: alice}, "b", "a")
	want(AgentRunQuery{FromMs: base + 1}, "c", "b")
	want(AgentRunQuery{ToMs: base + 10}, "a")
	want(AgentRunQuery{Statuses: []AgentJobStatus{AgentJobQueued}}, "c", "b", "a")
	want(AgentRunQuery{Statuses: []AgentJobStatus{AgentJobReplied}})
	want(AgentRunQuery{Limit: 1}, "c")
	want(AgentRunQuery{Before: AgentRunCursor{CreatedAtMs: base + 10, JobID: "c-" + sfx}}, "b", "a")
	want(AgentRunQuery{Before: AgentRunCursor{CreatedAtMs: base + 10, JobID: "b-" + sfx}}, "a")

	if _, err := s.GetAgentRun(ctx, "x-"+sfx); err != ErrAgentJobNotFound {
		t.Fatalf("cross-tenant GetAgentRun err = %v, want not found", err)
	}
	// Claiming stamps the claim phase.
	bridge := "bridge-runs-" + sfx
	now := time.Now().UnixMilli()
	if err := s.PutBridgeHeartbeat(ctx, BridgeHeartbeat{BridgeID: bridge, OwnerUserID: bob, Status: BridgeOnline, LastSeenAtMs: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceBridgeAgents(ctx, bridge, []BridgeAgent{{OwnerUserID: bob, Core: "c1", Provider: "codex", LastSeenAtMs: now}}); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimAgentJobs(ctx, ClaimRequest{BridgeID: bridge, OwnerUserID: bob, MaxJobs: 5, NowMs: now, LeaseMs: 60_000})
	if err != nil || len(claimed) != 1 || claimed[0].ClaimedAtMs != now {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	got, err := s.GetAgentRun(ctx, "c-"+sfx)
	if err != nil || got.ClaimedAtMs != now || got.Status != AgentJobClaimed {
		t.Fatalf("GetAgentRun = %+v, %v", got, err)
	}
}

func TestMemoryAgentRuns(t *testing.T) {
	exerciseAgentRuns(t, NewMemoryBridgeJobs(), "mem")
}

func TestMySQLAgentRuns(t *testing.T) {
	m, done := mysqlJobs(t)
	defer done()
	exerciseAgentRuns(t, m, itoaBase(time.Now().UnixNano()))
}
