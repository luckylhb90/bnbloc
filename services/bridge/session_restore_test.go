package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	corepkg "github.com/luckylhb90/bnbloc/core"
)

// restartProv is a core that keeps its sessions across a bridge restart:
// each turn answers one reply (versions count up per session), and a
// session in gone is no longer known.
type restartProv struct {
	recordingProv
	mu      sync.Mutex
	created int
	inputs  []string
	afters  []int64
	turns   map[string]int64
	polls   map[string]int
	gone    map[string]bool
}

func (p *restartProv) CreateSession(context.Context, corepkg.Endpoint, corepkg.SessionRequest) (corepkg.SessionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.created++
	sid := "s" + strconv.Itoa(p.created)
	if p.turns == nil {
		p.turns, p.polls = map[string]int64{}, map[string]int{}
	}
	p.turns[sid]++
	p.polls[sid] = 0
	return corepkg.SessionResult{SessionID: sid}, nil
}

func (p *restartProv) SendInput(_ context.Context, _ corepkg.Endpoint, _, sid, _ string, _ corepkg.SessionOptions) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inputs = append(p.inputs, sid)
	p.turns[sid]++
	p.polls[sid] = 0
	return nil
}

func (p *restartProv) PollTurn(_ context.Context, _ corepkg.Endpoint, _, sid string) (corepkg.TurnStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gone[sid] {
		return corepkg.TurnStatus{}, &corepkg.HTTPError{Status: http.StatusNotFound}
	}
	p.polls[sid]++
	if p.polls[sid] == 1 {
		return corepkg.TurnStatus{Idle: false}, nil
	}
	return corepkg.TurnStatus{Idle: true}, nil
}

func (p *restartProv) Messages(_ context.Context, _ corepkg.Endpoint, _, sid string, after int64) ([]corepkg.Message, int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.afters = append(p.afters, after)
	v := p.turns[sid]
	if after >= v {
		return nil, after, nil
	}
	return []corepkg.Message{{Version: v, Seq: v, Role: "assistant", Text: "reply " + strconv.FormatInt(v, 10) + " in " + sid, UserVisible: true, Completed: true, MessageID: sid + "-" + strconv.FormatInt(v, 10), CreatedAtMs: v}}, v, nil
}

// A bridge restart (the desktop restarts it whenever a core is updated)
// resumes each channel's session: the agent keeps its conversation, and
// the reflow continues from the cursor it had.
func TestSessionSurvivesABridgeRestart(t *testing.T) {
	dir := t.TempDir()
	gw := newSandboxGateway(t, "room-1", false)
	prov := &restartProv{}
	cfg := Config{GatewayBaseURL: gw.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov), StateDir: dir}
	New(cfg).runBridgeJob(context.Background(), job("job-1", "alice"))
	if raw, err := os.ReadFile(filepath.Join(dir, workspacesFile)); err != nil || !strings.Contains(string(raw), `"sessionId": "s1"`) {
		t.Fatalf("persisted bindings = %s (%v); want the session kept", raw, err)
	}

	prov.mu.Lock()
	firstRead := len(prov.afters)
	prov.mu.Unlock()
	New(cfg).runBridgeJob(context.Background(), job("job-2", "alice"))
	if prov.afters[firstRead] != 1 {
		t.Fatalf("the restored session read messages after %d; want its kept cursor 1", prov.afters[firstRead])
	}
	if prov.created != 1 || len(prov.inputs) != 1 || prov.inputs[0] != "s1" {
		t.Fatalf("created %d sessions, inputs %v; want the second turn in the kept session s1", prov.created, prov.inputs)
	}
	if st := gw.lastStatus(); st["status"] != "replied" {
		t.Fatalf("status = %v", st)
	}
	_, fresh := gw.stored()
	if texts := storedTexts(fresh); len(texts) != 2 || texts[1] != "reply 2 in s1" {
		t.Fatalf("rows = %q; want each turn's reply once", texts)
	}
}

// A kept binding the core no longer knows falls back to a new session (and
// the kept binding is replaced by it).
func TestStaleKeptSessionFallsBackToANewOne(t *testing.T) {
	dir := t.TempDir()
	gw := newSandboxGateway(t, "room-1", false)
	prov := &restartProv{}
	cfg := Config{GatewayBaseURL: gw.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov), StateDir: dir}
	New(cfg).runBridgeJob(context.Background(), job("job-1", "alice"))
	prov.mu.Lock()
	prov.gone = map[string]bool{"s1": true}
	prov.mu.Unlock()

	New(cfg).runBridgeJob(context.Background(), job("job-2", "alice"))
	if prov.created != 2 {
		t.Fatalf("created %d sessions; want a new one for the stale binding", prov.created)
	}
	if st := gw.lastStatus(); st["status"] != "replied" {
		t.Fatalf("status = %v", st)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, workspacesFile))
	if !strings.Contains(string(raw), `"sessionId": "s2"`) || strings.Contains(string(raw), `"sessionId": "s1"`) {
		t.Fatalf("persisted bindings = %s; want s2 kept in place of s1", raw)
	}
}

