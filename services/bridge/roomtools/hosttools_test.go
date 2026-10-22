package roomtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
