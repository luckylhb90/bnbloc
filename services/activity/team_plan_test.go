package activity

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func postPlan(t *testing.T, svc *Service, path, caller string, body any) (int, wire.TeamPlanReply, string) {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	r.Header.Set("X-Bnbloc-User", caller)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, r)
	var out wire.TeamPlanReply
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %s: %v", w.Body.String(), err)
		}
	}
	return w.Code, out, w.Body.String()
}

// teamPlanEnv is the catalog room (alice, bob, the platform agent
// root/nimbus/codex) with finished runs that reported costs.
func teamPlanEnv(t *testing.T) *catalogEnv {
	t.Helper()
	e := newCatalogEnv(t)
	e.svc.Runs, e.svc.Leases = e.jobs, e.jobs
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	now := time.Now().UnixMilli()
	run := func(id, owner, core, provider string, cost *float64) {
		t.Helper()
		usage := map[string]any{"model": "m"}
		if cost != nil {
			usage["costUsd"] = *cost
		}
		diag, _ := json.Marshal(map[string]any{wire.RunDiagUsage: usage})
		if _, err := e.jobs.PutAgentJob(ctx, store.AgentJob{JobID: id, RoomID: "room-x", MessageSeq: int64(len(id)) + now%1000, RequesterUserID: "alice",
			OwnerUserID: owner, Core: core, Provider: provider, Status: store.AgentJobReplied, DiagnosticsJSON: diag, CreatedAtMs: now}); err != nil {
			t.Fatal(err)
		}
	}
	f := func(v float64) *float64 { return &v }
	run("r-alice", "alice", "acme", "codex", f(0.10))
	run("r-bob-1", "bob", "acme", "codex", f(0.20))
	run("r-bob-22", "bob", "acme", "codex", f(0.40))
	run("r-bob-333", "bob", "acme", "codex", nil) // no cost reported: not counted
	return e
}

func TestTeamPlanStaffsRolesAndEstimates(t *testing.T) {
	e := teamPlanEnv(t)
	code, plan, body := postPlan(t, e.svc, "/v1/rooms/room-1/team-plan", "alice", wire.TeamPlanRequest{
		Lead: &wire.PlanAgent{OwnerID: "alice", Core: "acme", Provider: "codex"},
		Members: []wire.TeamPlanRole{
			{Role: "self", DefaultAgent: "requester"},
			{Role: "tester", DefaultAgent: "member:bob"},
			{Role: "plat", DefaultAgent: "platform"},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("plan = %d %s", code, body)
	}
	if plan.Lead.Agent == nil || plan.Lead.Agent.Handle != "alice/codex" || !plan.Lead.Online || plan.Lead.Cost == nil || plan.Lead.Cost.Runs != 1 {
		t.Fatalf("lead = %+v", plan.Lead)
	}
	self, tester, plat := plan.Roles[0], plan.Roles[1], plan.Roles[2]
	if self.Agent != nil || self.Reason == "" {
		t.Fatalf("self (alice's only agent is the lead) = %+v", self)
	}
	if tester.Agent == nil || tester.Agent.Handle != "bob/codex" || tester.Cost.Runs != 2 || math.Abs(*tester.Cost.AvgCostUsd-0.30) > 1e-9 {
		t.Fatalf("tester = %+v %+v", tester.Agent, tester.Cost)
	}
	if plat.Agent == nil || plat.Agent.Handle != "root/codex" || !plat.Agent.Platform || plat.Cost.AvgCostUsd != nil {
		t.Fatalf("plat = %+v %+v", plat.Agent, plat.Cost)
	}
	est := plan.Estimate
	if est.CostUsd == nil || math.Abs(*est.CostUsd-0.40) > 1e-9 || est.Runs != 3 || len(est.Unknown) != 1 || est.Unknown[0] != "plat" {
		t.Fatalf("estimate = %+v", est)
	}
}

func TestTeamPlanForAJob(t *testing.T) {
	e := teamPlanEnv(t)
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	// bob's message asked alice's agent (the lead, borrowed) on b-alice.
	if _, err := e.jobs.PutAgentJob(ctx, store.AgentJob{JobID: "lead-job", RoomID: "room-1", MessageSeq: 9, RequesterUserID: "bob", OwnerUserID: "alice", Core: "acme", Provider: "codex",
		Status: store.AgentJobRunning, LeaseBridgeID: "b-alice", LeaseExpiresAtMs: time.Now().Add(time.Minute).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	req := wire.TeamPlanRequest{Members: []wire.TeamPlanRole{{Role: "mine", DefaultAgent: "requester"}}}
	code, plan, body := postPlan(t, e.svc, "/v1/rooms/room-1/team-plan?job=lead-job", "alice", req)
	if code != http.StatusOK {
		t.Fatalf("plan = %d %s", code, body)
	}
	// The requester is the job's (bob), so "requester" staffs bob's agent.
	if plan.Lead.Agent.Handle != "alice/codex" || plan.Roles[0].Agent == nil || plan.Roles[0].Agent.Handle != "bob/codex" {
		t.Fatalf("plan = %+v / %+v", plan.Lead.Agent, plan.Roles[0])
	}
	// Only the bridge holding the lease may plan for the job.
	if code, _, _ := postPlan(t, e.svc, "/v1/rooms/room-1/team-plan?job=lead-job", "bob", req); code != http.StatusForbidden {
		t.Fatalf("other bridge = %d", code)
	}
	if code, _, _ := postPlan(t, e.svc, "/v1/rooms/room-1/team-plan?job=nope", "alice", req); code != http.StatusNotFound {
		t.Fatalf("unknown job = %d", code)
	}
}

func TestTeamPlanRefusals(t *testing.T) {
	e := teamPlanEnv(t)
	lead := &wire.PlanAgent{OwnerID: "alice", Provider: "codex"}
	one := []wire.TeamPlanRole{{Role: "a", DefaultAgent: "platform"}}
	nine := make([]wire.TeamPlanRole, 9)
	for i := range nine {
		nine[i] = wire.TeamPlanRole{Role: "r" + string(rune('a'+i)), DefaultAgent: "platform"}
	}
	for name, c := range map[string]struct {
		caller string
		body   wire.TeamPlanRequest
		want   int
	}{
		"non member": {"dave", wire.TeamPlanRequest{Lead: lead, Members: one}, http.StatusForbidden},
		"no lead":    {"alice", wire.TeamPlanRequest{Members: one}, http.StatusBadRequest},
		"no roles":   {"alice", wire.TeamPlanRequest{Lead: lead}, http.StatusBadRequest},
		"too many":   {"alice", wire.TeamPlanRequest{Lead: lead, Members: nine}, http.StatusBadRequest},
		"bad role":   {"alice", wire.TeamPlanRequest{Lead: lead, Members: []wire.TeamPlanRole{{Role: "A B", DefaultAgent: "platform"}}}, http.StatusBadRequest},
	} {
		if code, _, body := postPlan(t, e.svc, "/v1/rooms/room-1/team-plan", c.caller, c.body); code != c.want {
			t.Errorf("%s = %d %s; want %d", name, code, body, c.want)
		}
	}
}
