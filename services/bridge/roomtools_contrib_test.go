package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
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

// A room relay's call lasts as long as the tool call that made it
// (docs/prd/2026-10-19-video-generation.md §2.4): it outlives the bridge's
// short-call limit, which still ends every other room tool's call, and its
// context still ends it.
func TestRoomRelayCallHasNoFixedLimit(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond) // a picture still drawing
		_, _ = w.Write([]byte(`{"images":[]}`))
	}))
	defer gw.Close()
	b := New(Config{GatewayBaseURL: gw.URL, GatewayToken: "t", OwnerSubject: "alice"})
	b.http = &http.Client{Timeout: 100 * time.Millisecond} // the short-call limit, shortened
	g := roomToolsGateway{b}
	call := func(ctx context.Context, relay bool) error {
		_, err := g.Do(ctx, http.MethodPost, "/v1/media/image?room=room-1&job=job-1", roomtools.RawBody{ContentType: "application/json", Data: []byte(`{}`), Relay: relay})
		return err
	}
	if err := call(context.Background(), true); err != nil {
		t.Fatalf("a relayed call longer than the short-call limit was cut: %v", err)
	}
	if err := call(context.Background(), false); err == nil {
		t.Fatal("a room tool's own call must keep the short-call limit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := call(ctx, true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the tool call's end must end the relayed call: %v", err)
	}
}

// An agent that runs inside the sandbox cannot reach this computer's loopback (its own is the
// guest's), so it is handed the proxy port by the name its guest has for the host; an agent on this
// computer whose commands only run in the sandbox keeps the loopback address. dsh stops its whole
// session on an MCP server it cannot reach, so a loopback address in the guest meant no session at all.
func TestRoomToolsAGuestAgentIsHandedAnAddressItCanReach(t *testing.T) {
	g := newActivityGateway(t, "room-1")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxy := ln.Addr().String()
	_ = ln.Close()
	_, port, _ := net.SplitHostPort(proxy)
	guestHost := "host.vm.internal:" + port
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "alice", StateDir: t.TempDir(),
		Sandbox: SandboxConfig{HostProxy: guestHost}})
	// hostProxyPort reads the port from the guest's name; the proxy listens where the bridge says.
	stop, err := b.EnableRoomTools("/opt/bnbloc/bnbloc-bridge")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	c := b.sessionContributors()[0]
	base := SessionInfo{RoomID: "room-1", ChannelID: "ch", Owner: "alice", Core: "core-a", Provider: "dsh", JobID: "job-1", RequesterUserID: "alice",
		MCPTransports: []string{corepkg.MCPStdio, corepkg.MCPHTTP}}

	// On this computer, commands in the sandbox: the loopback address.
	onHost := base
	onHost.Sandboxed = true
	part, err := c.ContributeSession(context.Background(), onHost)
	if err != nil {
		t.Fatal(err)
	}
	if u := part.MCPServers[0].URL; !strings.HasPrefix(u, "http://127.0.0.1:") {
		t.Errorf("an agent on this computer is handed %q, want the loopback address", u)
	}

	// In the guest: the proxy port, by the guest's name for the host, for the room tools...
	inGuest := base
	inGuest.Sandboxed, inGuest.InGuest = true, true
	part, err = c.ContributeSession(context.Background(), inGuest)
	if err != nil {
		t.Fatal(err)
	}
	if u := part.MCPServers[0].URL; u != "http://"+guestHost+roomtools.Endpoint {
		t.Errorf("a guest agent's room tools = %q, want http://%s%s", u, guestHost, roomtools.Endpoint)
	}
	// ...whatever transports are known of it: a guest-only agent's are unknown until its first session
	// has run, and the stdio shim is this computer's program, which does not exist in the guest.
	// dsh stopped its whole session on that shim ("initial connection or tool synchronization failed").
	unknown := inGuest
	unknown.MCPTransports = nil
	part, err = c.ContributeSession(context.Background(), unknown)
	if err != nil {
		t.Fatal(err)
	}
	if s := part.MCPServers[0]; s.Transport() != corepkg.MCPHTTP || s.URL != "http://"+guestHost+roomtools.Endpoint {
		t.Errorf("a guest agent of unknown transports is given %+v, want the http guest address, never the shim", s)
	}
	// ...and for each host-placed tool.
	withTool := inGuest
	withTool.HostTools = []HostTool{{Server: mailerServer()}}
	part, err = c.ContributeSession(context.Background(), withTool)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, s := range part.MCPServers {
		if s.Name == "mailer" {
			found = true
			if s.URL != "http://"+guestHost+roomtools.ToolsPath+"/mailer" {
				t.Errorf("a guest agent's host tool = %q", s.URL)
			}
		}
	}
	if !found {
		t.Error("the host tool was not handed to the guest agent")
	}
}

// Where the guest cannot reach the host at all (the sandbox has no host proxy), a guest agent is
// handed no room tools rather than an address that cannot work.
func TestRoomToolsAGuestAgentWithNoHostPortGetsNoUnreachableAddress(t *testing.T) {
	g := newActivityGateway(t, "room-1")
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "alice"})
	stop, err := b.EnableRoomTools("/opt/bnbloc/bnbloc-bridge")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	info := SessionInfo{RoomID: "room-1", ChannelID: "ch", Owner: "alice", Core: "core-a", Provider: "dsh", JobID: "job-1", RequesterUserID: "alice", Sandboxed: true, InGuest: true}
	if _, err := b.sessionContributors()[0].ContributeSession(context.Background(), info); err == nil {
		t.Fatal("a guest agent with no host port was handed an address anyway")
	}
}

// The tools the bridge serves reach a guest-only agent through the sandbox's host port. When there
// is none, the run goes on without them and says so in the run record: not silently.
func TestAGuestOnlyAgentWithNoHostPortHasItsMissingToolsReported(t *testing.T) {
	b := New(Config{GatewayBaseURL: "http://127.0.0.1:1", GatewayToken: "t", OwnerSubject: "alice"})
	b.targetCache = map[string][]corepkg.Target{"core-a": {{ID: "dsh", Provider: "dsh", RequiresSandbox: true}, {ID: "codex", Provider: "codex"}}}
	drive := func(target string) *drive {
		return &drive{job: job("job-1", "alice"), owner: "alice", core: "core-a", target: target, content: &jobContent{}}
	}
	if got := b.guestToolsUnreachable(drive("dsh")); !strings.Contains(got, "no host port") {
		t.Errorf("a guest-only agent with no host port: reason %q, want it said", got)
	}
	if got := b.guestToolsUnreachable(drive("codex")); got != "" {
		t.Errorf("an agent on this computer: reason %q, want none (its tools are at its own loopback)", got)
	}
	b.cfg.Sandbox = SandboxConfig{HostProxy: "host.vm.internal:41234"}
	if got := b.guestToolsUnreachable(drive("dsh")); got != "" {
		t.Errorf("a guest-only agent with a host port: reason %q, want none", got)
	}
	// And bindTarget puts it in the run's missing list, where the room reads it.
	b.cfg.Sandbox = SandboxConfig{}
	d := drive("dsh")
	d.job.Provider = "dsh"
	if r := b.bindTarget(context.Background(), d); r != nil {
		t.Fatalf("bindTarget refused: %+v", r)
	}
	if len(d.content.missing) != 1 || !strings.Contains(d.content.missing[0], "room tools") {
		t.Errorf("missing = %v, want the tools named as left out", d.content.missing)
	}
}
