package activity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// teamFixture: alice's codex (online) ran a borrowed turn for bob and is
// running another; bob's claude (bridge gone) failed one turn.
func teamFixture(t *testing.T) *Service {
	t.Helper()
	now := time.UnixMilli(1_800_000_000_000)
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	jobs := store.NewMemoryBridgeJobs()
	act := store.NewMemory()
	rooms := store.NewMemoryRoom()
	if err := rooms.CreateRoom(ctx, store.Room{RoomID: "room-1", Name: "Design", CreatedBy: "alice", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	nowMs := now.UnixMilli()
	_ = jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: "b-alice", OwnerUserID: "alice", Status: store.BridgeOnline, LastSeenAtMs: nowMs})
	_ = jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: "b-bob", OwnerUserID: "bob", Status: store.BridgeOnline, LastSeenAtMs: nowMs - 3_600_000})
	_ = jobs.ReplaceBridgeAgents(ctx, "b-alice", []store.BridgeAgent{{OwnerUserID: "alice", Core: "c1", Provider: "codex", TargetID: "c1/local:codex"}})
	_ = jobs.ReplaceBridgeAgents(ctx, "b-bob", []store.BridgeAgent{{OwnerUserID: "bob", Core: "c1", Provider: "claude"}})
	put := func(id, owner, requester, provider string, created int64) {
		if _, err := jobs.PutAgentJob(ctx, store.AgentJob{JobID: id, RoomID: "room-1", MessageSeq: created, RequesterUserID: requester,
			OwnerUserID: owner, Core: "c1", Provider: provider, MessageText: "secret prompt", CreatedAtMs: created}); err != nil {
			t.Fatal(err)
		}
	}
	put("j-borrow", "alice", "bob", "codex", nowMs-60_000)
	put("j-running", "alice", "alice", "codex", nowMs-10_000)
	put("j-failed", "bob", "bob", "claude", nowMs-30_000)
	// Drive the jobs through the worker path (bob's bridge claims while live).
	_ = jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: "b-bob", OwnerUserID: "bob", Status: store.BridgeOnline, LastSeenAtMs: nowMs})
	for _, owner := range []string{"alice", "bob"} {
		claimed, _ := jobs.ClaimAgentJobs(ctx, store.ClaimRequest{BridgeID: "b-" + owner, OwnerUserID: owner, MaxJobs: 5, NowMs: nowMs, LeaseMs: 60_000})
		for _, j := range claimed {
			status, reason := store.AgentJobRunning, store.AgentJobFailureReason("")
			switch j.JobID {
			case "j-borrow":
				status = store.AgentJobReplied
			case "j-failed":
				status, reason = store.AgentJobFailed, store.FailureBorrowNotAuthorized
			}
			if _, err := jobs.UpdateAgentJobStatus(ctx, store.JobStatusUpdate{JobID: j.JobID, BridgeID: "b-" + owner, Status: status, FailureReason: reason, NowMs: nowMs}); err != nil {
				t.Fatal(err)
			}
		}
	}
	_ = jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: "b-bob", OwnerUserID: "bob", Status: store.BridgeOnline, LastSeenAtMs: nowMs - 3_600_000})
	if _, _, err := act.AppendMessages(ctx, "room-1", wire.ReportMessagesInput{WorkspaceID: "room-1", AgentSessionID: "s1", Messages: []wire.Message{{
		Seq: 1, CreatedAtUnixMs: nowMs - 50_000,
		Payload: map[string]any{"ownerUserId": "alice", "provider": "codex", "metrics": map[string]any{"inputTokens": 120.0, "outputTokens": 30.0, "costUsd": 0.4, "reported": true}},
	}}}); err != nil {
		t.Fatal(err)
	}
	return &Service{
		Store: act, Publisher: &capturePublisher{}, Membership: membership.NewStatic(), BridgeJobs: jobs,
		Team: jobs, Usage: act, Rooms: rooms, Now: func() time.Time { return now },
	}
}

