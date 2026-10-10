package manifestcore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
)

// restManifest reproduces a REST daemon core's control path as data, proving a
// real REST core is expressible without hand-written Go.
const restManifest = `{
  "name": "fakerest",
  "discover": { "probeUrl": "REPLACED", "probePath": "/v1/info" },
  "createWorkspace": { "method": "POST", "path": "/v1/projects",
    "body": { "name": "bnbloc-{shortid}" }, "extract": { "workspaceId": "id" } },
  "createSession": { "method": "POST", "path": "/v1/sessions",
    "body": { "agent_id": "{targetId|firstTarget}", "project_id": "{workspaceId}", "prompt": "{prompt}" },
    "extract": { "sessionId": "id", "turnId": "active_response_id", "phase": "status" } },
  "sendInput": { "method": "POST", "path": "/v1/sessions/{sessionId}/events",
    "body": { "text": "{text}" } },
  "listTargets": { "method": "GET", "path": "/v1/agents",
    "targets": { "list": "data", "id": "id", "provider": "harness" } },
  "sessionIdle": { "method": "GET", "path": "/v1/sessions/{sessionId}",
    "idleWhen": { "field": "status", "notIn": ["running"] } },
  "latestSession": { "method": "GET", "path": "/v1/sessions",
    "targets": { "list": "data", "id": "id" } }
}`

type capture struct {
	method, path string
	body         map[string]any
}

