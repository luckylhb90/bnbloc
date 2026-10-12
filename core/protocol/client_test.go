package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/core"
)

type requestCapture struct {
	method string
	path   string
	query  string
	body   map[string]any
}

func protocolServer(t *testing.T, handler func(http.ResponseWriter, *http.Request)) (*httptest.Server, *[]requestCapture) {
	t.Helper()
	calls := []requestCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		calls = append(calls, requestCapture{method: r.Method, path: r.URL.EscapedPath(), query: r.URL.RawQuery, body: body})
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func writeDiscover(w http.ResponseWriter, caps ...string) {
	_ = json.NewEncoder(w).Encode(Discovery{
		Name:         "proto-core",
		Version:      "1.4.2",
		Protocol:     ProtocolRange{Min: "1.0", Max: "1.0"},
		Capabilities: caps,
		Limits:       Limits{MaxInputBytes: 16384, MaxConcurrentTurns: 4},
	})
}

func TestClient_ProviderMethods_useProtocolPaths_whenDiscoverySucceeds(t *testing.T) {
	// Given
	srv, calls := protocolServer(t, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		switch {
		case r.Method == http.MethodGet && path == PathDiscover:
			writeDiscover(w)
		case r.Method == http.MethodPost && path == PathWorkspaces:
			_ = json.NewEncoder(w).Encode(CreateWorkspaceResponse{WorkspaceID: "ws-1"})
		case r.Method == http.MethodPost && path == "/core/v1/workspaces/ws-1/sessions":
			_ = json.NewEncoder(w).Encode(CreateSessionResponse{SessionID: "s-1", TurnID: "t-1", Phase: "running"})
		case r.Method == http.MethodPost && path == "/core/v1/workspaces/ws-1/sessions/s-1/input":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && path == PathTargets:
			_ = json.NewEncoder(w).Encode(ListTargetsResponse{Targets: []Target{{ID: "local:codex", Provider: "codex"}}})
		case r.Method == http.MethodGet && path == "/core/v1/workspaces/ws-1/sessions/s-1/messages":
			_ = json.NewEncoder(w).Encode(MessagesResponse{Messages: []Message{{Version: 3, Role: "assistant", Text: "hi", UserVisible: true, Completed: true}}, LatestVersion: 3})
		case r.Method == http.MethodGet && path == "/core/v1/workspaces/ws-1/sessions/s-1/turn":
			_ = json.NewEncoder(w).Encode(PollTurnResponse{Idle: true})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	client := NewClient("proto", srv.Client())
	ep := core.Endpoint{BaseURL: srv.URL, Token: "tok"}
	ctx := context.Background()

	// When
	ws, err := client.CreateWorkspace(ctx, ep)
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	res, err := client.CreateSession(ctx, ep, core.SessionRequest{WorkspaceID: ws, TargetID: "local:codex", Prompt: "hello"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := client.SendInput(ctx, ep, ws, res.SessionID, "next", core.SessionOptions{Model: "gpt-5.5"}); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	targets, err := client.ListTargets(ctx, ep)
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	msgs, cursor, err := client.Messages(ctx, ep, ws, res.SessionID, 2)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	turn, err := client.PollTurn(ctx, ep, ws, res.SessionID)
	if err != nil {
		t.Fatalf("PollTurn: %v", err)
	}

	// Then
	if ws != "ws-1" || res.SessionID != "s-1" || len(targets) != 1 || len(msgs) != 1 || cursor != 3 || !turn.Idle {
		t.Fatalf("unexpected results: ws=%q res=%+v targets=%+v msgs=%+v cursor=%d turn=%+v", ws, res, targets, msgs, cursor, turn)
	}
	discoverCalls := 0
	for _, call := range *calls {
		if call.path == PathDiscover {
			discoverCalls++
		}
	}
	if discoverCalls != 1 {
		t.Fatalf("discover calls=%d want 1 cached discovery", discoverCalls)
	}
	last := (*calls)[len(*calls)-2]
	if last.path != "/core/v1/workspaces/ws-1/sessions/s-1/messages" || last.query != "after=2" {
		t.Fatalf("messages request = %s?%s", last.path, last.query)
	}
}

func TestClient_Discovery_returnsVersionMismatch_whenMajorRangesDoNotOverlap(t *testing.T) {
	// Given
	srv, _ := protocolServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Discovery{Name: "future", Version: "2.0.0", Protocol: ProtocolRange{Min: "2.0", Max: "2.1"}})
	})
	client := NewClient("proto", srv.Client())

	// When
	_, err := client.ListTargets(context.Background(), core.Endpoint{BaseURL: srv.URL})

	// Then
	var mismatch *VersionMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err=%v want VersionMismatchError", err)
	}
	if mismatch.LocalMin != SupportedMinVersion || mismatch.RemoteMin != "2.0" {
		t.Fatalf("mismatch = %+v", mismatch)
	}
}

func TestClient_OptionalCapability_returnsLocalError_whenNotAdvertised(t *testing.T) {
	// Given
	srv, calls := protocolServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeDiscover(w)
	})
	client := NewClient("proto", srv.Client())

	// When
	_, implementsInterrupt := core.As[core.TurnInterrupter](context.Background(), client, core.Endpoint{BaseURL: srv.URL}, core.CapabilityInterrupt)

	// Then
	if implementsInterrupt {
		t.Fatal("provider must not implement TurnInterrupter when interrupt is not advertised")
	}
	if len(*calls) != 1 || (*calls)[0].path != PathDiscover {
		t.Fatalf("optional endpoint called despite missing capability: %+v", *calls)
	}
}

