package bridge

import (
	"context"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
)

// cutGuidance splits text that opens with session guidance
// (roomlog.SessionGuidance, under any facts) into the facts it was rendered
// with and what follows.
func cutGuidance(text string) (facts roomlog.Guidance, rest string, ok bool) {
	self := ""
	if m := guidanceSelf.FindStringSubmatch(text); m != nil {
		self = m[1]
	}
	channel := ""
	if m := guidanceChannel.FindStringSubmatch(text); m != nil {
		channel = m[1]
	}
	for _, g := range []roomlog.Guidance{{HasTools: true, HasRoomFiles: true}, {HasTools: true}, {HasRoomFiles: true}, {}} {
		g.Self = self
		if g.HasRoomFiles {
			g.Channel = channel
		}
		if rest, ok := strings.CutPrefix(text, strings.TrimRight(roomlog.SessionGuidance(g), "\n")); ok {
			return g, rest, true
		}
	}
	return roomlog.Guidance{}, text, false
}

// guidanceSelf reads the agent a session's guidance names.
var guidanceSelf = regexp.MustCompile(`^\[How this bnbloc room works\]\nYou are the agent "([^"]*)"`)

// guidanceChannel reads the own channel's records a session's guidance
// names.
var guidanceChannel = regexp.MustCompile(`this conversation's: (channels/[^/\s]*/)\.`)

// profileContent is a session profile's instructions after the guidance
// every profile opens with (sessionGuidance); it fails when the guidance is
// not there.
func profileContent(t *testing.T, p *corepkg.AgentProfile) string {
	t.Helper()
	if p == nil {
		t.Fatal("no profile")
	}
	_, rest, ok := cutGuidance(p.Instructions)
	if !ok {
		t.Fatalf("the profile does not open with the session guidance:\n%s", p.Instructions)
	}
	return strings.TrimSpace(rest)
}

// profileProv is a pull-capable core that advertises agentProfile (unless
// noProfile) and records the profile of every session create.
type profileProv struct {
	fakeCoreProvider
	noProfile bool
	// noFiles: the core takes no workspace files (the push model).
	noFiles bool

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
		return !p.noFiles
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
			MCPServers: []corepkg.MCPServer{{Name: "room", Type: corepkg.MCPHTTP, URL: "http://127.0.0.1:1/mcp", Headers: []corepkg.NameValue{{Name: "Authorization", Value: "Bearer per-session"}}}},
			RoomTools:  true,
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
	// The room tools' usage is session guidance (HasTools), worded by
	// roomlog alone: the contributor adds none of its own.
	if facts, _, _ := cutGuidance(p.Instructions); len(p.MCPServers) != 1 || p.MCPServers[0].Name != "room" || profileContent(t, p) != "" ||
		!reflect.DeepEqual(facts, roomlog.Guidance{Self: "maya/agent-a", HasTools: true, HasRoomFiles: true, Channel: roomlog.GuidanceChannel("ch")}) {
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

// roomModelMarker is a line of the room model (roomlog.SessionGuidance).
const roomModelMarker = "Every agent works in its own workspace"

// The room model is session guidance: a session created on an agentProfile
// core carries it in its profile (once, at create), and no turn's prompt
// repeats it.
func TestRoomModelRidesTheProfile(t *testing.T) {
	prov := &profileProv{}
	b := newProfileBridge(t, prov)
	for i, text := range []string{"first", "second"} {
		if _, err := b.driveClaim(context.Background(), testDrive(b, jobRef{ID: "job-" + text}, "r1", "maya", "core-a", "agent-a", "t", text, "ch"), nil); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
	}
	ps := prov.recordedProfiles()
	if len(ps) != 1 {
		t.Fatalf("profiles = %+v, want one session", ps)
	}
	if rest := profileContent(t, ps[0]); rest != "" {
		t.Fatalf("a session without content carries %q after the room model", rest)
	}
	turns := append(prov.recordedPrompts(), prov.recordedInputs()...)
	if len(turns) != 2 {
		t.Fatalf("turn prompts = %q", turns)
	}
	for _, p := range turns {
		if strings.Contains(p, roomModelMarker) {
			t.Fatalf("a turn's prompt repeats the room model the profile carries:\n%s", p)
		}
	}
}

// A core without agentProfile gets the room model at the head of its
// session's first prompt only, pull model and push model alike; a follow-up
// turn's prompt does not carry it.
func TestRoomModelInFirstPromptOnlyWithoutProfile(t *testing.T) {
	for _, tc := range []struct {
		name string
		push bool
	}{{"pull", false}, {"push", true}} {
		t.Run(tc.name, func(t *testing.T) {
			prov := &profileProv{noProfile: true}
			prov.failWrites = tc.push // no .room/ files: the pushed context
			b := newProfileBridge(t, prov)
			for i, text := range []string{"first", "second"} {
				if _, err := b.driveClaim(context.Background(), testDrive(b, jobRef{ID: "job-" + text}, "r1", "maya", "core-a", "agent-a", "t", text, "ch"), nil); err != nil {
					t.Fatalf("turn %d: %v", i, err)
				}
			}
			if ps := prov.recordedProfiles(); len(ps) != 1 || ps[0] != nil {
				t.Fatalf("profiles = %+v, want one session without a profile", ps)
			}
			first, follow := prov.recordedPrompts(), prov.recordedInputs()
			if len(first) != 1 || len(follow) != 1 {
				t.Fatalf("first prompts %q, follow-ups %q", first, follow)
			}
			if _, _, ok := cutGuidance(first[0]); !ok || strings.Count(first[0], roomModelMarker) != 1 {
				t.Fatalf("the first prompt does not open with the room model (once):\n%s", first[0])
			}
			if !strings.Contains(first[0], "first") {
				t.Fatalf("the first prompt lost its message:\n%s", first[0])
			}
			if strings.Contains(follow[0], roomModelMarker) || !strings.Contains(follow[0], "second") {
				t.Fatalf("the follow-up turn's prompt carries the room model (or lost its message):\n%s", follow[0])
			}
		})
	}
}

// A push session that starts in an empty room still gets its session
// guidance (2026-10-13: Compose returned the bare message when there was no
// history, and the guidance used to ride that composition): the first
// prompt is the guidance, then the message under its [now] marker. No
// .room/ legend: the core takes no workspace files.
func TestPushSessionInEmptyRoomGetsGuidance(t *testing.T) {
	prov := &profileProv{noProfile: true, noFiles: true}
	b := newProfileBridge(t, prov) // an empty room
	if _, err := b.driveClaim(context.Background(), testDrive(b, jobRef{ID: "job-1", RequesterUserID: "maya"}, "r1", "maya", "core-a", "agent-a", "t", "hello", "ch"), nil); err != nil {
		t.Fatal(err)
	}
	first := prov.recordedPrompts()
	if len(first) != 1 {
		t.Fatalf("first prompts = %q", first)
	}
	want := roomlog.SessionGuidance(roomlog.Guidance{Self: "maya/agent-a"}) + "\n---\n\n[now — from maya]\nhello"
	if first[0] != want {
		t.Fatalf("first prompt in an empty room =\n%s\nwant\n%s", first[0], want)
	}
}