func fakeRESTDaemon(t *testing.T, caps *[]capture) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		*caps = append(*caps, capture{method: r.Method, path: r.URL.Path, body: body})
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/info":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/projects":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "proj-1"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sess-1", "active_response_id": "turn-1", "status": "running"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions/sess-1/events":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{
				map[string]any{"id": "agent-a", "harness": "codex"},
				map[string]any{"id": "agent-b", "harness": "claude"},
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "idle"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "sess-1"}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// A manifest drives a real REST daemon through all six verbs with the correct
// method/path/body and extracts the right response fields.
func TestManifestReproducesRESTDaemon(t *testing.T) {
	var caps []capture
	srv := fakeRESTDaemon(t, &caps)
	defer srv.Close()

	p, err := Parse([]byte(restManifest))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ep := core.Endpoint{BaseURL: srv.URL}
	ctx := context.Background()

	ws, err := p.CreateWorkspace(ctx, ep)
	if err != nil || ws != "proj-1" {
		t.Fatalf("CreateWorkspace = %q, %v; want proj-1", ws, err)
	}

	// Empty target => firstTarget fallback => ListTargets then bind agent-a.
	res, err := p.CreateSession(ctx, ep, core.SessionRequest{WorkspaceID: ws, Prompt: "hi"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if res.SessionID != "sess-1" || res.TurnID != "turn-1" || res.Phase != "running" {
		t.Fatalf("session result = %+v", res)
	}
	// Verify the create-session body carried the resolved first target + prompt.
	var createBody map[string]any
	for _, c := range caps {
		if c.method == http.MethodPost && c.path == "/v1/sessions" {
			createBody = c.body
		}
	}
	if createBody["agent_id"] != "agent-a" || createBody["project_id"] != "proj-1" || createBody["prompt"] != "hi" {
		t.Fatalf("create-session body = %#v", createBody)
	}

	targets, err := p.ListTargets(ctx, ep)
	if err != nil || len(targets) != 2 || targets[0].ID != "agent-a" || targets[0].Provider != "codex" {
		t.Fatalf("ListTargets = %+v, %v", targets, err)
	}

	if err := p.SendInput(ctx, ep, ws, "sess-1", "next", core.SessionOptions{}); err != nil {
		t.Fatalf("SendInput: %v", err)
	}

	idle, err := p.SessionIdle(ctx, ep, ws, "sess-1")
	if err != nil || !idle {
		t.Fatalf("SessionIdle = %v, %v; want idle (status idle not in [running])", idle, err)
	}

	latest, err := p.LatestSession(ctx, ep, ws)
	if err != nil || latest != "sess-1" {
		t.Fatalf("LatestSession = %q, %v", latest, err)
	}
}

// sessionFileManifest declares the session-file + workdir ops in a second
// dialect (different paths, nested list, query param name), proving the
// capability is manifest data rather than one daemon's REST shape.
func sessionFileManifest() Manifest {
	m := validManifest("files")
	m.ListSessionFiles = Op{Method: "GET", Path: "/ws/{workspaceId}/s/{sessionId}/tree",
		Items: &ItemsSpec{List: "data.entries", Fields: map[string]string{"path": "name", "bytes": "stat.size", "mtimeMs": "stat.mtime"}}}
	m.ReadSessionFile = Op{Method: "GET", Path: "/ws/{workspaceId}/s/{sessionId}/blob?name={relPath}&raw=0",
		Extract: map[string]string{"content": "blob.text"}}
	m.SessionWorkdir = Op{Method: "GET", Path: "/ws/{workspaceId}/s/{sessionId}",
		Extract: map[string]string{"workdir": "info.dir"}}
	return m
}

// The session-file ops render their templates (ids path-escaped, the file path
// query-escaped) and map the responses through items / extract.
func TestManifestSessionFileOps(t *testing.T) {
	var uris []string
	var gotName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uris = append(uris, r.URL.RequestURI())
		switch {
		case r.URL.Path == "/ws/w 1/s/s?1/tree":
			_, _ = w.Write([]byte(`{"data":{"entries":[{"name":"a.md","stat":{"size":3,"mtime":1700}},{"name":"","stat":{"size":1}},{"name":"b/c.go","stat":{"size":"9","mtime":"1800"}}]}}`))
		case r.URL.Path == "/ws/w 1/s/s?1/blob":
			gotName = r.URL.Query().Get("name")
			if r.URL.Query().Get("raw") != "0" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"blob":{"text":"hello"}}`))
		case r.URL.Path == "/ws/w 1/s/s?1":
			_, _ = w.Write([]byte(`{"info":{"dir":" /proj/x "}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p, err := New(sessionFileManifest())
	if err != nil {
		t.Fatal(err)
	}
	ctx, ep := context.Background(), core.Endpoint{BaseURL: srv.URL}
	files, ok := core.As[core.SessionFiles](ctx, p, ep, core.CapabilitySessionFiles)
	if !ok {
		t.Fatal("listSessionFiles + readSessionFile must expose core.SessionFiles")
	}
	list, err := files.ListSessionFiles(ctx, ep, "w 1", "s?1")
	if err != nil {
		t.Fatalf("ListSessionFiles: %v", err)
	}
	want := []core.SessionFile{{Path: "a.md", Bytes: 3, MtimeMs: 1700}, {Path: "b/c.go", Bytes: 9, MtimeMs: 1800}}
	if len(list) != len(want) || list[0] != want[0] || list[1] != want[1] {
		t.Fatalf("files = %+v, want %+v (empty path dropped)", list, want)
	}
	const rel = "docs/a b&c=1#d+e.md"
	content, err := files.ReadSessionFile(ctx, ep, "w 1", "s?1", rel)
	if err != nil || string(content) != "hello" {
		t.Fatalf("ReadSessionFile = %q, %v", content, err)
	}
	if gotName != rel {
		t.Fatalf("daemon read name %q, want %q (query-escaped)", gotName, rel)
	}
	wd, ok := core.As[core.SessionWorkdirReporter](ctx, p, ep, core.CapabilitySessionWorkdir)
	if !ok {
		t.Fatal("sessionWorkdir must expose core.SessionWorkdirReporter")
	}
	if dir, err := wd.SessionWorkdir(ctx, ep, "w 1", "s?1"); err != nil || dir != "/proj/x" {
		t.Fatalf("SessionWorkdir = %q, %v", dir, err)
	}
	for _, u := range uris {
		if u != "/ws/w%201/s/s%3F1/tree" && u != "/ws/w%201/s/s%3F1/blob?name=docs%2Fa+b%26c%3D1%23d%2Be.md&raw=0" && u != "/ws/w%201/s/s%3F1" {
			t.Fatalf("unexpected request URI %q", u)
		}
	}
}

// extract.contentBase64 makes session-file reads binary-safe: the decoded
// bytes come back exactly, and the core reports sessionFileBytes. A text-only
// manifest does not.
func TestManifestSessionFileBytes(t *testing.T) {
	bin := []byte{0x89, 'P', 'N', 'G', 0x00, 0xff, 0xfe}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"blob":{"b64":"` + base64.StdEncoding.EncodeToString(bin) + `"}}`))
	}))
	defer srv.Close()
	ctx, ep := context.Background(), core.Endpoint{BaseURL: srv.URL}
	text, err := New(sessionFileManifest())
	if err != nil {
		t.Fatal(err)
	}
	if core.Supports(ctx, text, ep, core.CapabilitySessionFileBytes) {
		t.Fatal("a text-only readSessionFile must not report sessionFileBytes")
	}
	m := sessionFileManifest()
	m.ReadSessionFile.Extract = map[string]string{"contentBase64": "blob.b64"}
	p, err := New(m)
	if err != nil {
		t.Fatal(err)
	}
	files, ok := core.As[core.SessionFiles](ctx, p, ep, core.CapabilitySessionFileBytes)
	if !ok {
		t.Fatal("contentBase64 must report sessionFileBytes")
	}
	got, err := files.ReadSessionFile(ctx, ep, "w", "s", "a.png")
	if err != nil || !bytes.Equal(got, bin) {
		t.Fatalf("ReadSessionFile = %v, %v; want the exact bytes", got, err)
	}
}

// A half-declared session-file capability, or an op missing the mapping its
// result is read through, fails at load rather than at first publish.
func TestManifestSessionFileOpsValidation(t *testing.T) {
	cases := map[string]func(*Manifest){
		"list without read":  func(m *Manifest) { m.ReadSessionFile = Op{} },
		"read without list":  func(m *Manifest) { m.ListSessionFiles = Op{} },
		"list without items": func(m *Manifest) { m.ListSessionFiles.Items = nil },
		"list without path field": func(m *Manifest) {
			m.ListSessionFiles.Items = &ItemsSpec{List: "files", Fields: map[string]string{"bytes": "bytes"}}
		},
		"read without content":    func(m *Manifest) { m.ReadSessionFile.Extract = nil },
		"workdir without extract": func(m *Manifest) { m.SessionWorkdir.Extract = map[string]string{"cwd": "x"} },
	}
	for name, mutate := range cases {
		m := sessionFileManifest()
		mutate(&m)
		if _, err := New(m); err == nil {
			t.Errorf("%s: New accepted an invalid manifest", name)
		}
	}
	if _, err := New(sessionFileManifest()); err != nil {
		t.Fatalf("complete session-file manifest rejected: %v", err)
	}
}

// SessionIdle returns false when the status is the running enum value.
func TestManifestIdlePredicate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "running"})
	}))
	defer srv.Close()
	p, _ := Parse([]byte(restManifest))
	idle, err := p.SessionIdle(context.Background(), core.Endpoint{BaseURL: srv.URL}, "w", "sess-1")
	if err != nil || idle {
		t.Fatalf("SessionIdle(running) = %v, %v; want busy", idle, err)
	}
}

// LoadManifests registers a manifest by bytes and refuses to shadow a built-in.
// registerFakeBuiltin installs a non-removable core the way built-ins are
// registered, so shadow protection is testable without any real built-in.
func registerFakeBuiltin(t *testing.T, name string) {
	t.Helper()
	core.Register(name, func() core.Provider { return fakeBuiltinProvider{} })
}

type fakeBuiltinProvider struct{}

func (fakeBuiltinProvider) Name() string { return "fake" }
func (fakeBuiltinProvider) CreateWorkspace(ctx context.Context, ep core.Endpoint) (string, error) {
	return "", nil
}
func (fakeBuiltinProvider) CreateSession(ctx context.Context, ep core.Endpoint, req core.SessionRequest) (core.SessionResult, error) {
	return core.SessionResult{}, nil
}
func (fakeBuiltinProvider) SendInput(ctx context.Context, ep core.Endpoint, workspaceID, sessionID, text string, opts core.SessionOptions) error {
	return nil
}
func (fakeBuiltinProvider) ListTargets(ctx context.Context, ep core.Endpoint) ([]core.Target, error) {
	return nil, nil
}
func (fakeBuiltinProvider) SessionIdle(ctx context.Context, ep core.Endpoint, workspaceID, sessionID string) (bool, error) {
	return true, nil
}
func (fakeBuiltinProvider) LatestSession(ctx context.Context, ep core.Endpoint, workspaceID string) (string, error) {
	return "", nil
}
func (fakeBuiltinProvider) Messages(ctx context.Context, ep core.Endpoint, workspaceID, sessionID string, after int64) ([]core.Message, int64, error) {
	return nil, 0, nil
}
func (fakeBuiltinProvider) PollTurn(ctx context.Context, ep core.Endpoint, workspaceID, sessionID string) (core.TurnStatus, error) {
	return core.TurnStatus{}, nil
}

func TestLoadManifestsRegisters(t *testing.T) {
	registerFakeBuiltin(t, "fakebuiltin")
	myrest := []byte(`{
      "name": "myrest",
      "createWorkspace": {"method":"POST","path":"/w"},
      "createSession": {"method":"POST","path":"/s"},
      "sendInput": {"method":"POST","path":"/i"},
      "listTargets": {"method":"GET","path":"/t"},
      "sessionIdle": {"method":"GET","path":"/idle"},
      "latestSession": {"method":"GET","path":"/latest"}
    }`)
	shadow := []byte(`{
      "name": "fakebuiltin",
      "createWorkspace": {"method":"POST","path":"/w"},
      "createSession": {"method":"POST","path":"/s"},
      "sendInput": {"method":"POST","path":"/i"},
      "listTargets": {"method":"GET","path":"/t"},
      "sessionIdle": {"method":"GET","path":"/idle"},
      "latestSession": {"method":"GET","path":"/latest"}
    }`)
	reg, errs := LoadManifests([][]byte{myrest, shadow})
	foundMyrest := false
	for _, n := range reg {
		if n == "myrest" {
			foundMyrest = true
		}
		if n == "fakebuiltin" {
			t.Fatal("manifest must not register a built-in name")
		}
	}
	if !foundMyrest {
		t.Fatalf("myrest not registered: %v (errs %v)", reg, errs)
	}
	if _, err := core.New("myrest"); err != nil {
		t.Fatalf("registered core not constructible: %v", err)
	}
	if len(errs) == 0 {
		t.Fatal("expected a duplicate-name error for the fakebuiltin shadow")
	}
}

// LoadManifests skips a malformed manifest and reports it, without blocking a
// good one; re-registering the same name replaces it idempotently.
func TestLoadManifestsMalformedAndReregister(t *testing.T) {
	good := []byte(`{
      "name": "reg2",
      "createWorkspace": {"method":"POST","path":"/w"},
      "createSession": {"method":"POST","path":"/s"},
      "sendInput": {"method":"POST","path":"/i"},
      "listTargets": {"method":"GET","path":"/t"},
      "sessionIdle": {"method":"GET","path":"/idle"},
      "latestSession": {"method":"GET","path":"/latest"}
    }`)
	reg, errs := LoadManifests([][]byte{[]byte(`{bad json`), good})
	if len(errs) != 1 {
		t.Fatalf("errs = %v; want 1 for the malformed entry", errs)
	}
	if len(reg) != 1 || reg[0] != "reg2" {
		t.Fatalf("reg = %v; want [reg2]", reg)
	}
	reg2, errs2 := LoadManifests([][]byte{good})
	if len(errs2) != 0 || len(reg2) != 1 {
		t.Fatalf("re-register should replace: reg=%v errs=%v", reg2, errs2)
	}
	if err := core.Unregister("reg2"); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if err := core.Unregister("fakebuiltin"); err == nil {
		t.Fatal("unregistering a built-in must fail")
	}
}