func TestClient_OptionalCapability_callsEndpoint_whenAdvertised(t *testing.T) {
	// Given
	srv, calls := protocolServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case PathDiscover:
			writeDiscover(w, CapabilityInterrupt, CapabilityWriteFile, CapabilityDeleteWorkspace)
		case "/core/v1/workspaces/ws-1/sessions/s-1/interrupt", "/core/v1/workspaces/ws-1/files/.room%2Fhistory.md", "/core/v1/workspaces/ws-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ep := core.Endpoint{BaseURL: srv.URL}
	client := NewClient("proto", srv.Client())
	ctx := context.Background()
	interrupter, ok := core.As[core.TurnInterrupter](ctx, client, ep, core.CapabilityInterrupt)
	if !ok {
		t.Fatal("provider must implement TurnInterrupter when interrupt is advertised")
	}
	writer, ok := core.As[core.WorkspaceFileWriter](ctx, client, ep, core.CapabilityWriteFile)
	if !ok {
		t.Fatal("provider must implement WorkspaceFileWriter when writeFile is advertised")
	}
	deleter, ok := core.As[core.WorkspaceDeleter](ctx, client, ep, core.CapabilityDeleteWorkspace)
	if !ok {
		t.Fatal("provider must implement WorkspaceDeleter when deleteWorkspace is advertised")
	}

	// When
	if err := interrupter.Interrupt(context.Background(), ep, "ws-1", "s-1"); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if err := writer.WriteWorkspaceFile(context.Background(), ep, "ws-1", ".room/history.md", "hello"); err != nil {
		t.Fatalf("WriteWorkspaceFile: %v", err)
	}
	if err := deleter.DeleteWorkspace(context.Background(), ep, "ws-1"); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}

	// Then
	want := []requestCapture{
		{method: http.MethodGet, path: PathDiscover},
		{method: http.MethodPost, path: "/core/v1/workspaces/ws-1/sessions/s-1/interrupt"},
		{method: http.MethodPut, path: "/core/v1/workspaces/ws-1/files/.room%2Fhistory.md"},
		{method: http.MethodDelete, path: "/core/v1/workspaces/ws-1"},
	}
	if len(*calls) != len(want) {
		t.Fatalf("calls=%+v want %+v", *calls, want)
	}
	for i := range want {
		call := (*calls)[i]
		if call.method != want[i].method || call.path != want[i].path {
			t.Fatalf("call[%d]=%s %s want %s %s", i, call.method, call.path, want[i].method, want[i].path)
		}
	}
}

