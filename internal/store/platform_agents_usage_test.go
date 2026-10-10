package store

import (
	"testing"
)

// Platform-agent usage reads the platform grants' borrow turns across every
// tenant (PlatformOnly + AllTenants); each record keeps its tenant.
func platformTurnCases(t *testing.T, g GrantStore, sfx string) {
	t.Helper()
	host := "host-" + sfx
	type seed struct{ tnt, room, gid, source string }
	seeds := []seed{
		{"t-pa-a-" + sfx, "room-pa-a-" + sfx, "pg-a-" + sfx, GrantSourcePlatform},
		{"t-pa-b-" + sfx, "room-pa-b-" + sfx, "pg-b-" + sfx, GrantSourcePlatform},
		{"t-pa-a-" + sfx, "room-pa-a-" + sfx, "og-a-" + sfx, ""}, // owner-issued
	}
	for i, sd := range seeds {
		ctx := tenantCtx(sd.tnt)
		if err := g.PutGrant(ctx, Grant{GrantID: sd.gid, RoomID: sd.room, TargetID: "shared-agent:c:codex", OwnerUserID: host,
			GranteeUserID: "*", MaxConcurrent: 1, CreatedAtMs: 1, Source: sd.source}); err != nil {
			t.Fatalf("PutGrant: %v", err)
		}
		if err := g.AppendAudit(ctx, AuditRecord{RoomID: sd.room, GrantID: sd.gid, ActorUserID: "bob", OwnerUserID: host,
			Action: "turn", Decision: "allow", AtMs: int64(1000 + i)}); err != nil {
			t.Fatalf("AppendAudit: %v", err)
		}
	}
	turns := g.BorrowTurns(tenantCtx("t-pa-a-"+sfx), BorrowTurnQuery{FromMs: 0, ToMs: 5000, OwnerUserID: host, PlatformOnly: true, AllTenants: true})
	if len(turns) != 2 || turns[0].GrantID != "pg-a-"+sfx || turns[0].TenantID != "t-pa-a-"+sfx || turns[1].TenantID != "t-pa-b-"+sfx {
		t.Fatalf("platform turns = %+v", turns)
	}
	if n := len(g.BorrowTurns(tenantCtx("t-pa-a-"+sfx), BorrowTurnQuery{FromMs: 0, ToMs: 5000, OwnerUserID: host, PlatformOnly: true})); n != 1 {
		t.Fatalf("tenant-scoped platform turns = %d, want 1", n)
	}
	if n := len(g.BorrowTurns(tenantCtx("t-pa-a-"+sfx), BorrowTurnQuery{FromMs: 0, ToMs: 5000, OwnerUserID: host, AllTenants: true})); n != 3 {
		t.Fatalf("all-tenant turns = %d, want 3", n)
	}
}

func TestMemoryPlatformTurns(t *testing.T) { platformTurnCases(t, NewMemoryGrant(), "m") }

func TestMySQLPlatformTurns(t *testing.T) {
	b := mysqlBackendsForTest(t)
	platformTurnCases(t, b.Grant, runSuffix())
}

// PendingAgentJobs counts an owner's queued and claimed jobs per agent across
// tenants; running and finished jobs are not pending.
func pendingJobCases(t *testing.T, jobs BridgeJobStore, sfx string) {
	t.Helper()
	owner := "host-" + sfx
	for i, j := range []AgentJob{
		{RoomID: "r1", Core: "c", Provider: "codex", Status: AgentJobQueued},
		{RoomID: "r2", Core: "c", Provider: "codex", Status: AgentJobQueued, TenantID: "other"},
		{RoomID: "r3", Core: "c", Provider: "claude", Status: AgentJobClaimed},
		{RoomID: "r4", Core: "c", Provider: "codex", Status: AgentJobRunning},
		{RoomID: "r5", Core: "c", Provider: "codex", Status: AgentJobReplied},
		{RoomID: "r6", Core: "c", Provider: "codex", Status: AgentJobQueued, OwnerUserID: "someone-else-" + sfx},
	} {
		tnt := "t-pj-" + sfx
		if j.TenantID != "" {
			tnt += "-" + j.TenantID
		}
		if j.OwnerUserID == "" {
			j.OwnerUserID = owner
		}
		j.JobID = "job-pj-" + sfx + "-" + itoaBase(int64(i))
		j.RoomID += "-" + sfx
		j.MessageSeq = int64(i + 1)
		j.RequesterUserID = "bob"
		if _, err := jobs.PutAgentJob(tenantCtx(tnt), j); err != nil {
			t.Fatalf("PutAgentJob: %v", err)
		}
	}
	got, err := jobs.PendingAgentJobs(tenantCtx("any"), owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (AgentJobCount{Core: "c", Provider: "claude", Count: 1}) || got[1] != (AgentJobCount{Core: "c", Provider: "codex", Count: 2}) {
		t.Fatalf("pending = %+v", got)
	}
}

func TestMemoryPendingAgentJobs(t *testing.T) { pendingJobCases(t, NewMemoryBridgeJobs(), "m") }

func TestMySQLPendingAgentJobs(t *testing.T) {
	b := mysqlBackendsForTest(t)
	pendingJobCases(t, b.BridgeJobs, runSuffix())
}
