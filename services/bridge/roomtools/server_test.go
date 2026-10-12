package roomtools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// fakeGateway answers the gateway paths the tools call from a handler and
// records every call.
type fakeGateway struct {
	mu     sync.Mutex
	calls  []gwCall
	handle func(method, path string, body []byte) (int, any)
}

type gwCall struct {
	Method, Path string
	Body         []byte
}

func (g *fakeGateway) Do(_ context.Context, method, path string, body any) ([]byte, error) {
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	g.mu.Lock()
	g.calls = append(g.calls, gwCall{method, path, raw})
	g.mu.Unlock()
	status, out := g.handle(method, path, raw)
	buf, _ := json.Marshal(out)
	if status < 200 || status > 299 {
		return nil, &StatusError{Status: status, Body: buf}
	}
	return buf, nil
}

func (g *fakeGateway) callsTo(method, prefix string) []gwCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []gwCall
	for _, c := range g.calls {
		if c.Method == method && strings.HasPrefix(c.Path, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// roster is room-1's roster: jack/codex (the session's own agent) and
// bob/claude.
var roster = map[string]any{"agents": []wire.RosterAgent{
	{OwnerUserID: "jack", Core: "acme", Provider: "codex"},
	{OwnerUserID: "bob", Core: "acme", Provider: "claude", Sandboxed: true},
}}

func startServer(t *testing.T, gw Gateway) (*Server, string, *Session) {
	t.Helper()
	srv, err := Start(Config{Gateway: gw, GatewayBaseURL: "https://bnbloc.example.com", PollInterval: 5 * time.Millisecond, DeliverableSettle: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	token, sess, err := srv.Open(Scope{RoomID: "room-1", Owner: "jack", Core: "acme", Provider: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	return srv, token, sess
}

type rpcReply struct {
	status int
	header http.Header
	body   []byte
	Result map[string]any `json:"result"`
	Error  *rpcError      `json:"error"`
}

// post sends one JSON-RPC message with optional extra headers.
func post(t *testing.T, srv *Server, token string, msg map[string]any, headers map[string]string) rpcReply {
	t.Helper()
	buf, _ := json.Marshal(msg)
	req, _ := http.NewRequest(http.MethodPost, srv.URL(), bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	out := rpcReply{status: resp.StatusCode, header: resp.Header, body: body}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(body, &out)
	}
	return out
}

func rpc(id int, method string, params map[string]any) map[string]any {
	m := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		m["params"] = params
	}
	return m
}

func modernMeta(version string) map[string]any {
	return map[string]any{
		"io.modelcontextprotocol/protocolVersion":    version,
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "test", "version": "1"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
}

func toolText(t *testing.T, r rpcReply) (string, bool) {
	t.Helper()
	if r.Result == nil {
		t.Fatalf("no result: %d %s", r.status, r.body)
	}
	content, _ := r.Result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %v", r.Result)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	isErr, _ := r.Result["isError"].(bool)
	return text, isErr
}

// The legacy (initialize) era: handshake, notification, listing and a tool
// call; then the transport's refusals.
func TestLegacyProtocol(t *testing.T) {
	gw := &fakeGateway{handle: func(method, path string, _ []byte) (int, any) {
		if method == http.MethodGet && path == "/v1/rooms/room-1/agents" {
			return 200, roster
		}
		return 404, map[string]any{"error": "no route"}
	}}
	srv, token, _ := startServer(t, gw)

	init := post(t, srv, token, rpc(1, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"}}), nil)
	if init.status != 200 || init.Result["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize = %d %s", init.status, init.body)
	}
	if info, _ := init.Result["serverInfo"].(map[string]any); info["name"] != ServerName {
		t.Fatalf("serverInfo = %v", init.Result["serverInfo"])
	}
	if caps, _ := init.Result["capabilities"].(map[string]any); caps["tools"] == nil {
		t.Fatalf("capabilities = %v; want tools", init.Result["capabilities"])
	}
	if r := post(t, srv, token, rpc(2, "initialize", map[string]any{"protocolVersion": "1999-01-01"}), nil); r.Result["protocolVersion"] != LatestLegacyVersion {
		t.Fatalf("unknown version negotiates %v; want %s", r.Result["protocolVersion"], LatestLegacyVersion)
	}
	if r := post(t, srv, token, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}, nil); r.status != http.StatusAccepted {
		t.Fatalf("notification = %d; want 202", r.status)
	}

	list := post(t, srv, token, rpc(3, "tools/list", nil), map[string]string{"MCP-Protocol-Version": "2025-06-18"})
	tools, _ := list.Result["tools"].([]any)
	var names []string
	for _, tl := range tools {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "list_room_agents,ask_agent,room_status,read_room,save_component,get_component,list_components,publish_component" {
		t.Fatalf("tools = %v", names)
	}

	call := post(t, srv, token, rpc(4, "tools/call", map[string]any{"name": "list_room_agents", "arguments": map[string]any{}}), nil)
	text, isErr := toolText(t, call)
	if isErr || !strings.Contains(text, `"handle":"bob/claude"`) || !strings.Contains(text, `"you":true`) {
		t.Fatalf("list_room_agents = %s (error %v)", text, isErr)
	}
	if sc, _ := call.Result["structuredContent"].(map[string]any); sc["agents"] == nil {
		t.Fatalf("structuredContent = %v", call.Result["structuredContent"])
	}

	if r := post(t, srv, token, rpc(5, "tools/call", map[string]any{"name": "drop_tables"}), nil); r.Error == nil || r.Error.Code != codeInvalidParams {
		t.Fatalf("unknown tool = %s; want -32602", r.body)
	}
	if r := post(t, srv, token, rpc(6, "resources/list", nil), nil); r.Error == nil || r.Error.Code != codeMethodNotFound {
		t.Fatalf("unknown method = %s; want -32601", r.body)
	}
	if r := post(t, srv, token, rpc(7, "tools/list", nil), map[string]string{"MCP-Protocol-Version": "1999-01-01"}); r.status != 400 || r.Error == nil || r.Error.Code != codeUnsupportedVersion {
		t.Fatalf("bad version header = %d %s", r.status, r.body)
	}
}

// The modern (per-request _meta) era: discovery, the header rules, and a
// 404 for an unknown method.
func TestModernProtocol(t *testing.T) {
	gw := &fakeGateway{handle: func(string, string, []byte) (int, any) { return 200, roster }}
	srv, token, _ := startServer(t, gw)
	hdr := func(method, name string) map[string]string {
		h := map[string]string{"MCP-Protocol-Version": ModernVersion, "Mcp-Method": method}
		if name != "" {
			h["Mcp-Name"] = name
		}
		return h
	}
	d := post(t, srv, token, rpc(1, "server/discover", map[string]any{"_meta": modernMeta(ModernVersion)}), hdr("server/discover", ""))
	if d.status != 200 || d.Result["resultType"] != "complete" {
		t.Fatalf("discover = %d %s", d.status, d.body)
	}
	if vs, _ := d.Result["supportedVersions"].([]any); len(vs) == 0 || vs[0] != ModernVersion {
		t.Fatalf("supportedVersions = %v", d.Result["supportedVersions"])
	}
	call := post(t, srv, token, rpc(2, "tools/call", map[string]any{"name": "list_room_agents", "arguments": map[string]any{}, "_meta": modernMeta(ModernVersion)}), hdr("tools/call", "list_room_agents"))
	if text, isErr := toolText(t, call); isErr || call.Result["resultType"] != "complete" || !strings.Contains(text, "bob/claude") {
		t.Fatalf("modern call = %s", call.body)
	}
	// Base64-sentinel Mcp-Name decodes before the comparison.
	if r := post(t, srv, token, rpc(3, "tools/call", map[string]any{"name": "list_room_agents", "arguments": map[string]any{}, "_meta": modernMeta(ModernVersion)}), hdr("tools/call", "=?base64?bGlzdF9yb29tX2FnZW50cw==?=")); r.status != 200 {
		t.Fatalf("encoded Mcp-Name = %d %s", r.status, r.body)
	}
	cases := []struct {
		name    string
		headers map[string]string
		params  map[string]any
		method  string
		status  int
		code    int
	}{
		{"missing Mcp-Method", map[string]string{"MCP-Protocol-Version": ModernVersion}, map[string]any{"_meta": modernMeta(ModernVersion)}, "tools/list", 400, codeHeaderMismatch},
		{"version header mismatch", map[string]string{"MCP-Protocol-Version": "2025-11-25", "Mcp-Method": "tools/list"}, map[string]any{"_meta": modernMeta(ModernVersion)}, "tools/list", 400, codeHeaderMismatch},
		{"Mcp-Name mismatch", hdr("tools/call", "read_room"), map[string]any{"name": "list_room_agents", "_meta": modernMeta(ModernVersion)}, "tools/call", 400, codeHeaderMismatch},
		{"unsupported version", map[string]string{"MCP-Protocol-Version": "2099-01-01", "Mcp-Method": "tools/list"}, map[string]any{"_meta": modernMeta("2099-01-01")}, "tools/list", 400, codeUnsupportedVersion},
		{"unknown method", hdr("prompts/list", ""), map[string]any{"_meta": modernMeta(ModernVersion)}, "prompts/list", 404, codeMethodNotFound},
	}
	for _, tc := range cases {
		r := post(t, srv, token, rpc(9, tc.method, tc.params), tc.headers)
		if r.status != tc.status || r.Error == nil || r.Error.Code != tc.code {
			t.Errorf("%s = %d %s; want %d / %d", tc.name, r.status, r.body, tc.status, tc.code)
		}
	}
}

// Only the session's own bearer works; a revoked one, a foreign origin and
// the GET stream are refused.
func TestTransportRefusalsAndRevocation(t *testing.T) {
	gw := &fakeGateway{handle: func(string, string, []byte) (int, any) { return 200, roster }}
	srv, token, sess := startServer(t, gw)
	list := rpc(1, "tools/list", nil)
	if r := post(t, srv, "", list, nil); r.status != http.StatusUnauthorized {
		t.Fatalf("no bearer = %d", r.status)
	}
	if r := post(t, srv, token+"x", list, nil); r.status != http.StatusUnauthorized {
		t.Fatalf("wrong bearer = %d", r.status)
	}
	if r := post(t, srv, token, list, map[string]string{"Origin": "https://evil.example.com"}); r.status != http.StatusForbidden {
		t.Fatalf("foreign origin = %d", r.status)
	}
	if r := post(t, srv, token, list, map[string]string{"Origin": "http://localhost:5173"}); r.status != 200 {
		t.Fatalf("loopback origin = %d", r.status)
	}
	resp, err := http.Get(srv.URL())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d; want 405", resp.StatusCode)
	}
	// A second session's bearer is independent.
	other, _, err := srv.Open(Scope{RoomID: "room-1", Owner: "jack", Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	srv.Revoke(sess)
	if r := post(t, srv, token, list, nil); r.status != http.StatusUnauthorized {
		t.Fatalf("revoked bearer = %d; want 401", r.status)
	}
	if r := post(t, srv, other, list, nil); r.status != 200 {
		t.Fatalf("other session after a revoke = %d", r.status)
	}
	_ = srv.Close()
	if _, err := http.Post(srv.URL(), "application/json", strings.NewReader("{}")); err == nil {
		t.Fatal("a closed server still answers")
	}
}

// askFixture is a gateway for one ask_agent flow: the roster, an accepted
// dispatch, a job that runs and then replies, the reply in history and
// one deliverable published for the request.
type askFixture struct {
	gw      *fakeGateway
	mu      sync.Mutex
	polls   int
	replied bool
}

const (
	dispatchSeq   = int64(41)
	dispatchJobID = "job_child"
)

func newAskFixture(settleAfter int) *askFixture {
	f := &askFixture{}
	f.gw = &fakeGateway{handle: func(method, path string, body []byte) (int, any) {
		switch {
		case method == http.MethodGet && path == "/v1/rooms/room-1/agents":
			return 200, roster
		case method == http.MethodPost && path == "/v1/rooms/room-1/dispatch":
			return 200, wire.DispatchReply{Sequence: dispatchSeq, JobID: dispatchJobID, Depth: 1}
		case method == http.MethodGet && path == "/v1/rooms/room-1/jobs/"+dispatchJobID:
			f.mu.Lock()
			f.polls++
			done := f.polls > settleAfter
			f.mu.Unlock()
			run := wire.AgentRun{JobID: dispatchJobID, RoomID: "room-1", ChannelID: "ch_general", Requester: "maya", Status: "running",
				Request: wire.AgentRunRequest{MessageSeq: dispatchSeq}, Agent: wire.AgentRunAgent{Owner: "bob", Core: "acme", Provider: "claude"}}
			if done {
				run.Status = "replied"
				run.Reply = &wire.AgentRunReply{MessageID: "m1", RoomSeq: 43, Text: "preview", Count: 2}
				run.Phases.FinishedAtMs = 5000
				run.Diagnostics = map[string]any{"sessionId": "sess-bob"}
			}
			return 200, run
		case method == http.MethodGet && strings.HasPrefix(path, "/v1/rooms/room-1/history"):
			batch := func(seq int64, session, owner, text string, at int64) map[string]any {
				return map[string]any{"sequence": seq, "kind": "agent_messages", "atUnixMs": at, "payload": map[string]any{
					"agentSessionId": session,
					"messages":       []any{map[string]any{"payload": map[string]any{"role": "assistant", "text": text, "ownerUserId": owner, "provider": "claude"}}},
				}}
			}
			return 200, map[string]any{"messages": []any{
				map[string]any{"sequence": 40, "kind": "chat_message", "atUnixMs": 1000, "payload": map[string]any{"userId": "maya", "text": "earlier"}},
				map[string]any{"sequence": dispatchSeq, "kind": "chat_message", "atUnixMs": 1100, "payload": map[string]any{"userId": "maya", "text": "review it", "recipient": map[string]any{"ownerId": "bob", "provider": "claude"}, "dispatchedBy": map[string]any{"jobId": "job_parent", "depth": 1, "via": "jack/codex"}}},
				batch(42, "sess-other", "bob", "unrelated reply of another session", 1200),
				batch(43, "sess-bob", "bob", "Found two issues.", 1300),
				batch(44, "sess-bob", "bob", "Both fixed in parser.go.", 1400),
				batch(45, "sess-bob", "bob", "a later turn", 9000),
			}}
		case method == http.MethodGet && strings.HasPrefix(path, "/v1/rooms/room-1/artifacts?"):
			return 200, map[string]any{"artifacts": []any{map[string]any{"artifactId": "art_1", "kind": "code", "title": "parser", "fileName": "parser.go", "currentVersion": 2}}}
		}
		return 404, map[string]any{"error": "no route " + path}
	}}
	return f
}

// ask_agent end to end with a fake gateway: the dispatch names the bound
// parent job and the resolved agent, the wait follows the job until it
// replies, and the answer carries the reply text (only this run's session,
// up to its finish) and the deliverables published for the request. A
// streamed request gets progress notifications before the result.
func TestAskAgentFlow(t *testing.T) {
	f := newAskFixture(2)
	srv, token, sess := startServer(t, f.gw)
	call := rpc(1, "tools/call", map[string]any{"name": "ask_agent", "arguments": map[string]any{"handle": "bob/claude", "task": "review the parser change"}})

	// Unbound session: nothing to dispatch on behalf of.
	text, isErr := toolText(t, post(t, srv, token, call, nil))
	if !isErr || !strings.Contains(text, "only while you are answering") {
		t.Fatalf("unbound ask_agent = %s", text)
	}
	sess.Bind(Job{JobID: "job_parent", RequesterUserID: "maya", ChannelID: "ch_general", MessageSeq: 30})

	params := call["params"].(map[string]any)
	params["_meta"] = map[string]any{"progressToken": "p1"}
	frames := postStream(t, srv, token, call)
	if len(frames) < 2 {
		t.Fatalf("stream frames = %v; want progress then the result", frames)
	}
	for _, fr := range frames[:len(frames)-1] {
		if fr["method"] != "notifications/progress" {
			t.Fatalf("frame %v; want a progress notification", fr)
		}
	}
	final := frames[len(frames)-1]
	res, _ := final["result"].(map[string]any)
	sc, _ := res["structuredContent"].(map[string]any)
	if sc["status"] != "replied" || sc["handle"] != dispatchJobID || sc["agent"] != "bob/claude" {
		t.Fatalf("outcome = %v", sc)
	}
	if sc["reply"] != "Found two issues.\n\nBoth fixed in parser.go." {
		t.Fatalf("reply = %q", sc["reply"])
	}
	dl, _ := sc["deliverables"].([]any)
	if len(dl) != 1 {
		t.Fatalf("deliverables = %v", sc["deliverables"])
	}
	if d := dl[0].(map[string]any); d["name"] != "parser.go" || d["kind"] != "code" || d["link"] != "https://bnbloc.example.com/v1/rooms/room-1/artifacts/art_1/versions/2/content" {
		t.Fatalf("deliverable = %v", d)
	}

	dispatches := f.gw.callsTo(http.MethodPost, "/v1/rooms/room-1/dispatch")
	if len(dispatches) != 1 {
		t.Fatalf("dispatches = %d", len(dispatches))
	}
	var sent wire.DispatchRequest
	_ = json.Unmarshal(dispatches[0].Body, &sent)
	if sent.ParentJobID != "job_parent" || sent.Recipient != (wire.DispatchRecipient{OwnerID: "bob", Core: "acme", Provider: "claude"}) || sent.Text != "review the parser change" || sent.ClientMsgID == "" {
		t.Fatalf("dispatch body = %+v", sent)
	}
	if arts := f.gw.callsTo(http.MethodGet, "/v1/rooms/room-1/artifacts"); len(arts) != 1 || !strings.Contains(arts[0].Path, "sourceSeq=41") {
		t.Fatalf("artifact lookups = %+v; want one by sourceSeq=41", arts)
	}

	// room_status knows the task by its handle.
	st := post(t, srv, token, rpc(2, "tools/call", map[string]any{"name": "room_status", "arguments": map[string]any{"handle": dispatchJobID}}), nil)
	if text, isErr := toolText(t, st); isErr || !strings.Contains(text, `"status":"replied"`) {
		t.Fatalf("room_status = %s", text)
	}
	all := post(t, srv, token, rpc(3, "tools/call", map[string]any{"name": "room_status", "arguments": map[string]any{}}), nil)
	if text, _ := toolText(t, all); !strings.Contains(text, dispatchJobID) {
		t.Fatalf("room_status (all) = %s", text)
	}
	if r := post(t, srv, token, rpc(4, "tools/call", map[string]any{"name": "room_status", "arguments": map[string]any{"handle": "job_someone_elses"}}), nil); r.Result["isError"] != true {
		t.Fatalf("room_status of a foreign job = %s; want a tool error", r.body)
	}
}

// A wait that ends before the job settles returns the handle; the agent
// polls room_status with it.
func TestAskAgentTimeoutReturnsHandle(t *testing.T) {
	f := newAskFixture(1 << 30)
	srv, token, sess := startServer(t, f.gw)
	sess.Bind(Job{JobID: "job_parent", ChannelID: "ch_general"})
	r := post(t, srv, token, rpc(1, "tools/call", map[string]any{"name": "ask_agent", "arguments": map[string]any{"handle": "bob/claude", "task": "x", "wait_seconds": 0}}), nil)
	text, isErr := toolText(t, r)
	if isErr || !strings.Contains(text, `"handle":"job_child"`) || !strings.Contains(text, `"settled":false`) {
		t.Fatalf("timed-out ask_agent = %s", text)
	}
}

// Refusals reach the agent as tool errors with the server's reason, and a
// handle that is not in the room (or is the agent itself) never dispatches.
func TestAskAgentRefusals(t *testing.T) {
	gw := &fakeGateway{handle: func(method, path string, _ []byte) (int, any) {
		if path == "/v1/rooms/room-1/agents" {
			return 200, roster
		}
		if path == "/v1/rooms/room-1/dispatch" {
			return 409, map[string]string{"code": wire.DispatchFanOutExceeded, "error": "this job already dispatched 8 tasks (the limit)"}
		}
		return 404, nil
	}}
	srv, token, sess := startServer(t, gw)
	sess.Bind(Job{JobID: "job_parent", ChannelID: "ch_general"})
	for _, tc := range []struct{ handle, want string }{
		{"bob/claude", "refused (fan_out_exceeded)"},
		{"zoe/codex", `no agent "zoe/codex"`},
		{"jack/codex", "is you"},
	} {
		r := post(t, srv, token, rpc(1, "tools/call", map[string]any{"name": "ask_agent", "arguments": map[string]any{"handle": tc.handle, "task": "x"}}), nil)
		if text, isErr := toolText(t, r); !isErr || !strings.Contains(text, tc.want) {
			t.Errorf("ask_agent(%s) = %s; want an error containing %q", tc.handle, text, tc.want)
		}
	}
	if n := len(gw.callsTo(http.MethodPost, "/v1/rooms/room-1/dispatch")); n != 1 {
		t.Fatalf("dispatch calls = %d; want only the resolvable, non-self one", n)
	}
}

// read_room reads the bound job's channel and renders people, agents and
// dispatch attribution.
func TestReadRoom(t *testing.T) {
	f := newAskFixture(0)
	srv, token, sess := startServer(t, f.gw)
	sess.Bind(Job{JobID: "job_parent", ChannelID: "ch_general"})
	r := post(t, srv, token, rpc(1, "tools/call", map[string]any{"name": "read_room", "arguments": map[string]any{"since": 40}}), nil)
	text, isErr := toolText(t, r)
	if isErr || strings.Contains(text, "earlier") || !strings.Contains(text, `"via":"jack/codex"`) || !strings.Contains(text, `"to":"bob/claude"`) || !strings.Contains(text, `"author":"bob/claude"`) {
		t.Fatalf("read_room = %s", text)
	}
	hist := f.gw.callsTo(http.MethodGet, "/v1/rooms/room-1/history")
	if len(hist) == 0 || !strings.Contains(hist[0].Path, "channelId=ch_general") {
		t.Fatalf("history calls = %+v; want the job's channel", hist)
	}
}

// postStream sends a request that asks for progress and returns every SSE
// frame's JSON message.
func postStream(t *testing.T, srv *Server, token string, msg map[string]any) []map[string]any {
	t.Helper()
	buf, _ := json.Marshal(msg)
	req, _ := http.NewRequest(http.MethodPost, srv.URL(), bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("not a stream: %s %s", resp.Header.Get("Content-Type"), body)
	}
	var out []map[string]any
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			m := map[string]any{}
			if err := json.Unmarshal([]byte(data), &m); err != nil {
				t.Fatalf("frame %q: %v", data, err)
			}
			out = append(out, m)
		}
	}
	return out
}

func TestExpertRef(t *testing.T) {
	if expertRef("") != nil {
		t.Fatal("no expert")
	}
	if got := expertRef("cc_rev@1.2.0"); got == nil || *got != (wire.ChatExpert{ID: "cc_rev", Version: "1.2.0"}) {
		t.Fatalf("expertRef = %+v", got)
	}
	if got := expertRef("cc_rev"); got == nil || *got != (wire.ChatExpert{ID: "cc_rev"}) {
		t.Fatalf("expertRef = %+v", got)
	}
}
