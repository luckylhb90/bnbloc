package roomtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/luckylhb90/bnbloc/services/bridge/hosttool"
)

// fakeHostTool answers like a stdio MCP server behind the host.
type fakeHostTool struct {
	calls    atomic.Int32
	closed   atomic.Bool
	lastReq  atomic.Value // the last forwarded method
	failWith error
	progress []string
}

func (f *fakeHostTool) Info(context.Context) (json.RawMessage, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	return json.RawMessage(`{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"mailer","version":"3"}}`), nil
}

func (f *fakeHostTool) Call(_ context.Context, method string, params json.RawMessage, onProgress func(json.RawMessage)) (json.RawMessage, *hosttool.RPCError, error) {
	f.calls.Add(1)
	f.lastReq.Store(method)
	if f.failWith != nil {
		return nil, nil, f.failWith
	}
	for _, p := range f.progress {
		if onProgress != nil {
			onProgress(json.RawMessage(p))
		}
	}
	if strings.Contains(string(params), `"refuse"`) {
		return nil, &hosttool.RPCError{Code: -32001, Message: "refused by the tool"}, nil
	}
	return json.RawMessage(`{"content":[{"type":"text","text":"sent"}]}`), nil, nil
}

func (f *fakeHostTool) Close() { f.closed.Store(true) }

