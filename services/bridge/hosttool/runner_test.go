package hosttool

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// The tests start the test binary itself as the tool: with HOSTTOOL_FAKE set
// it is a small stdio MCP server.
func TestMain(m *testing.M) {
	if os.Getenv("HOSTTOOL_FAKE") != "" {
		fakeTool()
		return
	}
	os.Exit(m.Run())
}

func fakeTool() {
	in := bufio.NewReader(os.Stdin)
	var wmu sync.Mutex
	send := func(v any) {
		raw, _ := json.Marshal(v)
		wmu.Lock()
		defer wmu.Unlock()
		os.Stdout.Write(append(raw, '\n'))
	}
	release := map[string]chan struct{}{}
	var rmu sync.Mutex
	gate := func(name string) chan struct{} {
		rmu.Lock()
		defer rmu.Unlock()
		if release[name] == nil {
			release[name] = make(chan struct{})
		}
		return release[name]
	}
	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			return // stdin closed: the host stopped us
		}
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		if m.Method == "" { // a response to a request we made
			continue
		}
		go func() {
			switch m.Method {
			case "initialize":
				send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{
					"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}},
					"serverInfo": map[string]any{"name": "fake", "version": "1"}}})
			case "tools/call":
				var p struct {
					Name string `json:"name"`
					Args struct {
						Text string `json:"text"`
						Gate string `json:"gate"`
					} `json:"arguments"`
					Meta struct {
						Token json.RawMessage `json:"progressToken"`
					} `json:"_meta"`
				}
				_ = json.Unmarshal(m.Params, &p)
				switch p.Name {
				case "crash":
					os.Exit(3)
				case "wait": // answers only when "open" is called with the same gate
					<-gate(p.Args.Gate)
				case "open":
					close(gate(p.Args.Gate))
				case "progress":
					send(map[string]any{"jsonrpc": "2.0", "method": "notifications/progress", "params": map[string]any{"progressToken": p.Meta.Token, "progress": 1}})
				case "ask": // a request of the client
					send(map[string]any{"jsonrpc": "2.0", "id": "srv-1", "method": "sampling/createMessage", "params": map[string]any{}})
				case "env":
					send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"text": os.Getenv("HOSTTOOL_SECRET") + "|" + mustCwd()}})
					return
				case "fail":
					send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32001, "message": "refused"}})
					return
				}
				send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"text": p.Args.Text}})
			default:
				send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"method": m.Method}})
			}
		}()
	}
}

func mustCwd() string { d, _ := os.Getwd(); return d }

func fakeSpec(extraEnv ...string) Spec {
	return Spec{Name: "fake", Command: os.Args[0], Env: append(append(os.Environ(), "HOSTTOOL_FAKE=1"), extraEnv...)}
}