func TestClient_ErrorEnvelope_mapsToProtocolError(t *testing.T) {
	// Given
	srv, _ := protocolServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == PathDiscover {
			writeDiscover(w)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		no := false
		_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ProtocolError{Code: ErrorSessionNotFound, Message: "missing", Retryable: &no}})
	})
	client := NewClient("proto", srv.Client())

	// When
	_, err := client.PollTurn(context.Background(), core.Endpoint{BaseURL: srv.URL}, "ws-404", "s-404")

	// Then
	var protoErr *ProtocolError
	if !errors.As(err, &protoErr) {
		t.Fatalf("err=%v want ProtocolError", err)
	}
	if protoErr.Code != ErrorSessionNotFound || protoErr.Retryable == nil || *protoErr.Retryable {
		t.Fatalf("protocol error = %+v", protoErr)
	}
}

// The envelope's retryable hint is optional: a 429/503 envelope without it
// is retried by the status rule (the daemon did not process the request),
// while a stated verdict wins either way.
func TestClient_RetryHint_absentFallsBackToStatus(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name   string
		status int
		hint   *bool
		want   bool
	}{
		{"503 without a hint", http.StatusServiceUnavailable, nil, true},
		{"429 without a hint", http.StatusTooManyRequests, nil, true},
		{"503 stated not retryable", http.StatusServiceUnavailable, &no, false},
		{"500 without a hint (ambiguous)", http.StatusInternalServerError, nil, false},
		{"500 stated retryable", http.StatusInternalServerError, &yes, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := protocolServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == PathDiscover {
					writeDiscover(w)
					return
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ProtocolError{Code: ErrorCoreUnavailable, Message: "busy", Retryable: tc.hint}})
			})
			client := NewClient("proto", srv.Client())
			_, err := client.CreateSession(context.Background(), core.Endpoint{BaseURL: srv.URL}, core.SessionRequest{WorkspaceID: "ws-1", Prompt: "hi"})
			var protoErr *ProtocolError
			if !errors.As(err, &protoErr) || protoErr.Code != ErrorCoreUnavailable {
				t.Fatalf("err = %v, want the core_unavailable envelope", err)
			}
			if got := core.Retryable(err, false); got != tc.want {
				t.Fatalf("Retryable(create session) = %v, want %v", got, tc.want)
			}
		})
	}
}

// Core Protocol v1 defines no session-workdir endpoint, so the protocol client
// must not offer it: the default sync dir is skipped for protocol cores rather
// than faked.
func TestClient_DoesNotOfferSessionWorkdir(t *testing.T) {
	var provider core.Provider = NewClient("proto", nil)
	if _, ok := provider.(core.SessionWorkdirReporter); ok {
		t.Fatal("protocol client must not implement core.SessionWorkdirReporter (no Core Protocol endpoint)")
	}
}