// Content the drive no longer wears is not restored (another expert): the
// kept binding is forgotten and a new session starts.
func TestKeptSessionOfOtherContentIsNotRestored(t *testing.T) {
	dir := t.TempDir()
	gw := newSandboxGateway(t, "room-1", false)
	prov := &restartProv{}
	cfg := Config{GatewayBaseURL: gw.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov), StateDir: dir}
	New(cfg).runBridgeJob(context.Background(), job("job-1", "alice"))
	b := New(cfg)
	sk := sessionKey{driveKey: driveKey{room: "room-1", core: "core-a", provider: "codex"}, channel: "ch"}
	rec, ok := b.workspaces.session(sk)
	if !ok {
		t.Fatal("no kept binding")
	}
	rec.ProfileSum = "another expert"
	b.workspaces.putSession(sk, rec)
	b.runBridgeJob(context.Background(), job("job-2", "alice"))
	if prov.created != 2 {
		t.Fatalf("created %d sessions; want a new one for other content", prov.created)
	}
}

// The room tools a restored session was given keep working: the server
// listens where it did, and the session's bearer derives again from the
// nonce its binding kept (the bearer itself is never persisted).
func TestRoomToolsReattachAfterARestart(t *testing.T) {
	dir := t.TempDir()
	gw := newSandboxGateway(t, "room-1", false)
	cfg := Config{GatewayBaseURL: gw.srv.URL, GatewayToken: "t", OwnerSubject: "alice", StateDir: dir}
	info := SessionInfo{TenantID: "t1", RoomID: "room-1", ChannelID: "ch", Owner: "alice", Core: "core-a", Provider: "codex", MCPTransports: []string{corepkg.MCPHTTP}}

	b1 := New(cfg)
	stop1, err := b1.EnableRoomTools("")
	if err != nil {
		t.Fatal(err)
	}
	c1 := b1.sessionContributors()[0].(*roomToolsContributor)
	part, err := c1.ContributeSession(context.Background(), info)
	if err != nil || len(part.MCPServers) != 1 || len(part.State) == 0 {
		t.Fatalf("contribution = %+v %v", part, err)
	}
	server := part.MCPServers[0]
	if strings.Contains(string(part.State), strings.TrimPrefix(server.Headers[0].Value, "Bearer ")) {
		t.Fatal("the kept state holds the bearer")
	}
	if fi, err := os.Stat(filepath.Join(dir, roomToolsKeyFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file = %v %v; want owner-only", fi, err)
	}
	stop1()

	b2 := New(cfg)
	stop2, err := b2.EnableRoomTools("")
	if err != nil {
		t.Fatal(err)
	}
	defer stop2()
	c2 := b2.sessionContributors()[0].(*roomToolsContributor)
	restored, ok := c2.RestoreSession(context.Background(), info, part.State)
	if !ok || restored.Turn == nil || restored.Release == nil {
		t.Fatalf("restore = %+v %v", restored, ok)
	}
	req, _ := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set(server.Headers[0].Name, server.Headers[0].Value)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("the agent's old bearer after the restart = %v %v; want it accepted", resp, err)
	}
	resp.Body.Close()
	if _, ok := c2.RestoreSession(context.Background(), info, []byte(`{"url":"http://127.0.0.1:1/mcp","nonce":"x"}`)); ok {
		t.Fatal("a session told another address was restored")
	}
	// A session given another tool list (a build that added or changed a tool)
	// is not restored: it keeps the list it began with, so it starts anew.
	var grant roomToolsGrant
	if err := json.Unmarshal(part.State, &grant); err != nil || grant.Tools == "" {
		t.Fatalf("the kept state names its tool list: %s %v", part.State, err)
	}
	grant.Tools = "an older list"
	older, _ := json.Marshal(grant)
	if _, ok := c2.RestoreSession(context.Background(), info, older); ok {
		t.Fatal("a session given another tool list was restored")
	}
	grant.Tools = ""
	legacy, _ := json.Marshal(grant)
	if _, ok := c2.RestoreSession(context.Background(), info, legacy); ok {
		t.Fatal("a session kept before the tool list was recorded was restored")
	}
}
