package bridge

import (
	"context"
	"sync"
	"testing"

	corepkg "github.com/luckylhb90/bnbloc/core"
)

// profileProv is a pull-capable core that advertises agentProfile (unless
// noProfile) and records the profile of every session create.
type profileProv struct {
	fakeCoreProvider
	noProfile bool

	pmu      sync.Mutex
	profiles []*corepkg.AgentProfile
	inputs   []string
	// started reports a just-started turn busy once, so the drive does not
	// wait out waitTurnStart's deadline.
	started bool
}

func (p *profileProv) PollTurn(context.Context, corepkg.Endpoint, string, string) (corepkg.TurnStatus, error) {
	p.pmu.Lock()
	defer p.pmu.Unlock()
	if p.started {
		p.started = false
		return corepkg.TurnStatus{}, nil
	}
	return corepkg.TurnStatus{Idle: true}, nil
}

func (p *profileProv) Supports(_ context.Context, _ corepkg.Endpoint, c corepkg.Capability) bool {
	switch c {
	case corepkg.CapabilityWriteFile:
		return true
	case corepkg.CapabilityAgentProfile:
		return !p.noProfile
	}
	return false
}

func (p *profileProv) CreateSession(ctx context.Context, ep corepkg.Endpoint, req corepkg.SessionRequest) (corepkg.SessionResult, error) {
	p.pmu.Lock()
	p.profiles = append(p.profiles, req.Profile)
	n := len(p.profiles)
	p.started = true
	p.pmu.Unlock()
	if _, err := p.fakeCoreProvider.CreateSession(ctx, ep, req); err != nil {
		return corepkg.SessionResult{}, err
	}
	return corepkg.SessionResult{SessionID: "s" + string(rune('0'+n))}, nil
}

func (p *profileProv) SendInput(ctx context.Context, ep corepkg.Endpoint, w, s, text string, opts corepkg.SessionOptions) error {
	p.pmu.Lock()
	p.inputs = append(p.inputs, text)
	p.started = true
	p.pmu.Unlock()
	return nil
}

func (p *profileProv) recordedProfiles() []*corepkg.AgentProfile {
	p.pmu.Lock()
	defer p.pmu.Unlock()
	return append([]*corepkg.AgentProfile(nil), p.profiles...)
}

func (p *profileProv) recordedInputs() []string {
	p.pmu.Lock()
	defer p.pmu.Unlock()
	return append([]string(nil), p.inputs...)
}

func newProfileBridge(t *testing.T, prov *profileProv) *Bridge {
	t.Helper()
	g := newPullGateway(t)
	g.setHistory("general", nil, nil)
	g.setHistory("test", nil, nil)
	prov.url = g.srv.URL
	return New(Config{
		GatewayBaseURL: g.srv.URL,
		GatewayToken:   "t",
		OwnerSubject:   "maya",
		Cores:          []CoreConfig{{Name: "core-a", Provider: prov, Endpoint: corepkg.Endpoint{BaseURL: g.srv.URL}}},
	})
}

// A session contributor's MCP server reaches an agentProfile core's session
// create; its turn hook sees every turn's job and requester; its release
// runs when the binding is dropped.
func TestSessionContributorReachesProfileCore(t *testing.T) {
	prov := &profileProv{}
	b := newProfileBridge(t, prov)
	var mu sync.Mutex
	var infos []SessionInfo
	var turns []TurnInfo
	released := make(chan struct{}, 1)
	b.AddSessionContributor(SessionContributorFunc(func(_ context.Context, s SessionInfo) (SessionContribution, error) {
		mu.Lock()
		infos = append(infos, s)
		mu.Unlock()
		return SessionContribution{
			MCPServers:   []corepkg.MCPServer{{Name: "room", Type: corepkg.MCPHTTP, URL: "http://127.0.0.1:1/mcp", Headers: []corepkg.NameValue{{Name: "Authorization", Value: "Bearer per-session"}}}},
			Instructions: "Use the room tools to reach other agents.",
			Turn: func(t TurnInfo) {
				mu.Lock()
				turns = append(turns, t)
				mu.Unlock()
			},
			Release: func() { released <- struct{}{} },
		}, nil
	}))

	b.cacheTargets("core-a", []corepkg.Target{{ID: "t", Provider: "agent-a", MCPTransports: []string{corepkg.MCPStdio, corepkg.MCPHTTP}}})
	job1 := jobRef{ID: "job-1", RequesterUserID: "jack", TenantID: "t1", MessageSeq: 4}
	if _, err := b.driveClaim(context.Background(), testDrive(b, job1, "r1", "maya", "core-a", "agent-a", "t", "first", "ch"), nil); err != nil {
		t.Fatal(err)
	}
	job2 := jobRef{ID: "job-2", RequesterUserID: "maya", TenantID: "t1", MessageSeq: 9}
	if _, err := b.driveClaim(context.Background(), testDrive(b, job2, "r1", "maya", "core-a", "agent-a", "t", "second", "ch"), nil); err != nil {
		t.Fatal(err)
	}

	profiles := prov.recordedProfiles()
	if len(profiles) != 1 || profiles[0] == nil {
		t.Fatalf("profiles = %+v", profiles)
	}
	p := profiles[0]
	if len(p.MCPServers) != 1 || p.MCPServers[0].Name != "room" || p.Instructions != "Use the room tools to reach other agents." {
		t.Fatalf("profile = %+v", p)
	}
	mu.Lock()
	if len(infos) != 1 || len(infos[0].MCPTransports) != 2 || infos[0].MCPTransports[1] != corepkg.MCPHTTP {
		t.Fatalf("session transports = %+v", infos)
	}
	if len(infos) != 1 || infos[0].JobID != "job-1" || infos[0].RequesterUserID != "jack" || infos[0].RoomID != "r1" || infos[0].Owner != "maya" {
		t.Fatalf("session info = %+v", infos)
	}
	if len(turns) != 2 || turns[0].JobID != "job-1" || turns[1].JobID != "job-2" || turns[1].RequesterUserID != "maya" || turns[1].MessageSeq != 9 {
		t.Fatalf("turns = %+v", turns)
	}
	mu.Unlock()

	b.mu.Lock()
	b.forgetRoom("r1")
	b.mu.Unlock()
	<-released
}

// A core without agentProfile never receives a profile, and contributors
// are not consulted (nothing could carry their MCP servers).
func TestSessionContributorSkipsCoreWithoutProfile(t *testing.T) {
	prov := &profileProv{noProfile: true}
	b := newProfileBridge(t, prov)
	called := false
	b.AddSessionContributor(SessionContributorFunc(func(context.Context, SessionInfo) (SessionContribution, error) {
		called = true
		return SessionContribution{MCPServers: []corepkg.MCPServer{{Name: "x", Command: "x"}}}, nil
	}))
	if _, err := b.driveClaim(context.Background(), testDrive(b, jobRef{ID: "job-1"}, "r1", "maya", "core-a", "agent-a", "t", "go", "ch"), nil); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("contributor consulted for a core without agentProfile")
	}
	if ps := prov.recordedProfiles(); len(ps) != 1 || ps[0] != nil {
		t.Fatalf("profiles = %+v", ps)
	}
}