// sessionFiles + sessionFileBytes make a protocol core a deliverable source:
// the listing maps the camelCase wire entries onto core.SessionFile, and a
// read returns the exact raw body bytes (binary-safe) from the workspace file
// route, the rel path escaped as one segment.
func TestClient_SessionFiles_listAndReadExactBytes_whenAdvertised(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0xff, 0x10}
	srv, calls := protocolServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case PathDiscover:
			writeDiscover(w, CapabilitySessionFiles, CapabilitySessionFileBytes)
		case "/core/v1/workspaces/ws-1/sessions/s-1/files":
			listBody := `{"files":[{"path":"docs/spec.md","sizeBytes":12,"modTimeMs":1700},{"path":"img/logo.png","sizeBytes":11,"modTimeMs":1800},{"path":"","sizeBytes":1}],"truncated":true}`
			_, _ = w.Write([]byte(listBody))
		case "/core/v1/workspaces/ws-1/files/img%2Flogo.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(png)
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ProtocolError{Code: ErrorFileNotFound, Message: "no such file"}})
		}
	})
	ep := core.Endpoint{BaseURL: srv.URL}
	client := NewClient("proto", srv.Client())
	ctx := context.Background()
	files, ok := core.As[core.SessionFiles](ctx, client, ep, core.CapabilitySessionFiles)
	if !ok {
		t.Fatal("provider must offer SessionFiles when sessionFiles is advertised")
	}
	if _, ok := core.As[core.SessionFiles](ctx, client, ep, core.CapabilitySessionFileBytes); !ok {
		t.Fatal("provider must report sessionFileBytes when advertised")
	}

	got, err := files.ListSessionFiles(ctx, ep, "ws-1", "s-1")
	if err != nil {
		t.Fatalf("ListSessionFiles: %v", err)
	}
	want := []core.SessionFile{{Path: "docs/spec.md", Bytes: 12, MtimeMs: 1700}, {Path: "img/logo.png", Bytes: 11, MtimeMs: 1800}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ListSessionFiles = %+v, want %+v (empty paths dropped)", got, want)
	}
	content, err := files.ReadSessionFile(ctx, ep, "ws-1", "s-1", "img/logo.png")
	if err != nil {
		t.Fatalf("ReadSessionFile: %v", err)
	}
	if string(content) != string(png) {
		t.Fatalf("ReadSessionFile = %v, want the exact bytes %v", content, png)
	}
	_, err = files.ReadSessionFile(ctx, ep, "ws-1", "s-1", "missing.bin")
	var protoErr *ProtocolError
	if !errors.As(err, &protoErr) || protoErr.Code != ErrorFileNotFound {
		t.Fatalf("missing file err = %v, want file_not_found ProtocolError", err)
	}
	if (*calls)[2].method != http.MethodGet || (*calls)[2].path != "/core/v1/workspaces/ws-1/files/img%2Flogo.png" {
		t.Fatalf("read call = %+v", (*calls)[2])
	}
}

// Without the capabilities the protocol client offers no deliverable source,
// and a direct call fails with capability_missing without touching the route.
func TestClient_SessionFiles_absent_whenNotAdvertised(t *testing.T) {
	srv, calls := protocolServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeDiscover(w, CapabilityWriteFile)
	})
	ep := core.Endpoint{BaseURL: srv.URL}
	client := NewClient("proto", srv.Client())
	ctx := context.Background()
	if _, ok := core.As[core.SessionFiles](ctx, client, ep, core.CapabilitySessionFiles); ok {
		t.Fatal("SessionFiles offered without sessionFiles advertised")
	}
	_, err := client.ReadSessionFile(ctx, ep, "ws-1", "s-1", "a.png")
	var protoErr *ProtocolError
	if !errors.As(err, &protoErr) || protoErr.Code != ErrorCapabilityMissing {
		t.Fatalf("ReadSessionFile err = %v, want capability_missing", err)
	}
	if _, err := client.ListSessionFiles(ctx, ep, "ws-1", "s-1"); err == nil {
		t.Fatal("ListSessionFiles without sessionFiles must fail")
	}
	for _, c := range *calls {
		if c.path != PathDiscover {
			t.Fatalf("session-file route called without the capability: %+v", c)
		}
	}
}

