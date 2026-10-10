package manifestcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
)

// The listener discovery form reads a daemon's descriptor: the file named by
// an env var carries the daemon's random addr+token, and a host
// rewrite makes the loopback address reachable from a container. Unset rewrite
// env applies the manifest default; set-to-empty disables it.
func TestDiscoverListener(t *testing.T) {
	dir := t.TempDir()
	desc := filepath.Join(dir, "daemon.listener.json")
	if err := os.WriteFile(desc, []byte(`{"addr":"127.0.0.1:4545","auth":{"token":"tok-1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_LISTENER_PATH", desc)

	m := validManifest("acme")
	m.Discover = &DiscoverOp{Listener: &ListenerSpec{
		PathEnv: "TEST_LISTENER_PATH", AddrField: "addr", TokenField: "auth.token",
	}, HostRewrite: &HostRewriteSpec{Env: "TEST_HOST_REWRITE", Default: "host.docker.internal"}}
	p, err := New(m)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("unset rewrite env applies default", func(t *testing.T) {
		os.Unsetenv("TEST_HOST_REWRITE")
		ep, ok := p.Discover(context.Background())
		if !ok {
			t.Fatal("discover not found")
		}
		if ep.BaseURL != "http://host.docker.internal:4545" {
			t.Errorf("BaseURL = %q", ep.BaseURL)
		}
		if ep.Token != "tok-1" {
			t.Errorf("Token = %q", ep.Token)
		}
	})
	t.Run("empty rewrite env disables rewrite", func(t *testing.T) {
		t.Setenv("TEST_HOST_REWRITE", "")
		ep, ok := p.Discover(context.Background())
		if !ok {
			t.Fatal("discover not found")
		}
		if ep.BaseURL != "http://127.0.0.1:4545" {
			t.Errorf("BaseURL = %q", ep.BaseURL)
		}
	})
	t.Run("localhost rewritten too", func(t *testing.T) {
		if err := os.WriteFile(desc, []byte(`{"addr":"localhost:9999","auth":{"token":""}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		os.Unsetenv("TEST_HOST_REWRITE")
		ep, ok := p.Discover(context.Background())
		if !ok {
			t.Fatal("discover not found")
		}
		if ep.BaseURL != "http://host.docker.internal:9999" {
			t.Errorf("BaseURL = %q", ep.BaseURL)
		}
	})
	t.Run("missing env or file not found", func(t *testing.T) {
		t.Setenv("TEST_LISTENER_PATH", filepath.Join(dir, "absent.json"))
		if _, ok := p.Discover(context.Background()); ok {
			t.Error("absent descriptor must not discover")
		}
		os.Unsetenv("TEST_LISTENER_PATH")
		if _, ok := p.Discover(context.Background()); ok {
			t.Error("unset env must not discover")
		}
	})
	t.Run("probe token overrides the file token", func(t *testing.T) {
		t.Setenv("TEST_LISTENER_PATH", desc)
		p.ProbeToken = "explicit"
		ep, ok := p.Discover(context.Background())
		if !ok {
			t.Fatal("discover not found")
		}
		if ep.Token != "explicit" {
			t.Errorf("Token = %q", ep.Token)
		}
	})
}

// A manifest's defaultPath locates the descriptor when its env override is
// unset or empty ("~/" expands to the home dir), so a host-side bridge needs
// no core-specific env; a non-empty env still wins.
func TestDiscoverListenerDefaultPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".acme", "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".acme", "run", "daemon.listener.json"), []byte(`{"addr":"127.0.0.1:4545","auth":{"token":"tok-home"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other.json")
	if err := os.WriteFile(other, []byte(`{"addr":"127.0.0.1:5555","auth":{"token":"tok-env"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := validManifest("acme")
	m.Discover = &DiscoverOp{Listener: &ListenerSpec{
		PathEnv: "TEST_LISTENER_PATH", DefaultPath: "~/.acme/run/daemon.listener.json", AddrField: "addr", TokenField: "auth.token",
	}}
	p, err := New(m)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_LISTENER_PATH", "")
	ep, ok := p.Discover(context.Background())
	if !ok || ep.BaseURL != "http://127.0.0.1:4545" || ep.Token != "tok-home" {
		t.Fatalf("default path discover = %+v, %v", ep, ok)
	}
	t.Setenv("TEST_LISTENER_PATH", other)
	ep, ok = p.Discover(context.Background())
	if !ok || ep.BaseURL != "http://127.0.0.1:5555" || ep.Token != "tok-env" {
		t.Fatalf("env override discover = %+v, %v", ep, ok)
	}
}

// unlessSet wins over the phase: a turn that settled is idle even while its
// phase still reads as working — the trap that wedged follow-ups when idle was
// keyed off activeTurnId alone.
func TestIdleUnlessSet(t *testing.T) {
	pred := &IdlePredicate{
		Field:     "session.activeTurn.phase",
		NotIn:     []string{"running", "streaming", "working", "executing"},
		UnlessSet: "session.activeTurn.settledAtUnixMs",
	}
	cases := []struct {
		name string
		json string
		idle bool
	}{
		{"no active turn", `{"session":{}}`, true},
		{"working and unsettled", `{"session":{"activeTurn":{"phase":"running"}}}`, false},
		{"working but settled", `{"session":{"activeTurn":{"phase":"running","settledAtUnixMs":123}}}`, true},
		{"parked phase", `{"session":{"activeTurn":{"phase":"waiting"}}}`, true},
		{"settled null", `{"session":{"activeTurn":{"phase":"streaming","settledAtUnixMs":null}}}`, false},
	}
	for _, tc := range cases {
		var doc any
		if err := json.Unmarshal([]byte(tc.json), &doc); err != nil {
			t.Fatal(err)
		}
		if got := idleFromPredicate(doc, pred); got != tc.idle {
			t.Errorf("%s: idle = %v, want %v", tc.name, got, tc.idle)
		}
	}
}

// enabledWhen keeps disabled rows out of the roster.
func TestExtractTargetsEnabledWhen(t *testing.T) {
	doc := jsonDoc(t, `{"targets":[
		{"id":"local:codex","provider":"codex","enabled":true},
		{"id":"local:cmp","provider":"cmp","enabled":false}
	]}`)
	op := Op{Targets: &TargetsSpec{
		List: "targets", ID: "id", Provider: "provider",
		EnabledWhen: &EqualsPredicate{Field: "enabled", Equals: true},
	}}
	got := op.extractTargets(doc)
	if len(got) != 1 || got[0].ID != "local:codex" {
		t.Errorf("targets = %+v", got)
	}
}

// latestBy picks the element with the max numeric field, last-wins on ties —
// not list order.
func TestLatestBy(t *testing.T) {
	doc := jsonDoc(t, `{"sessions":[
		{"id":"s-old","createdAtUnixMs":10},
		{"id":"s-new","createdAtUnixMs":42},
		{"id":"s-mid","createdAtUnixMs":20}
	]}`)
	spec := &TargetsSpec{List: "sessions", ID: "id"}
	if got, ok := latestByField(doc, spec, "createdAtUnixMs"); !ok || got != "s-new" {
		t.Errorf("latestByField = %q,%v", got, ok)
	}
	tie := jsonDoc(t, `{"sessions":[{"id":"a","createdAtUnixMs":5},{"id":"b","createdAtUnixMs":5}]}`)
	if got, _ := latestByField(tie, spec, "createdAtUnixMs"); got != "b" {
		t.Errorf("tie should be last-wins, got %q", got)
	}
}

// targetIdPrefix exposes TargetResolver; without it the provider must report
// not-resolvable so the bridge falls back to live listing.
func TestResolveTargetPrefix(t *testing.T) {
	withPrefix, _ := New(func() Manifest { m := validManifest("acme"); m.TargetIDPrefix = "local:"; return m }())
	if id, ok := withPrefix.ResolveTarget("codex"); !ok || id != "local:codex" {
		t.Errorf("ResolveTarget = %q,%v", id, ok)
	}
	without, _ := New(validManifest("x"))
	if _, ok := without.ResolveTarget("codex"); ok {
		t.Error("no prefix must not resolve")
	}
}

// The enrich op fills each target's catalog from a per-target secondary call,
// gated per list, with per-model reasoning mapped from an object of arrays.
func TestListTargetsEnrich(t *testing.T) {
	var mu sync.Mutex
	var enriched []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agent-targets":
			fmt.Fprint(w, `{"targets":[{"id":"local:codex","provider":"codex","enabled":true},{"id":"local:cmp","provider":"cmp","enabled":true}]}`)
		default:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			enriched = append(enriched, fmt.Sprint(body["agentTargetId"]))
			mu.Unlock()
			fmt.Fprint(w, `{
				"modelConfig": {"configurable": true, "currentValue": "gpt-5.5", "options": [{"value":"gpt-5.5"},{"value":"gpt-5.6"},{"value":""}]},
				"reasoningConfig": {"configurable": false, "currentValue": "low", "options": [{"value":"low"}]},
				"effectiveSettings": {"model": "", "reasoningEffort": "high"},
				"reasoningOptionsByModel": {"gpt-5.6": {"defaultValue":"medium","options":[{"value":"medium"},{"value":"high"}]}}
			}`)
		}
	}))
	defer srv.Close()

	m := validManifest("acme")
	m.ListTargets = Op{
		Method: "GET", Path: "/v1/agent-targets",
		Targets: &TargetsSpec{
			List: "targets", ID: "id", Provider: "provider",
			EnabledWhen: &EqualsPredicate{Field: "enabled", Equals: true},
			Enrich: &EnrichSpec{
				Method: "POST", Path: "/v1/agent-providers/{provider}/composer-options",
				Body: json.RawMessage(`{"agentTargetId":"{targetId}"}`),
				Fields: map[string]string{
					"defaultModel":     "effectiveSettings.model|modelConfig.currentValue",
					"defaultReasoning": "effectiveSettings.reasoningEffort|reasoningConfig.currentValue",
				},
				Lists: map[string]ListProj{
					"models":           {Path: "modelConfig.options", Value: "value", When: &EqualsPredicate{Field: "modelConfig.configurable", Equals: true}},
					"reasoningEfforts": {Path: "reasoningConfig.options", Value: "value", When: &EqualsPredicate{Field: "reasoningConfig.configurable", Equals: true}},
				},
				Maps: map[string]MapProj{
					"reasoningByModel": {Path: "reasoningOptionsByModel", List: "options", Value: "value"},
				},
			},
		},
	}
	p, err := New(m)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := p.ListTargets(context.Background(), core.Endpoint{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %d", len(targets))
	}
	codex := targets[0]
	if fmt.Sprint(codex.Models) != "[gpt-5.5 gpt-5.6]" {
		t.Errorf("Models = %v", codex.Models)
	}
	if len(codex.ReasoningEfforts) != 0 {
		t.Errorf("ReasoningEfforts = %v (configurable=false must yield none)", codex.ReasoningEfforts)
	}
	if codex.DefaultModel != "gpt-5.5" {
		t.Errorf("DefaultModel = %q (fallback path must fire when effectiveSettings empty)", codex.DefaultModel)
	}
	if codex.DefaultReasoning != "high" {
		t.Errorf("DefaultReasoning = %q", codex.DefaultReasoning)
	}
	if fmt.Sprint(codex.ReasoningByModel) != "map[gpt-5.6:[medium high]]" {
		t.Errorf("ReasoningByModel = %v", codex.ReasoningByModel)
	}
	if len(enriched) != 2 {
		t.Errorf("enrich calls = %v", enriched)
	}
}

// Optional capabilities are reported (core.As) only when the manifest
// configures the op.
func TestCapabilitiesFollowDeclaredOps(t *testing.T) {
	ctx, ep := context.Background(), core.Endpoint{BaseURL: "http://daemon"}
	fullM := validManifest("full")
	fullM.WriteFile = Op{Method: "PUT", Path: "/w/{workspaceId}/room-file"}
	fullM.DeleteWksp = Op{Method: "DELETE", Path: "/w/{workspaceId}"}
	fullM.Interrupt = Op{Method: "POST", Path: "/w/{workspaceId}/s/{sessionId}/interrupt"}
	full, _ := New(fullM)
	if _, ok := core.As[core.WorkspaceFileWriter](ctx, full, ep, core.CapabilityWriteFile); !ok {
		t.Error("writeWorkspaceFile op must expose WorkspaceFileWriter")
	}
	if _, ok := core.As[core.WorkspaceFileBytesWriter](ctx, full, ep, core.CapabilityWriteFile); !ok {
		t.Error("writeWorkspaceFile op must expose WorkspaceFileBytesWriter")
	}
	if _, ok := core.As[core.WorkspaceDeleter](ctx, full, ep, core.CapabilityDeleteWorkspace); !ok {
		t.Error("deleteWorkspace op must expose WorkspaceDeleter")
	}
	if _, ok := core.As[core.TurnInterrupter](ctx, full, ep, core.CapabilityInterrupt); !ok {
		t.Error("interrupt op must expose TurnInterrupter")
	}

	woM := validManifest("wo")
	woM.WriteFile = Op{Method: "PUT", Path: "/w"}
	writeOnly, _ := New(woM)
	if _, ok := core.As[core.WorkspaceDeleter](ctx, writeOnly, ep, core.CapabilityDeleteWorkspace); ok {
		t.Error("no deleteWorkspace op must not expose WorkspaceDeleter")
	}

	bare, _ := New(validManifest("bare"))
	if _, ok := core.As[core.WorkspaceFileWriter](ctx, bare, ep, core.CapabilityWriteFile); ok {
		t.Error("no writeWorkspaceFile op must not expose WorkspaceFileWriter")
	}
	if _, ok := core.As[core.WorkspaceDeleter](ctx, bare, ep, core.CapabilityDeleteWorkspace); ok {
		t.Error("no deleteWorkspace op must not expose WorkspaceDeleter")
	}
	if _, ok := core.As[core.TurnInterrupter](ctx, bare, ep, core.CapabilityInterrupt); ok {
		t.Error("no interrupt op must not expose TurnInterrupter")
	}
	if _, ok := core.As[core.SessionFiles](ctx, bare, ep, core.CapabilitySessionFiles); ok {
		t.Error("no session-file ops must not expose SessionFiles")
	}
	if _, ok := core.As[core.SessionWorkdirReporter](ctx, bare, ep, core.CapabilitySessionWorkdir); ok {
		t.Error("no sessionWorkdir op must not expose SessionWorkdirReporter")
	}
}

// DeleteWorkspace issues the op's method+path with the id.
func TestDeleteWorkspaceOp(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	dwM := validManifest("acme")
	dwM.DeleteWksp = Op{Method: "DELETE", Path: "/v1/workspaces/{workspaceId}"}
	p, _ := New(dwM)
	d, ok := core.As[core.WorkspaceDeleter](context.Background(), p, core.Endpoint{BaseURL: srv.URL}, core.CapabilityDeleteWorkspace)
	if !ok {
		t.Fatal("deleter capability expected")
	}
	if err := d.DeleteWorkspace(context.Background(), core.Endpoint{BaseURL: srv.URL}, "ws-9"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "DELETE /v1/workspaces/ws-9" {
		t.Errorf("got %q", gotPath)
	}
}

// submitId is fresh per call, so a daemon requiring a per-request idempotency
// key never sees a repeat across retries.
func TestSubmitIdFreshPerCall(t *testing.T) {
	var mu sync.Mutex
	var ids []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		ids = append(ids, fmt.Sprint(body["clientSubmitId"]))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	siM := validManifest("acme")
	siM.SendInput = Op{Method: "POST", Path: "/v1/workspaces/{workspaceId}/agent-sessions/{sessionId}/input", Body: json.RawMessage(`{"clientSubmitId":"{submitId}"}`)}
	p, _ := New(siM)
	ep := core.Endpoint{BaseURL: srv.URL}
	_ = p.SendInput(context.Background(), ep, "ws", "s", "hi", core.SessionOptions{})
	_ = p.SendInput(context.Background(), ep, "ws", "s", "again", core.SessionOptions{})
	if len(ids) != 2 || ids[0] == "" || ids[0] == ids[1] {
		t.Errorf("submitIds = %v", ids)
	}
}

func jsonDoc(t *testing.T, raw string) any {
	t.Helper()
	var doc any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// validManifest fills the six required verbs so tests can focus on the field
// under test; New rejects a manifest missing any of them.
func validManifest(name string) Manifest {
	dummy := Op{Method: "GET", Path: "/dummy"}
	return Manifest{
		Name:          name,
		CreateWksp:    dummy,
		CreateSession: dummy,
		SendInput:     dummy,
		ListTargets:   dummy,
		SessionIdle:   dummy,
		LatestSession: dummy,
	}
}

// Manifest calls go through the shared core REST client (BR-16): a daemon
// failure surfaces as a structured *core.HTTPError, so retry classification
// (core.Retryable) and the per-request timeout apply to manifest cores exactly
// as to protocol cores.
func TestCallUsesSharedRESTClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	p, _ := New(validManifest("m"))
	_, err := p.CreateWorkspace(context.Background(), core.Endpoint{BaseURL: srv.URL})
	var he *core.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusServiceUnavailable {
		t.Fatalf("err = %v, want *core.HTTPError 503", err)
	}
	if !core.Retryable(err, false) {
		t.Fatal("a 503 from a manifest daemon must be retryable")
	}
}
