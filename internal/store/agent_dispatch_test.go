package store

import (
	"errors"
	"testing"
	"time"
)

// exerciseAgentDispatch checks the dispatch read side of one store: a
// dispatched job keeps its parent and depth, a parent's dispatches list
// oldest first, and a bridge's owner resolves by id alone.
func exerciseAgentDispatch(t *testing.T, jobs interface {
	BridgeJobStore
	AgentDispatchReader
	AgentRunReader
}, suffix string) {
	t.Helper()
	ctx := testCtx()
	now := time.Now().UnixMilli()
	parent := AgentJob{JobID: "job-parent-" + suffix, RoomID: "room-" + suffix, MessageSeq: 1, RequesterUserID: "maya", OwnerUserID: "jack", Core: "acme", Provider: "codex", CreatedAtMs: now}
	if _, err := jobs.PutAgentJob(ctx, parent); err != nil {
		t.Fatalf("put parent: %v", err)
	}
	for i, provider := range []string{"claude", "codex"} {
		child := AgentJob{JobID: "job-child-" + provider + "-" + suffix, RoomID: parent.RoomID, MessageSeq: int64(2 + i), RequesterUserID: "maya", OwnerUserID: "bob", Core: "acme", Provider: provider, CreatedAtMs: now + int64(i), DispatchedByJobID: parent.JobID, DispatchDepth: 1}
		if _, err := jobs.PutAgentJob(ctx, child); err != nil {
			t.Fatalf("put child: %v", err)
		}
	}
	got, err := jobs.ListDispatchedJobs(ctx, parent.JobID)
	if err != nil {
		t.Fatalf("ListDispatchedJobs: %v", err)
	}
	if len(got) != 2 || got[0].Provider != "claude" || got[1].Provider != "codex" {
		t.Fatalf("dispatched = %+v; want claude then codex", got)
	}
	if got[0].DispatchedByJobID != parent.JobID || got[0].DispatchDepth != 1 {
		t.Fatalf("child marker = %q/%d; want %s/1", got[0].DispatchedByJobID, got[0].DispatchDepth, parent.JobID)
	}
	run, err := jobs.GetAgentRun(ctx, got[1].JobID)
	if err != nil || run.DispatchedByJobID != parent.JobID || run.DispatchDepth != 1 {
		t.Fatalf("GetAgentRun = %+v, %v; want the dispatch marker read back", run, err)
	}
	if none, err := jobs.ListDispatchedJobs(ctx, got[0].JobID); err != nil || len(none) != 0 {
		t.Fatalf("a leaf lists %v, %v; want none", none, err)
	}

	bridgeID := "bridge-dispatch-" + suffix
	if err := jobs.PutBridgeHeartbeat(ctx, BridgeHeartbeat{BridgeID: bridgeID, OwnerUserID: "jack", Status: BridgeOnline, LastSeenAtMs: now}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	// Cross-tenant by design: the owner resolves from any tenant scope.
	owner, err := jobs.BridgeOwner(tenantCtx("other-tenant"), bridgeID)
	if err != nil || owner != "jack" {
		t.Fatalf("BridgeOwner = %q, %v; want jack", owner, err)
	}
	if _, err := jobs.BridgeOwner(ctx, "bridge-never-seen-"+suffix); !errors.Is(err, ErrBridgeNotFound) {
		t.Fatalf("unknown bridge: %v; want ErrBridgeNotFound", err)
	}
}

func TestMemoryAgentDispatch(t *testing.T) {
	exerciseAgentDispatch(t, NewMemoryBridgeJobs(), "mem")
}

func TestMySQLAgentDispatch(t *testing.T) {
	m, done := mysqlJobs(t)
	defer done()
	exerciseAgentDispatch(t, m, itoaBase(time.Now().UnixNano()))
}