// The wire contract is explicit camelCase JSON, independent of core's Go field
// names (BR-25).
func TestClient_WireShape_isExplicitCamelCase(t *testing.T) {
	var sessionBody, inputBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case PathDiscover:
			writeDiscover(w)
		case "/core/v1/workspaces/ws-1/sessions":
			_ = json.NewDecoder(r.Body).Decode(&sessionBody)
			_, _ = w.Write([]byte(`{"sessionId":"s-1","turnId":"t-1","phase":"running"}`))
		case "/core/v1/workspaces/ws-1/sessions/s-1/input":
			_ = json.NewDecoder(r.Body).Decode(&inputBody)
			w.WriteHeader(http.StatusNoContent)
		case "/core/v1/workspaces/ws-1/sessions/s-1/messages":
			_, _ = w.Write([]byte(`{"messages":[{"version":4,"role":"assistant","text":"hi","userVisible":true,"completed":true,"messageId":"m-4","seq":4,"createdAtMs":1700}],"latestVersion":4}`))
		case "/core/v1/workspaces/ws-1/sessions/s-1/turn":
			_, _ = w.Write([]byte(`{"idle":true,"failed":true,"failMessage":"no key"}`))
		case PathTargets:
			_, _ = w.Write([]byte(`{"targets":[{"id":"codex","provider":"codex","models":["m1"],"defaultModel":"m1"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := NewClient("proto", srv.Client())
	ep := core.Endpoint{BaseURL: srv.URL}
	ctx := context.Background()
	res, err := c.CreateSession(ctx, ep, core.SessionRequest{WorkspaceID: "ws-1", TargetID: "codex", Prompt: "go", Options: core.SessionOptions{Model: "m1", ReasoningEffort: "high"}})
	if err != nil || res.SessionID != "s-1" || res.TurnID != "t-1" || res.Phase != "running" {
		t.Fatalf("CreateSession = %+v, %v", res, err)
	}
	if sessionBody["targetId"] != "codex" || sessionBody["prompt"] != "go" {
		t.Fatalf("create session body = %v, want camelCase targetId/prompt", sessionBody)
	}
	if opts, _ := sessionBody["options"].(map[string]any); opts["model"] != "m1" || opts["reasoningEffort"] != "high" {
		t.Fatalf("create session options = %v", sessionBody["options"])
	}
	if err := c.SendInput(ctx, ep, "ws-1", "s-1", "more", core.SessionOptions{Model: "m2"}); err != nil {
		t.Fatal(err)
	}
	if inputBody["text"] != "more" || inputBody["options"].(map[string]any)["model"] != "m2" {
		t.Fatalf("input body = %v", inputBody)
	}
	msgs, latest, err := c.Messages(ctx, ep, "ws-1", "s-1", 0)
	if err != nil || latest != 4 || len(msgs) != 1 {
		t.Fatalf("Messages = %+v %d %v", msgs, latest, err)
	}
	if m := msgs[0]; m.Version != 4 || !m.UserVisible || !m.Completed || m.MessageID != "m-4" || m.Seq != 4 || m.CreatedAtMs != 1700 {
		t.Fatalf("message = %+v", m)
	}
	ts, err := c.PollTurn(ctx, ep, "ws-1", "s-1")
	if err != nil || !ts.Idle || !ts.Failed || ts.FailMessage != "no key" {
		t.Fatalf("PollTurn = %+v %v", ts, err)
	}
	targets, err := c.ListTargets(ctx, ep)
	if err != nil || len(targets) != 1 || targets[0].ID != "codex" || targets[0].DefaultModel != "m1" || len(targets[0].Models) != 1 {
		t.Fatalf("ListTargets = %+v %v", targets, err)
	}
}

// A daemon that is down when the client is built is discovered on first use
// once it is up (BR-4): construction is lazy and transport failures are never
// cached.
func TestClient_Discovery_isLazyAndRecoversWhenDaemonComesUp(t *testing.T) {
	var up atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		switch r.URL.EscapedPath() {
		case PathDiscover:
			writeDiscover(w, CapabilityInterrupt)
		case PathTargets:
			_, _ = w.Write([]byte(`{"targets":[{"id":"t","provider":"p"}]}`))
		}
	}))
	defer srv.Close()
	c := NewClient("proto", srv.Client())
	ep := core.Endpoint{BaseURL: srv.URL}
	if _, err := c.ListTargets(context.Background(), ep); err == nil {
		t.Fatal("ListTargets against a down daemon must fail")
	}
	if c.Supports(context.Background(), ep, core.CapabilityInterrupt) {
		t.Fatal("a down daemon supports nothing optional")
	}
	up.Store(true)
	if ts, err := c.ListTargets(context.Background(), ep); err != nil || len(ts) != 1 {
		t.Fatalf("ListTargets after daemon came up = %+v, %v", ts, err)
	}
	if !c.Supports(context.Background(), ep, core.CapabilityInterrupt) {
		t.Fatal("capability advertised after the daemon came up must be supported")
	}
}

// Discovery is re-fetched after discoveryTTL so an in-place upgrade that
// changes capabilities is noticed without a restart (BR-25).
func TestClient_Discovery_expiresAfterTTL(t *testing.T) {
	var caps atomic.Value
	caps.Store([]string{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeDiscover(w, caps.Load().([]string)...)
	}))
	defer srv.Close()
	now := time.Unix(1000, 0)
	c := NewClient("proto", srv.Client())
	c.now = func() time.Time { return now }
	ep := core.Endpoint{BaseURL: srv.URL}
	if c.Supports(context.Background(), ep, core.CapabilityWriteFile) {
		t.Fatal("writeFile not advertised yet")
	}
	caps.Store([]string{CapabilityWriteFile})
	if c.Supports(context.Background(), ep, core.CapabilityWriteFile) {
		t.Fatal("cached discovery must be reused within the TTL")
	}
	now = now.Add(discoveryTTL + time.Second)
	if !c.Supports(context.Background(), ep, core.CapabilityWriteFile) {
		t.Fatal("discovery must be refreshed after the TTL")
	}
}

func TestClient_SandboxExec_gatedByDiscovery_andCarriedOnTheWire(t *testing.T) {
	// Given a core advertising sandboxExec
	srv, calls := protocolServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case PathDiscover:
			writeDiscover(w, CapabilitySandboxExec)
		case PathWorkspaces:
			_ = json.NewEncoder(w).Encode(CreateWorkspaceResponse{WorkspaceID: "ws-9"})
		case "/core/v1/workspaces/ws-9/sessions/s-9/input":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	client := NewClient("proto", srv.Client())
	ep := core.Endpoint{BaseURL: srv.URL}
	ctx := context.Background()
	if !core.Supports(ctx, client, ep, core.CapabilitySandboxExec) {
		t.Fatal("sandboxExec advertised but not supported")
	}
	creator, ok := core.As[core.WorkdirWorkspaceCreator](ctx, client, ep, core.CapabilitySandboxExec)
	if !ok {
		t.Fatal("WorkdirWorkspaceCreator not offered with sandboxExec")
	}

	// When
	ws, err := creator.CreateWorkspaceAt(ctx, ep, "/h/bnbloc-workspaces/s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SendInput(ctx, ep, ws, "s-9", "go", core.SessionOptions{Execution: sampleExecution()}); err != nil {
		t.Fatal(err)
	}

	// Then
	got := *calls
	if got[1].body["workdir"] != "/h/bnbloc-workspaces/s1" {
		t.Fatalf("create workspace body = %+v", got[1].body)
	}
	exec, _ := got[2].body["execution"].(map[string]any)
	sb, _ := exec["sandbox"].(map[string]any)
	if exec["mode"] != "sandbox" || sb["execToken"] != "tok" || sb["space"] != "r1-c-p" {
		t.Fatalf("input execution = %+v", got[2].body)
	}

	// A core without the capability is never offered either.
	plain, _ := protocolServer(t, func(w http.ResponseWriter, r *http.Request) { writeDiscover(w) })
	if core.Supports(ctx, NewClient("p", plain.Client()), core.Endpoint{BaseURL: plain.URL}, core.CapabilitySandboxExec) {
		t.Fatal("sandboxExec supported without being advertised")
	}
}