func toolPost(t *testing.T, srv *Server, token, name string, body string, header map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.HostToolURL(name), bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func hostServer(t *testing.T) (*Server, string, *Session, *fakeHostTool) {
	t.Helper()
	srv, token, sess := startServer(t, &fakeGateway{handle: func(string, string, []byte) (int, any) { return 200, map[string]any{} }})
	tool := &fakeHostTool{}
	sess.AttachHostTool("mailer", tool)
	return srv, token, sess, tool
}

const (
	initReq  = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`
	listReq  = `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	callReq  = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"send","arguments":{}}}`
	refusing = `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"refuse"}}`
)

func TestHostToolNeedsTheSessionsBearerAndItsOwnTool(t *testing.T) {
	srv, token, sess, tool := hostServer(t)
	if code, _ := toolPost(t, srv, "", "mailer", initReq, nil); code != http.StatusUnauthorized {
		t.Fatalf("no bearer = %d", code)
	}
	if code, _ := toolPost(t, srv, "not-the-token-not-the-token-not-the-token", "mailer", initReq, nil); code != http.StatusUnauthorized {
		t.Fatalf("another bearer = %d", code)
	}
	if code, _ := toolPost(t, srv, token, "other", initReq, nil); code != http.StatusNotFound {
		t.Fatalf("a tool this session does not hold = %d; want 404", code)
	}
	if code, _ := toolPost(t, srv, token, "mailer/extra", initReq, nil); code != http.StatusNotFound {
		t.Fatalf("a nested path = %d; want 404", code)
	}
	// A second session has its own bearer and none of this session's tools.
	other, otherSess := srv, (*Session)(nil)
	tok2, otherSess, err := other.Open(Scope{RoomID: "room-2"})
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := toolPost(t, srv, tok2, "mailer", initReq, nil); code != http.StatusNotFound {
		t.Fatalf("another session's bearer reached this session's tool: %d", code)
	}
	_ = otherSess
	if tool.calls.Load() != 0 {
		t.Fatal("a refused request reached the tool")
	}
	// A GET is no MCP stream here.
	req, _ := http.NewRequest(http.MethodGet, srv.HostToolURL("mailer"), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d", resp.StatusCode)
	}
	_ = sess
}

func TestHostToolHandshakeAnswersFromTheToolsOwnInfo(t *testing.T) {
	srv, token, _, tool := hostServer(t)
	code, body := toolPost(t, srv, token, "mailer", initReq, nil)
	if code != 200 {
		t.Fatalf("initialize = %d %s", code, body)
	}
	var out struct {
		Result struct {
			ProtocolVersion string         `json:"protocolVersion"`
			Capabilities    map[string]any `json:"capabilities"`
			ServerInfo      map[string]any `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || out.Result.ServerInfo["name"] != "mailer" || out.Result.Capabilities["tools"] == nil || out.Result.ProtocolVersion != "2025-06-18" {
		t.Fatalf("initialize = %s (%v)", body, err)
	}
	// A version the server does not know is answered with the latest it speaks.
	_, body = toolPost(t, srv, token, "mailer", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`, nil)
	if !strings.Contains(body, LatestLegacyVersion) {
		t.Fatalf("initialize with an unknown version = %s", body)
	}
	// ping and a notification never reach the tool.
	if code, _ := toolPost(t, srv, token, "mailer", `{"jsonrpc":"2.0","id":9,"method":"ping"}`, nil); code != 200 {
		t.Fatalf("ping = %d", code)
	}
	if code, _ := toolPost(t, srv, token, "mailer", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil); code != http.StatusAccepted {
		t.Fatalf("notification = %d", code)
	}
	if tool.calls.Load() != 0 {
		t.Fatalf("the handshake was forwarded: %d calls", tool.calls.Load())
	}
}

// The tool is listed between turns (a client opens before it works) but a
// call that acts needs the turn the session is driving.
func TestHostToolActsOnlyInATurn(t *testing.T) {
	srv, token, sess, tool := hostServer(t)
	if code, body := toolPost(t, srv, token, "mailer", listReq, nil); code != 200 || !strings.Contains(body, `"result"`) {
		t.Fatalf("list between turns = %d %s", code, body)
	}
	if code, body := toolPost(t, srv, token, "mailer", callReq, nil); code != http.StatusConflict || !strings.Contains(body, "no turn is running") {
		t.Fatalf("call between turns = %d %s; want 409", code, body)
	}
	if tool.calls.Load() != 1 { // only the list reached the tool
		t.Fatalf("calls = %d", tool.calls.Load())
	}
	sess.Bind(Job{JobID: "job-1", RequesterUserID: "maya"})
	code, body := toolPost(t, srv, token, "mailer", callReq, nil)
	if code != 200 || !strings.Contains(body, `"sent"`) || !strings.Contains(body, `"id":3`) {
		t.Fatalf("call in a turn = %d %s", code, body)
	}
	if m, _ := tool.lastReq.Load().(string); m != "tools/call" {
		t.Fatalf("forwarded %q", m)
	}
}

func TestHostToolErrorsAreTheToolsOrExplainTheToolIsGone(t *testing.T) {
	srv, token, sess, tool := hostServer(t)
	sess.Bind(Job{JobID: "job-1"})
	code, body := toolPost(t, srv, token, "mailer", refusing, nil)
	if code != 200 || !strings.Contains(body, `"error"`) || !strings.Contains(body, "-32001") || !strings.Contains(body, "refused by the tool") {
		t.Fatalf("the tool's own error = %d %s", code, body)
	}
	tool.failWith = errors.New("the mailer tool stopped: exit status 3")
	code, body = toolPost(t, srv, token, "mailer", callReq, nil)
	if code != 200 || !strings.Contains(body, "the mailer tool stopped") || !strings.Contains(body, "-32603") {
		t.Fatalf("a dead tool = %d %s", code, body)
	}
}

func TestHostToolProgressStreamsToTheRequestThatAsked(t *testing.T) {
	srv, token, sess, tool := hostServer(t)
	sess.Bind(Job{JobID: "job-1"})
	tool.progress = []string{`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"p1","progress":1}}`}
	req := `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"send","_meta":{"progressToken":"p1"}}}`
	code, body := toolPost(t, srv, token, "mailer", req, map[string]string{"Accept": "application/json, text/event-stream"})
	if code != 200 || strings.Count(body, "event: message") != 2 || !strings.Contains(body, "notifications/progress") || !strings.Contains(body, `"id":5`) {
		t.Fatalf("stream = %d %q", code, body)
	}
	// Without Accept for a stream, the progress is not sent and the answer is plain JSON.
	code, body = toolPost(t, srv, token, "mailer", req, nil)
	if code != 200 || strings.Contains(body, "event:") || !strings.Contains(body, `"result"`) {
		t.Fatalf("plain = %d %q", code, body)
	}
}

func TestHostToolModernRequestsGetModernFields(t *testing.T) {
	srv, token, _, _ := hostServer(t)
	hdr := map[string]string{"MCP-Protocol-Version": ModernVersion, "Mcp-Method": "tools/list"}
	req := `{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"` + ModernVersion + `"}}}`
	code, body := toolPost(t, srv, token, "mailer", req, hdr)
	if code != 200 || !strings.Contains(body, `"resultType":"complete"`) || !strings.Contains(body, `"cacheScope":"private"`) {
		t.Fatalf("modern list = %d %s", code, body)
	}
	// Header and body must agree, as on the room tools endpoint.
	hdr["Mcp-Method"] = "tools/call"
	if code, _ := toolPost(t, srv, token, "mailer", req, hdr); code != http.StatusBadRequest {
		t.Fatalf("mismatched headers = %d", code)
	}
}

func TestRevokingTheSessionStopsItsHostTools(t *testing.T) {
	srv, token, sess, tool := hostServer(t)
	srv.Revoke(sess)
	if !tool.closed.Load() {
		t.Fatal("a revoked session left its host tool running")
	}
	if code, _ := toolPost(t, srv, token, "mailer", initReq, nil); code != http.StatusUnauthorized {
		t.Fatalf("revoked bearer = %d", code)
	}
	// A tool attached to a session that is already gone is stopped at once.
	late := &fakeHostTool{}
	sess.AttachHostTool("late", late)
	if !late.closed.Load() {
		t.Fatal("a tool attached after revoke was left running")
	}
}

func TestReplacingAHostToolStopsTheOldOne(t *testing.T) {
	_, _, sess, tool := hostServer(t)
	sess.AttachHostTool("mailer", &fakeHostTool{})
	if !tool.closed.Load() {
		t.Fatal("the replaced tool was left running")
	}
}

func TestHostToolRefusesAForeignOrigin(t *testing.T) {
	srv, token, _, _ := hostServer(t)
	code, _ := toolPost(t, srv, token, "mailer", initReq, map[string]string{"Origin": "https://evil.example"})
	if code != http.StatusForbidden {
		t.Fatalf("a web page's origin = %d", code)
	}
}

// A turn's end unbinds the session: a process of the agent that outlives the turn
// holds the bearer and still cannot act (a host tool has no downstream check that
// the job is running, as the gateway is for the room relay).
func TestAHostToolIsRefusedOnceTheTurnHasEnded(t *testing.T) {
	srv, token, sess, _ := hostServer(t)
	sess.Bind(Job{JobID: "job-1", RequesterUserID: "maya"})
	if code, _ := toolPost(t, srv, token, "mailer", callReq, nil); code != 200 {
		t.Fatalf("in the turn = %d", code)
	}
	sess.Unbind("job-0") // another job's end: nothing
	if code, _ := toolPost(t, srv, token, "mailer", callReq, nil); code != 200 {
		t.Fatalf("an unrelated end unbound the turn: %d", code)
	}
	sess.Unbind("job-1")
	if code, body := toolPost(t, srv, token, "mailer", callReq, nil); code != http.StatusConflict || !strings.Contains(body, "no turn is running") {
		t.Fatalf("after the turn = %d %s; want 409", code, body)
	}
	if code, body := toolPost(t, srv, token, "mailer", listReq, nil); code != 200 {
		t.Fatalf("a list is a handshake and needs no turn: %d %s", code, body)
	}
	// The room relay answers the same: no turn, no facts.
	req, _ := http.NewRequest(http.MethodGet, srv.RelayURL()+"/session", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("relay session after the turn = %d", resp.StatusCode)
	}
	// A view that outlives its turn may still name the job it belongs to.
	if !sess.drove("job-1") {
		t.Fatal("the ended turn's job is no longer one the session drove")
	}
	sess.Bind(Job{JobID: "job-2", RequesterUserID: "maya"})
	if code, _ := toolPost(t, srv, token, "mailer", callReq, nil); code != 200 {
		t.Fatalf("the next turn = %d", code)
	}
}

// guestServer is a server whose proxy port is up, as the sandbox opens it to a guest: the
// guest names it by an alias, not by the loopback address the server itself listens on.
func guestServer(t *testing.T) (*Server, string, *Session, *fakeHostTool) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	_, port, _ := net.SplitHostPort(addr)
	srv, err := Start(Config{Gateway: &fakeGateway{handle: func(string, string, []byte) (int, any) { return 200, map[string]any{} }},
		ProxyAddr: addr, GuestHost: "host.vm.internal:" + port})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	token, sess, err := srv.Open(Scope{RoomID: "room-1", Owner: "jack", Core: "acme", Provider: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	tool := &fakeHostTool{}
	sess.AttachHostTool("mailer", tool)
	return srv, token, sess, tool
}

// guestPost sends what an agent in the guest sends: to the proxy port, under the name the guest
// has for the host (the Host header), with the session's bearer.
func guestPost(t *testing.T, srv *Server, token, path, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "http://"+srv.proxyLn.Addr().String()+path, strings.NewReader(body))
	req.Host = srv.cfg.GuestHost
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// An agent that runs in the sandbox reaches its room tools and its host-placed tools on the one
// host port the sandbox opens to it, by the name the guest has for the host. Without this such an
// agent was handed 127.0.0.1:<port>, which is its own loopback: dsh stops its whole session on an
// MCP server it cannot connect to ("mcp-client(media-tools): initial connection or tool
// synchronization failed"), so the agent could not start at all.
func TestAGuestReachesItsToolsOnTheProxyPort(t *testing.T) {
	srv, token, sess, tool := guestServer(t)
	if got := srv.GuestURL(ToolsPath + "/mailer"); got != "http://"+srv.cfg.GuestHost+ToolsPath+"/mailer" {
		t.Fatalf("GuestURL = %q", got)
	}
	sess.Bind(Job{JobID: "job-1", RequesterUserID: "jack", ChannelID: "ch"})
	if code, body := guestPost(t, srv, token, ToolsPath+"/mailer", initReq); code != 200 || !strings.Contains(body, `"mailer"`) {
		t.Fatalf("a guest's initialize of its host tool = %d %s", code, body)
	}
	if code, body := guestPost(t, srv, token, ToolsPath+"/mailer", callReq); code != 200 || !strings.Contains(body, "sent") || tool.calls.Load() == 0 {
		t.Fatalf("a guest's call of its host tool = %d %s", code, body)
	}
	if code, body := guestPost(t, srv, token, Endpoint, initReq); code != 200 || !strings.Contains(body, `"result"`) {
		t.Fatalf("a guest's initialize of the room tools = %d %s", code, body)
	}
	// The bearer is still what decides.
	if code, _ := guestPost(t, srv, "", ToolsPath+"/mailer", initReq); code != http.StatusUnauthorized {
		t.Errorf("no bearer = %d, want 401", code)
	}
	if code, _ := guestPost(t, srv, "not-a-session", Endpoint, initReq); code != http.StatusUnauthorized {
		t.Errorf("a wrong bearer = %d, want 401", code)
	}
}

// The guest's port serves the tools and the proxy, nothing else: not the room relay (it carries
// the room's credentials to the tools the bridge runs), and not under any other name for the host.
func TestTheGuestsPortDoesNotServeTheRelayOrAForeignHost(t *testing.T) {
	srv, token, _, _ := guestServer(t)
	if code, _ := guestPost(t, srv, token, RelayPath+"/v1/anything", `{}`); code != http.StatusNotFound {
		t.Errorf("the room relay on the guest's port = %d, want 404", code)
	}
	req, _ := http.NewRequest(http.MethodPost, "http://"+srv.proxyLn.Addr().String()+Endpoint, strings.NewReader(initReq))
	req.Host = "evil.example.com"
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a request addressed to another host = %d, want 403 (DNS rebinding)", resp.StatusCode)
	}
	// And it says nothing about the guest when the port is not up.
	plain, _, _ := startServer(t, &fakeGateway{handle: func(string, string, []byte) (int, any) { return 200, map[string]any{} }})
	if got := plain.GuestURL(Endpoint); got != "" {
		t.Errorf("GuestURL without a proxy port = %q, want none", got)
	}
}

// An agent in the guest names the host the tools are on by its own alias; that name must not go
// through the egress proxy. An agent whose MCP client honours the proxy variables (qwen's) would
// otherwise tunnel to a name no resolver has ("host.microsandbox.internal does not resolve") and run
// without its tools, with nothing said.
func TestTheGuestsHostIsNotProxied(t *testing.T) {
	srv, _, _, _ := guestServer(t)
	env := srv.EgressEnv("token-token-token-token", srv.cfg.GuestHost)
	host, _, _ := net.SplitHostPort(srv.cfg.GuestHost)
	for _, k := range []string{"NO_PROXY", "no_proxy"} {
		if !strings.Contains(","+env[k]+",", ","+host+",") {
			t.Errorf("%s = %q, want the guest's name for the host (%s) in it", k, env[k], host)
		}
		if !strings.Contains(env[k], "127.0.0.1") {
			t.Errorf("%s = %q lost the loopback addresses", k, env[k])
		}
	}
	// On this computer the proxy is the server's own loopback address: nothing to add.
	if own := srv.EgressEnv("token-token-token-token", ""); strings.Count(own["NO_PROXY"], ",") != 2 {
		t.Errorf("NO_PROXY on this computer = %q, want only the loopback names", own["NO_PROXY"])
	}
}
