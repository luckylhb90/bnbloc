package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/schedule"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/services/bridge/roomtools"
)

// mcpCall posts one tools/call to an attached room tools server with the
// credential the session was given and returns the tool's text and error flag.
func mcpCall(t *testing.T, server corepkg.MCPServer, token, tool string, args map[string]any) (int, string, bool) {
	t.Helper()
	buf, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
	req, _ := http.NewRequest(http.MethodPost, server.URL, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	text := ""
	if len(out.Result.Content) > 0 {
		text = out.Result.Content[0].Text
	}
	return resp.StatusCode, text, out.Result.IsError
}

// Each session gets its own server attachment: http with a bearer header
// where the agent accepts http MCP, the stdio shim (bearer in env, never in
// argv) otherwise. A turn binds the bearer to its job; the release revokes it.
func TestRoomToolsContribution(t *testing.T) {
	g := newActivityGateway(t, "room-1")
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "alice"})
	stop, err := b.EnableRoomTools("/opt/bnbloc/bnbloc-bridge")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	contribs := b.sessionContributors()
	if len(contribs) != 1 {
		t.Fatalf("contributors = %d", len(contribs))
	}
	info := SessionInfo{RoomID: "room-1", ChannelID: "ch", Owner: "alice", Core: "core-a", Provider: "codex", JobID: "job-1", RequesterUserID: "bob"}

	stdio, err := contribs[0].ContributeSession(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	s := stdio.MCPServers[0]
	if s.Name != roomtools.ServerName || s.Transport() != corepkg.MCPStdio || s.Command != "/opt/bnbloc/bnbloc-bridge" ||
		len(s.Args) != 3 || s.Args[0] != "roomtools-stdio" || s.Args[1] != "--url" || len(s.Env) != 1 || s.Env[0].Name != roomtools.TokenEnv {
		t.Fatalf("stdio attachment = %+v", s)
	}
	for _, a := range s.Args {
		if a == s.Env[0].Value {
			t.Fatal("the bearer leaked into argv")
		}
	}
	if !stdio.RoomTools || stdio.Turn == nil || stdio.Release == nil {
		t.Fatalf("contribution = %+v; want the room tools, a turn hook and a release", stdio)
	}

	info.MCPTransports = []string{corepkg.MCPStdio, corepkg.MCPHTTP}
	httpPart, err := contribs[0].ContributeSession(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	h := httpPart.MCPServers[0]
	if h.Transport() != corepkg.MCPHTTP || h.URL == "" || len(h.Headers) != 1 || h.Headers[0].Name != "Authorization" {
		t.Fatalf("http attachment = %+v", h)
	}
	token := h.Headers[0].Value[len("Bearer "):]
	if token == s.Env[0].Value {
		t.Fatal("two sessions share a bearer")
	}

	// Before any turn the bearer works but dispatches nothing.
	if code, text, isErr := mcpCall(t, h, token, "ask_agent", map[string]any{"handle": "bob/claude", "task": "x"}); code != 200 || !isErr {
		t.Fatalf("unbound ask_agent = %d %s", code, text)
	}
	httpPart.Turn(TurnInfo{JobID: "job-2", RequesterUserID: "bob", ChannelID: "ch", MessageSeq: 7})
	// Bound: read_room reads the turn's channel (the fake room is empty).
	if code, text, isErr := mcpCall(t, h, token, "read_room", nil); code != 200 || isErr {
		t.Fatalf("bound read_room = %d %s", code, text)
	}
	httpPart.Release()
	if code, _, _ := mcpCall(t, h, token, "read_room", nil); code != http.StatusUnauthorized {
		t.Fatalf("released bearer = %d; want 401", code)
	}
}

// A room-credential MCP tool (the room is its credential) gets the room
// relay's address and the session's own bearer in its env, nothing else:
// never the bridge's gateway session. Servers that are not room-credential
// tools get nothing.
func TestRoomRelayReachesRoomCredentialTools(t *testing.T) {
	g := newActivityGateway(t, "room-1")
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "gateway-session", OwnerSubject: "alice"})
	stop, err := b.EnableRoomTools("")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	info := SessionInfo{RoomID: "room-1", Owner: "alice", Core: "core-a", Provider: "codex", JobID: "job-1", RequesterUserID: "alice",
		MCPTransports: []string{corepkg.MCPStdio, corepkg.MCPHTTP}, RoomRelayServers: []string{"media"}}
	p := &corepkg.AgentProfile{MCPServers: []corepkg.MCPServer{
		{Name: "media", Type: corepkg.MCPStdio, Command: "/tools/media"},
		{Name: "codeword", Type: corepkg.MCPStdio, Command: "/tools/codeword"},
	}}
	b.contribute(context.Background(), info, p)
	media, other := serverNamed(p, "media"), serverNamed(p, "codeword")
	env := map[string]string{}
	for _, e := range media.Env {
		env[e.Name] = e.Value
	}
	room := serverNamed(p, roomtools.ServerName)
	if room == nil || len(room.Headers) != 1 {
		t.Fatalf("room tools = %+v", room)
	}
	bearer := room.Headers[0].Value[len("Bearer "):]
	if len(env) != 2 || env[pkgmeta.RoomRelayTokenEnv] != bearer || !strings.HasSuffix(env[pkgmeta.RoomRelayURLEnv], roomtools.RelayPath) {
		t.Fatalf("media env = %+v", media.Env)
	}
	if len(other.Env) != 0 {
		t.Fatalf("codeword got the relay: %+v", other.Env)
	}
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "gateway-session") {
		t.Fatal("the gateway session reached the session's profile")
	}
}