func callTool(r *Runner, ctx context.Context, name string, args map[string]any, onProgress func(json.RawMessage)) (map[string]any, *RPCError, error) {
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	if onProgress != nil {
		params, _ = json.Marshal(map[string]any{"name": name, "arguments": args, "_meta": map[string]any{"progressToken": "tok-1"}})
	}
	raw, rerr, err := r.Call(ctx, "tools/call", params, onProgress)
	if err != nil || rerr != nil {
		return nil, rerr, err
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out, nil, nil
}

func TestStartsOnFirstCallAndAnswers(t *testing.T) {
	r := New(fakeSpec())
	defer r.Close()
	if r.proc != nil {
		t.Fatal("started before the first call")
	}
	out, rerr, err := callTool(r, context.Background(), "echo", map[string]any{"text": "hi"}, nil)
	if err != nil || rerr != nil || out["text"] != "hi" {
		t.Fatalf("out=%v rerr=%v err=%v", out, rerr, err)
	}
	info, err := r.Info(context.Background())
	if err != nil || !strings.Contains(string(info), `"fake"`) {
		t.Fatalf("info=%s err=%v", info, err)
	}
}

// Two callers each get their own answer even when the tool answers the second
// first: ids towards the tool are the runner's, not the callers'.
func TestConcurrentCallsAreAnsweredOutOfOrder(t *testing.T) {
	r := New(fakeSpec())
	defer r.Close()
	ctx := context.Background()
	if _, _, err := callTool(r, ctx, "echo", map[string]any{"text": "warm"}, nil); err != nil {
		t.Fatal(err)
	}
	first := make(chan string, 1)
	go func() {
		out, _, err := callTool(r, ctx, "wait", map[string]any{"gate": "g", "text": "first"}, nil)
		if err != nil {
			first <- "error: " + err.Error()
			return
		}
		first <- out["text"].(string)
	}()
	time.Sleep(100 * time.Millisecond) // the first request is in flight
	out, _, err := callTool(r, ctx, "echo", map[string]any{"text": "second"}, nil)
	if err != nil || out["text"] != "second" {
		t.Fatalf("second = %v %v", out, err)
	}
	select {
	case got := <-first:
		t.Fatalf("the first answered before it was released: %s", got)
	default:
	}
	if _, _, err := callTool(r, ctx, "open", map[string]any{"gate": "g"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-first:
		if got != "first" {
			t.Fatalf("first = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first never answered")
	}
}

func TestProgressGoesToTheRequestThatAskedForIt(t *testing.T) {
	r := New(fakeSpec())
	defer r.Close()
	var got []string
	var mu sync.Mutex
	_, _, err := callTool(r, context.Background(), "progress", map[string]any{"text": "done"}, func(raw json.RawMessage) {
		mu.Lock()
		got = append(got, string(raw))
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || !strings.Contains(got[0], "notifications/progress") || !strings.Contains(got[0], "tok-1") {
		t.Fatalf("progress = %v", got)
	}
}

func TestAToolErrorIsTheToolsAnswer(t *testing.T) {
	r := New(fakeSpec())
	defer r.Close()
	_, rerr, err := callTool(r, context.Background(), "fail", nil, nil)
	if err != nil || rerr == nil || rerr.Code != -32001 || rerr.Message != "refused" {
		t.Fatalf("rerr=%v err=%v", rerr, err)
	}
}

// A request the tool makes of its client is refused, and the call goes on.
func TestARequestFromTheToolIsRefused(t *testing.T) {
	r := New(fakeSpec())
	defer r.Close()
	out, rerr, err := callTool(r, context.Background(), "ask", map[string]any{"text": "after"}, nil)
	if err != nil || rerr != nil || out["text"] != "after" {
		t.Fatalf("out=%v rerr=%v err=%v", out, rerr, err)
	}
}

func TestACrashFailsEveryCallInFlightAndTheNextOneRestarts(t *testing.T) {
	r := New(fakeSpec())
	defer r.Close()
	ctx := context.Background()
	if _, _, err := callTool(r, ctx, "echo", map[string]any{"text": "up"}, nil); err != nil {
		t.Fatal(err)
	}
	waiting := make(chan error, 1)
	go func() {
		_, _, err := callTool(r, ctx, "wait", map[string]any{"gate": "never"}, nil)
		waiting <- err
	}()
	time.Sleep(100 * time.Millisecond)
	_, _, err := callTool(r, ctx, "crash", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("the crashing call: %v", err)
	}
	select {
	case err := <-waiting:
		if err == nil || !strings.Contains(err.Error(), "stopped") {
			t.Fatalf("the call in flight: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a call in flight hung after the tool died")
	}
	out, rerr, err := callTool(r, ctx, "echo", map[string]any{"text": "again"}, nil)
	if err != nil || rerr != nil || out["text"] != "again" {
		t.Fatalf("after a restart: %v %v %v", out, rerr, err)
	}
}

func TestACallerThatGoesAwayEndsItsCallOnly(t *testing.T) {
	r := New(fakeSpec())
	defer r.Close()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, _, err := callTool(r, ctx, "wait", map[string]any{"gate": "x"}, nil)
		errc <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call did not end with its caller")
	}
	if out, _, err := callTool(r, context.Background(), "echo", map[string]any{"text": "still"}, nil); err != nil || out["text"] != "still" {
		t.Fatalf("the tool does not serve after a caller left: %v %v", out, err)
	}
}

func TestEnvironmentAndDirectoryAreTheSpecs(t *testing.T) {
	dir := t.TempDir()
	spec := fakeSpec("HOSTTOOL_SECRET=s3cret")
	spec.Dir = dir
	r := New(spec)
	defer r.Close()
	out, _, err := callTool(r, context.Background(), "env", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := out["text"].(string)
	secret, cwd, _ := strings.Cut(text, "|")
	if secret != "s3cret" || !strings.HasSuffix(cwd, strings.TrimPrefix(dir, "/private")) && cwd != dir {
		t.Fatalf("secret=%q cwd=%q want %q", secret, cwd, dir)
	}
}

func TestCloseStopsTheToolAndRefusesFurtherCalls(t *testing.T) {
	r := New(fakeSpec())
	if _, _, err := callTool(r, context.Background(), "echo", map[string]any{"text": "x"}, nil); err != nil {
		t.Fatal(err)
	}
	p := r.proc
	r.Close()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the tool outlived Close")
	}
	if _, _, err := callTool(r, context.Background(), "echo", nil, nil); !errors.Is(err, ErrStopped) {
		t.Fatalf("err = %v", err)
	}
}

func TestAToolThatCannotStartIsAnError(t *testing.T) {
	r := New(Spec{Name: "ghost", Command: "/no/such/tool"})
	defer r.Close()
	if _, err := r.Info(context.Background()); err == nil || !strings.Contains(err.Error(), "did not start") {
		t.Fatalf("err = %v", err)
	}
}
