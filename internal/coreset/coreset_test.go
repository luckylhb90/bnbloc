package coreset

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/manifestcore"
	"github.com/luckylhb90/bnbloc/core/protocol"
)

// clearCoreEnv unsets every env var FromEnv reads so a test starts from a known
// baseline regardless of the ambient environment. t.Setenv restores originals.
func clearCoreEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"BNBLOC_CORES",
		"BNBLOC_CORE_PROVIDER",
		"BNBLOC_CORE_ACME_URL", "BNBLOC_CORE_ACME_TOKEN",
		"BNBLOC_CORE_REMOTE_URL", "BNBLOC_CORE_REMOTE_TOKEN",
		"BNBLOC_CORE_NIMBUS_URL", "BNBLOC_CORE_NIMBUS_TOKEN",
		"BNBLOC_CORE_MANIFESTS_DIR",
	} {
		t.Setenv(k, "")
	}
}

// writeAcmeManifest drops a manifest core named "acme" (including the optional
// session-file and workdir ops) into dir, the shape BNBLOC_CORE_MANIFESTS_DIR
// carries.
func writeAcmeManifest(t *testing.T, dir string) {
	t.Helper()
	manifest := []byte(`{
		"name":"acme",
		"targetIdPrefix":"local:",
		"createWorkspace":{"method":"POST","path":"/v1/workspaces","extract":{"workspaceId":"workspace.id"}},
		"createSession":{"method":"POST","path":"/v1/workspaces/{workspaceId}/sessions"},
		"sendInput":{"method":"POST","path":"/v1/workspaces/{workspaceId}/sessions/{sessionId}/input"},
		"listTargets":{"method":"GET","path":"/v1/targets","targets":{"list":"targets","id":"id","provider":"provider"}},
		"sessionIdle":{"method":"GET","path":"/v1/workspaces/{workspaceId}/sessions/{sessionId}"},
		"latestSession":{"method":"GET","path":"/v1/workspaces/{workspaceId}/sessions"},
		"listSessionFiles":{"method":"GET","path":"/v1/workspaces/{workspaceId}/sessions/{sessionId}/files","items":{"list":"files","fields":{"path":"path"}}},
		"readSessionFile":{"method":"GET","path":"/v1/workspaces/{workspaceId}/sessions/{sessionId}/file?path={relPath}","extract":{"content":"content"}},
		"sessionWorkdir":{"method":"GET","path":"/v1/workspaces/{workspaceId}/sessions/{sessionId}","extract":{"workdir":"session.cwd"}}
	}`)
	if err := os.WriteFile(filepath.Join(dir, "acme.json"), manifest, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func TestFromEnvLoadsManifestDirectoryForDesktopSidecars(t *testing.T) {
	clearCoreEnv(t)
	const name = "sidecar-manifest-test"
	dir := t.TempDir()
	manifest := []byte(`{
		"name":"sidecar-manifest-test",
		"createWorkspace":{"method":"POST","path":"/v1/workspaces"},
		"createSession":{"method":"POST","path":"/v1/workspaces/{workspaceId}/agent-sessions"},
		"sendInput":{"method":"POST","path":"/v1/workspaces/{workspaceId}/agent-sessions/{sessionId}/input"},
		"listTargets":{"method":"GET","path":"/v1/agent-targets","targets":{"list":"targets","id":"id","provider":"provider"}},
		"sessionIdle":{"method":"GET","path":"/v1/workspaces/{workspaceId}/agent-sessions/{sessionId}"},
		"latestSession":{"method":"GET","path":"/v1/workspaces/{workspaceId}/agent-sessions"}
	}`)
	if err := os.WriteFile(filepath.Join(dir, name+".json"), manifest, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	t.Cleanup(func() { _ = core.Unregister(name) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/agent-targets" {
			_ = json.NewEncoder(w).Encode(map[string]any{"targets": []map[string]any{{"id": "t1", "provider": "p1"}}})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv("BNBLOC_CORE_MANIFESTS_DIR", dir)
	t.Setenv("BNBLOC_CORES", name)
	t.Setenv("BNBLOC_CORE_SIDECAR_MANIFEST_TEST_URL", srv.URL)

	entries, err := FromEnv(nil)
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != name || !entries[0].Explicit {
		t.Fatalf("want explicit manifest entry, got %+v", entries)
	}
	if _, ok := entries[0].Provider.(*manifestcore.Provider); !ok {
		t.Fatalf("want manifest provider, got %T", entries[0].Provider)
	}
	targets, err := entries[0].Provider.ListTargets(t.Context(), entries[0].Endpoint)
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	if len(targets) != 1 || targets[0].Provider != "p1" {
		t.Fatalf("manifest provider did not drive sidecar target list: %+v", targets)
	}
}

func TestFromEnvEmptyWhenNoCoresConfigured(t *testing.T) {
	clearCoreEnv(t)
	entries, err := FromEnv(nil)
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("unset BNBLOC_CORES must yield no cores (no baked-in default), got %+v", entries)
	}
}

// A manifest core is the canonical env-configured core: a name in
// BNBLOC_CORES resolves through the manifests dir, with no compiled-in
// provider anywhere in the binary.
func TestFromEnvExplicitCoreListed(t *testing.T) {
	clearCoreEnv(t)
	dir := t.TempDir()
	writeAcmeManifest(t, dir)
	t.Setenv("BNBLOC_CORE_MANIFESTS_DIR", dir)
	t.Setenv("BNBLOC_CORES", "acme")
	entries, err := FromEnv(nil)
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "acme" {
		t.Fatalf("want single acme entry, got %+v", entries)
	}
	if _, ok := entries[0].Provider.(*manifestcore.Provider); !ok {
		t.Fatalf("want a manifestcore provider, got %T", entries[0].Provider)
	}
	// Deliverable publishing + the default sync dir reach a manifest core only
	// through its manifest-declared capabilities.
	ctx, ep := context.Background(), core.Endpoint{BaseURL: "http://acme"}
	if _, ok := core.As[core.SessionFiles](ctx, entries[0].Provider, ep, core.CapabilitySessionFiles); !ok {
		t.Fatal("loaded manifest must offer core.SessionFiles")
	}
	if _, ok := core.As[core.SessionWorkdirReporter](ctx, entries[0].Provider, ep, core.CapabilitySessionWorkdir); !ok {
		t.Fatal("loaded manifest must offer core.SessionWorkdirReporter")
	}
}

// The compose shape: a manifest core from the manifests dir, a second core as a protocol
// adapter driven by URL. Explicit URL/token overrides must land on the
// matching entry, in configured order.
func TestFromEnvMultiCoreOrderAndOverride(t *testing.T) {
	clearCoreEnv(t)
	dir := t.TempDir()
	writeAcmeManifest(t, dir)
	proto := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/core/v1/discover" {
			_, _ = w.Write([]byte(`{"name":"remote","version":"t","protocol":{"min":"1.0","max":"1.0"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer proto.Close()
	t.Setenv("BNBLOC_CORE_MANIFESTS_DIR", dir)
	t.Setenv("BNBLOC_CORES", "acme, remote")
	t.Setenv("BNBLOC_CORE_ACME_URL", "http://host.docker.internal:9000")
	t.Setenv("BNBLOC_CORE_ACME_TOKEN", "tok-acme")
	t.Setenv("BNBLOC_CORE_REMOTE_URL", proto.URL)
	t.Setenv("BNBLOC_CORE_REMOTE_TOKEN", "tok-remote")

	entries, err := FromEnv(nil)
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	// Order follows the configured list.
	if entries[0].Name != "acme" || entries[1].Name != "remote" {
		t.Fatalf("wrong order: %q, %q", entries[0].Name, entries[1].Name)
	}
	if entries[0].Endpoint.BaseURL != "http://host.docker.internal:9000" || entries[0].Endpoint.Token != "tok-acme" {
		t.Fatalf("acme endpoint not from override: %+v", entries[0].Endpoint)
	}
	if entries[1].Endpoint.BaseURL != proto.URL || entries[1].Endpoint.Token != "tok-remote" {
		t.Fatalf("remote endpoint not from override: %+v", entries[1].Endpoint)
	}
	if _, ok := entries[1].Provider.(*protocol.Client); !ok {
		t.Fatalf("want *protocol.Client for the URL-driven core, got %T", entries[1].Provider)
	}
}

func TestFromEnvUnknownCore(t *testing.T) {
	clearCoreEnv(t)
	// A configured core that is not registered is skipped (not fatal), and the
	// known cores in the same list still load — a bad entry in user-editable
	// settings must not crash-loop the process.
	dir := t.TempDir()
	writeAcmeManifest(t, dir)
	t.Setenv("BNBLOC_CORE_MANIFESTS_DIR", dir)
	t.Setenv("BNBLOC_CORES", "nope,acme")
	entries, err := FromEnv(nil)
	if err != nil {
		t.Fatalf("unknown core should be skipped, not error: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "acme" {
		t.Fatalf("want only the known acme core, got %+v", entries)
	}
}

func TestFromEnvAllUnknownCores(t *testing.T) {
	clearCoreEnv(t)
	t.Setenv("BNBLOC_CORES", "nope,alsonope")
	entries, err := FromEnv(nil)
	if err != nil {
		t.Fatalf("all-unknown should not error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("want no cores, got %+v", entries)
	}
}

func TestFromEnvUsesProtocolClientForExplicitUnknownCore(t *testing.T) {
	clearCoreEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != protocol.PathDiscover {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(protocol.Discovery{
			Name:         "nimbus",
			Version:      "1.0.0",
			Protocol:     protocol.ProtocolRange{Min: "1.0", Max: "1.0"},
			Capabilities: []string{protocol.CapabilityInterrupt},
		})
	}))
	defer srv.Close()
	t.Setenv("BNBLOC_CORES", "nimbus")
	t.Setenv("BNBLOC_CORE_NIMBUS_URL", srv.URL)
	t.Setenv("BNBLOC_CORE_NIMBUS_TOKEN", "tok")

	entries, err := FromEnv(nil)
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "nimbus" || entries[0].Endpoint.Token != "tok" || !entries[0].Explicit {
		t.Fatalf("want explicit protocol nimbus entry, got %+v", entries)
	}
	if _, ok := core.As[core.TurnInterrupter](context.Background(), entries[0].Provider, entries[0].Endpoint, core.CapabilityInterrupt); !ok {
		t.Fatalf("protocol provider did not expose advertised interrupt capability: %T", entries[0].Provider)
	}
}

// A URL-declared protocol core whose daemon is not up at boot must still be
// configured, listed, and usable once the daemon starts (BR-4): it used to be
// skipped when boot-time discovery failed and stayed lost until a restart.
func TestFromEnvKeepsURLCoreWhoseDaemonIsDownAtBoot(t *testing.T) {
	clearCoreEnv(t)
	const name = "latecore"
	t.Cleanup(func() { _ = core.Unregister(name) })
	var up atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case protocol.PathDiscover:
			_, _ = w.Write([]byte(`{"name":"latecore","version":"t","protocol":{"min":"1.0","max":"1.0"}}`))
		case protocol.PathTargets:
			_, _ = w.Write([]byte(`{"targets":[{"id":"t1","provider":"codex"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("BNBLOC_CORES", name)
	t.Setenv("BNBLOC_CORE_LATECORE_URL", srv.URL)

	entries, err := FromEnv(nil)
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != name || !entries[0].Explicit {
		t.Fatalf("down-at-boot URL core must stay configured, got %+v", entries)
	}
	if _, ok := core.Describe(name); !ok {
		t.Fatal("down-at-boot URL core must still be listed")
	}
	up.Store(true)
	ts, err := entries[0].Provider.ListTargets(context.Background(), entries[0].Endpoint)
	if err != nil || len(ts) != 1 || ts[0].Provider != "codex" {
		t.Fatalf("core must be usable once its daemon is up: %+v, %v", ts, err)
	}
}

// fakeDiscoverable is a core.Provider whose Discover result can change between
// calls, simulating a local daemon that restarts onto a new address.
type fakeDiscoverable struct {
	core.Provider
	ep core.Endpoint
}

func (f *fakeDiscoverable) Discover(context.Context) (core.Endpoint, bool) {
	return f.ep, f.ep.BaseURL != ""
}

// Rediscover must follow a discovered daemon across a restart (a new random
// port/token), keep the last-known endpoint on a transient discovery miss, and
// never touch an explicitly-configured endpoint. This guards the outage where a
// daemon restart changed its port and the gateway kept hitting the dead one.
func TestRediscoverFollowsRestart(t *testing.T) {
	prov := &fakeDiscoverable{ep: core.Endpoint{BaseURL: "http://127.0.0.1:1111", Token: "a"}}
	e := &Entry{Name: "acme", Provider: prov, Endpoint: prov.ep}

	// Daemon restarts onto a new address: Rediscover adopts it.
	prov.ep = core.Endpoint{BaseURL: "http://127.0.0.1:2222", Token: "b"}
	if got := Rediscover(context.Background(), e); got.BaseURL != "http://127.0.0.1:2222" || got.Token != "b" {
		t.Fatalf("Rediscover did not follow restart: %+v", got)
	}

	// Daemon momentarily absent: keep the last-known endpoint, don't blank it.
	prov.ep = core.Endpoint{}
	if got := Rediscover(context.Background(), e); got.BaseURL != "http://127.0.0.1:2222" {
		t.Fatalf("transient discovery miss must keep last-known, got %+v", got)
	}

	// Explicit endpoint is fixed: never rediscovered even if Discover differs.
	prov.ep = core.Endpoint{BaseURL: "http://127.0.0.1:3333", Token: "c"}
	ex := &Entry{Name: "acme", Provider: prov, Endpoint: core.Endpoint{BaseURL: "http://fixed:9000", Token: "x"}, Explicit: true}
	if got := Rediscover(context.Background(), ex); got.BaseURL != "http://fixed:9000" {
		t.Fatalf("explicit endpoint must not be rediscovered, got %+v", got)
	}
}

// A URL-declared adapter core (nothing installed, no manifest) must still be
// listed by /v1/cores: FromEnv registers a listing-only descriptor, and
// core.New keeps failing so the drive path stays on the explicit-URL branch.
func TestFromEnvRegistersAdapterDescriptorForURLDeclaredCore(t *testing.T) {
	clearCoreEnv(t)
	const name = "urldeclared"
	t.Cleanup(func() { _ = core.Unregister(name) })

	proto := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/core/v1/discover" {
			_, _ = w.Write([]byte(`{"name":"urldeclared","version":"t","protocol":{"min":"1.0","max":"1.0"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer proto.Close()
	t.Setenv("BNBLOC_CORES", name)
	t.Setenv("BNBLOC_CORE_URLDECLARED_URL", proto.URL)

	entries, err := FromEnv(nil)
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if len(entries) != 1 || !entries[0].Explicit {
		t.Fatalf("want one explicit entry, got %+v", entries)
	}
	if _, ok := core.Describe(name); !ok {
		t.Fatalf("adapter descriptor not registered; /v1/cores would dead-end the core")
	}
	if _, err := core.New(name); err == nil {
		t.Fatal("core.New must keep failing: the descriptor is not a provider")
	}
}