// A dispatched job reaches the target bridge as the requester's job: the
// requester is the human the dispatch ran under, so the drive is a borrow
// and reserves against the owner's authorization before anything runs,
// exactly like a typed message.
func TestDispatchedJobStillReservesTheBorrow(t *testing.T) {
	g := newBorrowGateway(t, "room-1")
	prov := &jobProv{recordingProv: recordingProv{targets: []corepkg.Target{{ID: "c", Provider: "codex"}}}, messages: []corepkg.Message{reply(1, "done", nil)}}
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov)})
	// The job exactly as realtime queues it for a dispatch (and the claim
	// hands it over): maya's request, dispatched by job_parent.
	raw, _ := json.Marshal(store.AgentJob{
		JobID: "job-dispatched", RoomID: "room-1", ChannelID: "ch", MessageSeq: 41, RequesterUserID: "maya",
		OwnerUserID: "alice", Core: "core-a", Provider: "codex", MessageText: "review the parser change",
		Status: store.AgentJobClaimed, DispatchedByJobID: "job_parent", DispatchDepth: 1,
	})
	var job bridgeJob
	if err := json.Unmarshal(raw, &job); err != nil {
		t.Fatal(err)
	}
	b.runBridgeJob(context.Background(), job)
	g.waitRecord(t)
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.reserves) != 1 || g.reserves[0]["borrower"] != "maya" {
		t.Fatalf("reservations = %v; want one for borrower maya", g.reserves)
	}
}

// A turn its requester typed is ByRequester; a schedule's post (its slot
// client id) or another agent's dispatch is not, though both run as the
// requester. The room tools session carries it (the relay's session route).
func TestTurnByRequester(t *testing.T) {
	typed := bridgeJob{JobID: "j1", RequesterUserID: "maya", ClientMsgID: "c-123"}
	scheduled := bridgeJob{JobID: "j2", RequesterUserID: "maya", ClientMsgID: schedule.RunSlotClientMsgID("sch_1", time.UnixMilli(1000))}
	dispatched := bridgeJob{JobID: "j3", RequesterUserID: "maya", DispatchedByJobID: "j1"}
	if !typed.byRequester() || scheduled.byRequester() || dispatched.byRequester() {
		t.Fatalf("typed %v scheduled %v dispatched %v", typed.byRequester(), scheduled.byRequester(), dispatched.byRequester())
	}
	var raw struct {
		ClientMsgID       string `json:"clientMsgId"`
		DispatchedByJobID string `json:"dispatchedByJobId"`
	}
	// The claim reply's job carries both fields under these names
	// (wire.AgentJob / store.AgentJob).
	b, _ := json.Marshal(store.AgentJob{ClientMsgID: "c-1", DispatchedByJobID: "j0"})
	if err := json.Unmarshal(b, &raw); err != nil || raw.ClientMsgID != "c-1" || raw.DispatchedByJobID != "j0" {
		t.Fatalf("claim fields = %+v %v", raw, err)
	}
}
