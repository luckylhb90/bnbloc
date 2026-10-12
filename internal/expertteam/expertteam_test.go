package expertteam

import (
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

func agents() []wire.PlanAgent {
	return []wire.PlanAgent{
		{OwnerID: "maya", Core: "acme", Provider: "opencode"},
		{OwnerID: "maya", Core: "acme", Provider: "codex"},
		{OwnerID: "jack", Core: "acme", Provider: "codex"},
		{OwnerID: "jack", Core: "nimbus", Provider: "codex"},
		{OwnerID: "host", Core: "acme", Provider: "claude", Platform: true},
	}
}

func TestResolveStaffsRoles(t *testing.T) {
	lead := wire.PlanAgent{OwnerID: "maya", Core: "acme", Provider: "opencode"}
	got := Resolve("maya", lead, agents(), []wire.TeamPlanRole{
		{Role: "self", DefaultAgent: "requester"},
		{Role: "plat", DefaultAgent: "platform"},
		{Role: "jack-any", DefaultAgent: "member:jack"},
		{Role: "jack-codex", DefaultAgent: "member:jack/codex"},
		{Role: "bob", DefaultAgent: "member:bob"},
		{Role: "bad", DefaultAgent: "someone"},
	})
	want := map[string]string{
		"self":       "maya/codex", // the requester's agent that is not the lead
		"plat":       "host/claude",
		"jack-any":   "jack/acme/codex", // first by core; jack exposes codex on two cores
		"jack-codex": "jack/acme/codex",
	}
	for _, e := range got {
		w, staffed := want[e.Role]
		switch {
		case staffed && (e.Agent == nil || e.Agent.Handle != w || !e.Online):
			t.Errorf("%s = %+v, want %s", e.Role, e.Agent, w)
		case !staffed && (e.Agent != nil || e.Reason == ""):
			t.Errorf("%s staffed: %+v", e.Role, e)
		}
	}
	if got[4].Reason != "bob's agent is not online in this room" {
		t.Errorf("bob reason = %q", got[4].Reason)
	}
}

func TestResolveNeverPicksTheLead(t *testing.T) {
	lead := wire.PlanAgent{OwnerID: "maya", Provider: "opencode"} // empty core matches any
	got := Resolve("maya", lead, []wire.PlanAgent{{OwnerID: "maya", Core: "acme", Provider: "opencode"}}, []wire.TeamPlanRole{{Role: "a", DefaultAgent: "requester"}})
	if got[0].Agent != nil || !strings.Contains(got[0].Reason, "is the lead itself") {
		t.Fatalf("got %+v", got[0])
	}
	got = Resolve("maya", lead, nil, []wire.TeamPlanRole{{Role: "p", DefaultAgent: "platform"}})
	if got[0].Reason != "no platform agent is added to this room" {
		t.Fatalf("platform reason = %q", got[0].Reason)
	}
}

func cost(runs int, avg float64) *wire.AgentCostSample {
	return &wire.AgentCostSample{Runs: runs, AvgCostUsd: &avg}
}

func TestEstimate(t *testing.T) {
	agent := &wire.PlanAgent{OwnerID: "x", Provider: "y"}
	lead := wire.TeamPlanEntry{Agent: agent, Cost: cost(10, 0.10)}
	roles := []wire.TeamPlanEntry{
		{Role: "a", Agent: agent, Cost: cost(5, 0.20)},
		{Role: "b", Agent: agent, Cost: &wire.AgentCostSample{Runs: 0}},
		{Role: "c"}, // unstaffed: not counted, not unknown
	}
	est := Estimate(lead, roles)
	if est.CostUsd == nil || *est.CostUsd < 0.2999 || *est.CostUsd > 0.3001 || est.Runs != 15 || len(est.Unknown) != 1 || est.Unknown[0] != "b" {
		t.Fatalf("estimate = %+v (%v)", est, est.CostUsd)
	}
	none := Estimate(wire.TeamPlanEntry{Agent: agent}, nil)
	if none.CostUsd != nil || len(none.Unknown) != 1 || none.Unknown[0] != LeadSeat {
		t.Fatalf("no data = %+v", none)
	}
}

func TestSection(t *testing.T) {
	team := wire.ContentTeam{Lead: "coordinator", Members: []wire.ContentTeamMember{
		{Role: "reviewer", Expert: "code-reviewer@^1", Resolved: &wire.ContentTeamExpert{Name: "code-reviewer", DisplayName: "Code reviewer", Version: "1.2.0", Description: "Reviews diffs."}},
		{Role: "tester", Expert: "tester@^1", Missing: "no expert tester@^1 you may use"},
		{Role: "writer", Expert: "writer", Description: "Writes notes.", Resolved: &wire.ContentTeamExpert{Name: "writer", Version: "1.0.0"}},
	}}
	plan := wire.TeamPlanReply{Roles: []wire.TeamPlanEntry{
		{Role: "reviewer", Agent: &wire.PlanAgent{Handle: "jack/codex"}, Online: true},
		{Role: "tester", Agent: &wire.PlanAgent{Handle: "host/claude"}, Online: true},
		{Role: "writer", Reason: "bob's agent is not online in this room"},
	}}
	s := Section(team, plan)
	for _, want := range []string{
		"You are the coordinator of this team",
		"- reviewer → Code reviewer v1.2.0 → jack/codex (online). Covers: Reviews diffs.",
		"- tester → tester@^1 → unavailable: no expert tester@^1 you may use",
		"- writer → writer v1.0.0 → unavailable: bob's agent is not online in this room. Covers: Writes notes.",
		"refuse that part",
		"post_progress",
		"After 3 failed member tasks",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("section lacks %q:\n%s", want, s)
		}
	}
}