func getTeam(t *testing.T, svc *Service, path, caller string) (int, wire.TeamAgentsReply) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if caller != "" {
		r.Header.Set("X-Bnbloc-User", caller)
	}
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, r)
	var out wire.TeamAgentsReply
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestTeamAgentsSelfScopeSeesOnlyOwnAgents(t *testing.T) {
	svc := teamFixture(t)
	code, out := getTeam(t, svc, "/v1/team/agents?range=7d", "bob")
	if code != http.StatusOK || out.Scope != "self" || len(out.Agents) != 1 || out.Agents[0].Owner != "bob" {
		t.Fatalf("bob self view = %d %+v, want only bob's agent", code, out)
	}
	b := out.Agents[0]
	if b.Online || b.FailedTurns != 1 || b.Turns != 0 || b.LastJob == nil || b.LastJob.FailureReason != "borrow_not_authorized" {
		t.Fatalf("bob's agent = %+v", b)
	}

	_, out = getTeam(t, svc, "/v1/team/agents?range=today", "alice")
	if len(out.Agents) != 1 {
		t.Fatalf("alice self view = %+v", out)
	}
	a := out.Agents[0]
	if !a.Online || a.Turns != 1 || a.BorrowedRuns != 1 || a.TargetID != "c1/local:codex" {
		t.Fatalf("alice's agent = %+v", a)
	}
	if a.CurrentJob == nil || a.CurrentJob.JobID != "j-running" || a.CurrentJob.RoomName != "Design" || a.CurrentJob.Status != "running" {
		t.Fatalf("current job = %+v", a.CurrentJob)
	}
	if a.LastJob == nil || a.LastJob.JobID != "j-borrow" || a.LastJob.Requester != "bob" {
		t.Fatalf("last job = %+v", a.LastJob)
	}
	if a.Usage == nil || a.Usage.ReportedTurns != 1 || a.Usage.InputTokens != 120 || a.Usage.CostUsd == nil || *a.Usage.CostUsd != 0.4 || a.Usage.Shared {
		t.Fatalf("usage = %+v", a.Usage)
	}
}

func TestTeamAgentsTenantScopeSeesEveryMember(t *testing.T) {
	svc := teamFixture(t)
	code, out := getTeam(t, svc, "/v1/tenant/team/agents?range=7d", "alice")
	if code != http.StatusOK || out.Scope != "tenant" || len(out.Agents) != 2 {
		t.Fatalf("tenant view = %d %+v, want both members' agents", code, out)
	}
	if out.Agents[0].Owner != "alice" || out.Agents[1].Owner != "bob" {
		t.Fatalf("order = %s, %s", out.Agents[0].Owner, out.Agents[1].Owner)
	}
	if out.Agents[1].Usage != nil {
		t.Fatalf("bob reported no metrics, usage must be omitted: %+v", out.Agents[1].Usage)
	}
}

func TestTeamAgentsRejectsAnonymousAndBadRange(t *testing.T) {
	svc := teamFixture(t)
	if code, _ := getTeam(t, svc, "/v1/team/agents", ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d, want 401", code)
	}
	for _, q := range []string{"range=30d", "range=today&tzOffsetMin=9999", "range=today&tzOffsetMin=x"} {
		if code, _ := getTeam(t, svc, "/v1/team/agents?"+q, "alice"); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", q, code)
		}
	}
	svc.Team = nil
	if code, _ := getTeam(t, svc, "/v1/team/agents", "alice"); code != http.StatusNotImplemented {
		t.Fatalf("unconfigured = %d, want 501", code)
	}
}

func TestTeamSinceTodayHonorsClientTimezone(t *testing.T) {
	// 2027-01-15T02:30Z is still the 14th at UTC-5 and already the 15th at UTC+8.
	now := time.Date(2027, 1, 15, 2, 30, 0, 0, time.UTC)
	cases := []struct {
		tz   string
		want time.Time
	}{
		{"", time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)},
		{"300", time.Date(2027, 1, 14, 5, 0, 0, 0, time.UTC)},   // UTC-5 midnight
		{"-480", time.Date(2027, 1, 14, 16, 0, 0, 0, time.UTC)}, // UTC+8 midnight
	}
	for _, c := range cases {
		_, since, ok := teamSince("today", c.tz, now)
		if !ok || since != c.want.UnixMilli() {
			t.Errorf("tz %q: since = %v, want %v", c.tz, time.UnixMilli(since).UTC(), c.want)
		}
	}
	if _, since, _ := teamSince("7d", "", now); since != now.Add(-7*24*time.Hour).UnixMilli() {
		t.Fatalf("7d since = %d", since)
	}
}
