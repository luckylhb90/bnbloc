package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// teamGateway fronts the content fixture with the routes a team turn and a
// borrowed drive add: the team plan (recorded) and the borrow reservation
// (recorded, always granted).
type teamGateway struct {
	*contentFixtureGateway
	url string

	mu       sync.Mutex
	plans    []string // request paths with query
	planBody wire.TeamPlanRequest
	reserves []map[string]any
	plan     wire.TeamPlanReply
}

func newTeamGateway(t *testing.T, plan wire.TeamPlanReply) *teamGateway {
	t.Helper()
	g := &teamGateway{contentFixtureGateway: newContentFixtureGateway(t), plan: plan}
	target, _ := url.Parse(g.srv.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/rooms/r1/team-plan":
			raw, _ := io.ReadAll(r.Body)
			g.mu.Lock()
			g.plans = append(g.plans, r.URL.RequestURI())
			_ = json.Unmarshal(raw, &g.planBody)
			plan := g.plan
			g.mu.Unlock()
			_ = json.NewEncoder(w).Encode(plan)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/authorization/reserve"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			body["path"] = r.URL.RequestURI()
			g.mu.Lock()
			g.reserves = append(g.reserves, body)
			g.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"reserved": true, "reservationId": "rsv-1"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/authorization/record"):
		default:
			proxy.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	g.url = srv.URL
	return g
}

func (g *teamGateway) bridge(prov corepkg.Provider, owner string) *Bridge {
	return New(Config{GatewayBaseURL: g.url, GatewayToken: "t", OwnerSubject: owner,
		Cores: []CoreConfig{{Name: "core-a", Provider: prov, Endpoint: corepkg.Endpoint{BaseURL: g.url}}}})
}

// turnRecorder is a session contributor that keeps every turn it sees.
type turnRecorder struct {
	mu    sync.Mutex
	turns []TurnInfo
}

func (r *turnRecorder) ContributeSession(context.Context, SessionInfo) (SessionContribution, error) {
	return SessionContribution{Turn: func(t TurnInfo) {
		r.mu.Lock()
		r.turns = append(r.turns, t)
		r.mu.Unlock()
	}}, nil
}

func teamForm() contentpkg.Form {
	return contentpkg.Form{Kind: "expert", Name: "release-team", DisplayName: "Release team", Instructions: "SOP: plan, delegate, merge.",
		Team: &contentpkg.TeamSpec{Lead: "coordinator", Members: []contentpkg.TeamMember{{Role: "reviewer", Expert: "code-reviewer@^1", DefaultAgent: "member:bob"}}}}
}

func teamEffective(g *teamGateway) wire.ContentEffective {
	expert := g.pkg(teamForm(), "1.0.0")
	expert.Source = wire.ContentSourceExpert
	return wire.ContentEffective{Expert: &expert, Team: &wire.ContentTeam{Lead: "coordinator", Members: []wire.ContentTeamMember{{
		Role: "reviewer", Expert: "code-reviewer@^1", DefaultAgent: "member:bob",
		Resolved: &wire.ContentTeamExpert{ComponentID: "cc_rev", Name: "code-reviewer", DisplayName: "Code reviewer", Description: "Reviews diffs.", Version: "1.2.0"},
	}}}}
}

func reviewerPlan() wire.TeamPlanReply {
	return wire.TeamPlanReply{
		Lead:  wire.TeamPlanEntry{Agent: &wire.PlanAgent{OwnerID: "maya", Core: "core-a", Provider: "agent-a", Handle: "maya/agent-a"}, Online: true},
		Roles: []wire.TeamPlanEntry{{Role: "reviewer", DefaultAgent: "member:bob", Agent: &wire.PlanAgent{OwnerID: "bob", Core: "core-a", Provider: "agent-b", Handle: "bob/agent-b"}, Online: true}},
	}
}

// A team job: the lead's bridge staffs the roles for the job, wears the
// SOP plus the generated roster section, and binds the roster to the turn
// (the room tools dispatch to roles from it).
func TestTeamLeadWearsSOPAndRoster(t *testing.T) {
	g := newTeamGateway(t, reviewerPlan())
	eff := teamEffective(g)
	g.setEffective("job-1", eff)
	prov := &profileProv{}
	b := g.bridge(prov, "maya")
	rec := &turnRecorder{}
	b.AddSessionContributor(rec)

	b.runBridgeJob(context.Background(), contentJob("job-1", eff.Expert.ComponentID))

	g.mu.Lock()
	plans, body := g.plans, g.planBody
	g.mu.Unlock()
	if len(plans) != 1 || plans[0] != "/v1/rooms/r1/team-plan?job=job-1" || len(body.Members) != 1 || body.Members[0] != (wire.TeamPlanRole{Role: "reviewer", DefaultAgent: "member:bob"}) {
		t.Fatalf("plans = %v body %+v", plans, body)
	}
	ps := prov.recordedProfiles()
	if len(ps) != 1 || ps[0] == nil {
		t.Fatalf("profiles = %+v", ps)
	}
	in := ps[0].Instructions
	for _, want := range []string{"SOP: plan, delegate, merge.", "## Your team", "You are the coordinator", "- reviewer → Code reviewer v1.2.0 → bob/agent-b (online)", "- reviewer: Reviews diffs."} {
		if !strings.Contains(in, want) {
			t.Fatalf("instructions lack %q:\n%s", want, in)
		}
	}
	if strings.Index(in, "SOP:") > strings.Index(in, "## Your team") {
		t.Fatalf("the roster precedes the SOP:\n%s", in)
	}
	rec.mu.Lock()
	turns := rec.turns
	rec.mu.Unlock()
	if len(turns) != 1 || turns[0].Team == nil || len(turns[0].Team.Roles) != 1 {
		t.Fatalf("turns = %+v", turns)
	}
	r := turns[0].Team.Roles[0]
	if r.Role != "reviewer" || r.Agent == nil || r.Agent.OwnerID != "bob" || r.Expert == nil || r.Expert.ComponentID != "cc_rev" || turns[0].Team.Name != "Release team" {
		t.Fatalf("bound role = %+v (team %+v)", r, turns[0].Team)
	}
}

// A team on a core that cannot attach the room tools is refused with a
// notice: its lead could not ask any member.
func TestTeamOnACoreWithoutRoomToolsRefuses(t *testing.T) {
	g := newTeamGateway(t, reviewerPlan())
	eff := teamEffective(g)
	g.setEffective("job-1", eff)
	prov := &bytesProfileProv{profileProv: profileProv{noProfile: true}}
	b := g.bridge(prov, "maya")

	b.runBridgeJob(context.Background(), contentJob("job-1", eff.Expert.ComponentID))

	if len(prov.recordedPrompts()) != 0 {
		t.Fatal("drove a team on a core without room tools")
	}
	if st := g.lastStatus(); st["status"] != "failed" || st["failureReason"] != "team_unsupported" {
		t.Fatalf("status = %+v", st)
	}
	if n := g.allNotices(); !strings.Contains(n, "The team Release team needs an agent that can use the room tools") {
		t.Fatalf("notices = %q", n)
	}
}

// The room's expert is worn under the chat's own: its instructions first,
// and the run report names both.
func TestRoomExpertUnderTheChatExpert(t *testing.T) {
	g := newTeamGateway(t, wire.TeamPlanReply{})
	expert := g.pkg(reviewerForm("You are the reviewer."), "1.0.0")
	expert.Source = wire.ContentSourceExpert
	house := g.pkg(contentpkg.Form{Kind: "expert", Name: "house-style", DisplayName: "House style", Instructions: "Write plainly."}, "2.0.0")
	house.Source = wire.ContentSourceRoomExpert
	g.setEffective("job-1", wire.ContentEffective{Expert: &expert, RoomExpert: &house})
	g.setEffective("job-2", wire.ContentEffective{RoomExpert: &house})
	prov := &profileProv{}
	b := g.bridge(prov, "maya")

	b.runBridgeJob(context.Background(), contentJob("job-1", expert.ComponentID))
	ps := prov.recordedProfiles()
	if len(ps) != 1 || profileContent(t, ps[0]) != "Write plainly.\n\nYou are the reviewer." {
		t.Fatalf("instructions = %q", ps[0].Instructions)
	}
	var running map[string]any
	g.mu.Lock()
	for _, st := range g.statuses {
		if st["status"] == "running" {
			running, _ = st["diagnostics"].(map[string]any)
		}
	}
	g.mu.Unlock()
	if running["roomExpert"] != "House style (v2.0.0)" || running["expert"] != "Reviewer (v1.0.0)" {
		t.Fatalf("running = %+v", running)
	}
	// No chat expert: the room expert alone (a new session: other content).
	b.runBridgeJob(context.Background(), contentJob("job-2", ""))
	if ps = prov.recordedProfiles(); len(ps) != 2 || profileContent(t, ps[1]) != "Write plainly." {
		t.Fatalf("room expert alone = %+v", ps)
	}
}

// A role dispatch reaches the role agent's bridge as an ordinary job of the
// requester: borrowed (maya asked, bob owns the agent), so bob's bridge
// reserves the borrow for maya before driving, and the role's expert is
// worn.
func TestRoleDispatchIsReservedForTheRequester(t *testing.T) {
	g := newTeamGateway(t, wire.TeamPlanReply{})
	reviewer := g.pkg(contentpkg.Form{Kind: "expert", Name: "code-reviewer", DisplayName: "Code reviewer", Instructions: "Review strictly. MARKER-ROLE"}, "1.2.0")
	reviewer.Source = wire.ContentSourceExpert
	g.setEffective("job-role", wire.ContentEffective{Expert: &reviewer})
	prov := &profileProv{}
	b := g.bridge(prov, "bob")
	job := bridgeJob{JobID: "job-role", RoomID: "r1", ChannelID: "general", RequesterUserID: "maya", OwnerUserID: "bob", Core: "core-a", Provider: "agent-b",
		MessageText: "review the parser change", ExpertID: reviewer.ComponentID}

	b.runBridgeJob(context.Background(), job)

	g.mu.Lock()
	reserves := g.reserves
	g.mu.Unlock()
	if len(reserves) != 1 || reserves[0]["borrower"] != "maya" || !strings.Contains(reserves[0]["path"].(string), "owner=bob") {
		t.Fatalf("reserves = %+v; want one for maya on bob's agent", reserves)
	}
	if ps := prov.recordedProfiles(); len(ps) != 1 || !strings.Contains(ps[0].Instructions, "MARKER-ROLE") {
		t.Fatalf("profiles = %+v", ps)
	}
}
