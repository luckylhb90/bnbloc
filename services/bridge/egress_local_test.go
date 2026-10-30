package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/component"
	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// providerOnThisComputer announces a provider under dir and records the
// drives it was asked about.
func providerOnThisComputer(t *testing.T, dir string) (asked func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var turn component.ProviderTurn
		_ = component.DecodeHeader(r.Header.Get(component.TurnHeader), &turn)
		mu.Lock()
		got = append(got, r.URL.Path+" drive="+turn.Drive+" job="+turn.Job+" core="+turn.Core+" provider="+turn.Provider)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(component.EgressHosts{Hosts: []string{"api.model.example.com"}, Env: map[string]string{"MODEL_API_KEY": "placeholder-key-000", "HTTPS_PROXY": "x"}})
	}))
	t.Cleanup(srv.Close)
	if err := os.MkdirAll(filepath.Join(dir, "egress-providers", "acme"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(component.EgressProviderFile{Contract: component.EgressProviderContract, URL: srv.URL})
	if err := os.WriteFile(filepath.Join(dir, "egress-providers", "acme", component.EgressProviderFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), got...) }
}

// emptyGateway has no egress service: only a provider on this computer exists.
func emptyGateway(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/my/turn-services" {
			_ = json.NewEncoder(w).Encode(wire.TurnServiceList{Services: []wire.TurnService{}})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A provider on this computer is asked about the member's own run in the
// sandbox, and about no other (a host run, borrowed, platform, or a run
// wearing someone else's scripts): the claim is the member's alone.
func TestLocalProviderServesOnlyTheMembersOwnSandboxedRun(t *testing.T) {
	state := t.TempDir()
	asked := providerOnThisComputer(t, state)
	gw := emptyGateway(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	b := New(Config{GatewayBaseURL: gw.URL, GatewayToken: "t", OwnerSubject: "maya", StateDir: state,
		Sandbox: SandboxConfig{HostProxy: fmt.Sprintf("host.vm.internal:%d", port)}})
	stop, err := b.EnableRoomTools("/opt/bnbloc/bnbloc-bridge")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	c := b.sessionContributors()[0]
	own := SessionInfo{RoomID: "room-1", Owner: "maya", Core: "core-a", Provider: "codex", JobID: "job-1", RequesterUserID: "maya", Drive: component.DriveOwn, OwnRun: true}

	// An own run on the host: its agent holds its own credentials; no proxy,
	// no question to the provider.
	hostPart, err := c.ContributeSession(context.Background(), own)
	if err != nil || hostPart.Env != nil || len(asked()) != 0 {
		t.Fatalf("host run: env %v, asked %v, %v", hostPart.Env, asked(), err)
	}

	own.Sandboxed, own.Workdir = true, t.TempDir()
	part, err := c.ContributeSession(context.Background(), own)
	if err != nil {
		t.Fatal(err)
	}
	if got := asked(); len(got) != 1 || !strings.Contains(got[0], "/egress/hosts drive=own job=job-1 core=core-a provider=codex") {
		t.Fatalf("asked = %v", got)
	}
	for _, other := range []TurnInfo{
		{JobID: "job-2", RequesterUserID: "jack", Drive: component.DriveBorrowed},
		{JobID: "job-3", Drive: component.DrivePlatform},
		{JobID: "job-4", RequesterUserID: "maya", Drive: component.DriveOwn, OwnRun: false}, // wearing foreign scripts
	} {
		part.Turn(other)
	}
	if got := asked(); len(got) != 1 {
		t.Fatalf("a run that is not the member's own reached the provider: %v", got)
	}
	part.Turn(TurnInfo{JobID: "job-5", RequesterUserID: "maya", Drive: component.DriveOwn, OwnRun: true})
	if got := asked(); len(got) != 2 || !strings.Contains(got[1], "job=job-5") {
		t.Fatalf("asked = %v", got)
	}
}

// A sandboxed own run gets the proxy and the provider's placeholders in its
// space with no organization egress service at all; a borrowed run gets
// neither the proxy nor a placeholder.
func TestSandboxedOwnRunGetsPlaceholdersFromALocalProviderAlone(t *testing.T) {
	state := t.TempDir()
	asked := providerOnThisComputer(t, state)
	gw := emptyGateway(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	b := New(Config{GatewayBaseURL: gw.URL, GatewayToken: "t", OwnerSubject: "maya", StateDir: state,
		Sandbox: SandboxConfig{HostProxy: fmt.Sprintf("host.vm.internal:%d", port)}})
	stop, err := b.EnableRoomTools("/opt/bnbloc/bnbloc-bridge")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if b.sandbox == nil || b.sandbox.spaceEnv == nil {
		t.Fatal("the sandbox's space environment is not wired")
	}
	workdir := t.TempDir()
	own := &component.ProviderTurn{Turn: component.Turn{Room: "room-1", Job: "job-1", Drive: component.DriveOwn}, Core: "core-a", Provider: "hermes"}
	env := b.sandbox.spaceEnv(context.Background(), workdir, "room-1", "job-1", own)
	if env["MODEL_API_KEY"] != "placeholder-key-000" || !strings.Contains(env["HTTPS_PROXY"], "host.vm.internal:") || env["HTTPS_PROXY"] == "x" || env["NODE_EXTRA_CA_CERTS"] == "" {
		t.Fatalf("own space env = %v", env)
	}
	if got := asked(); len(got) != 1 || !strings.Contains(got[0], "provider=hermes") {
		t.Fatalf("asked = %v", got)
	}
	// Not the member's own run: no provider is asked and no placeholder is set.
	env = b.sandbox.spaceEnv(context.Background(), workdir, "room-1", "job-2", nil)
	if _, has := env["MODEL_API_KEY"]; has || len(asked()) != 1 {
		t.Fatalf("a run that is not own got %v (asked %v)", env, asked())
	}
}

// With no provider and no service, a session has no egress proxy at all.
func TestNoEgressWithoutAServiceOrAProvider(t *testing.T) {
	gw := emptyGateway(t)
	b := New(Config{GatewayBaseURL: gw.URL, GatewayToken: "t", OwnerSubject: "maya", StateDir: t.TempDir()})
	stop, err := b.EnableRoomTools("/opt/bnbloc/bnbloc-bridge")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	part, err := b.sessionContributors()[0].ContributeSession(context.Background(), SessionInfo{RoomID: "r", Owner: "maya", JobID: "j", RequesterUserID: "maya", Drive: component.DriveOwn, OwnRun: true})
	if err != nil || part.Env != nil {
		t.Fatalf("env = %v %v", part.Env, err)
	}
}

// The one rule that says whose run may use a credential on this computer.
func TestOwnRunIsTheMembersOwnAndNothingForeign(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    drive
		want bool
		wire string
	}{
		{"own on the host", drive{owner: "maya", job: bridgeJob{RequesterUserID: "maya"}}, true, component.DriveOwn},
		{"own in the sandbox, owner's environment", drive{owner: "maya", job: bridgeJob{RequesterUserID: "maya"}, prepareExec: nopExec, ownSandbox: true}, true, component.DriveOwn},
		{"own, forced into the sandbox by foreign scripts", drive{owner: "maya", job: bridgeJob{RequesterUserID: "maya"}, prepareExec: nopExec}, false, component.DriveOwn},
		{"borrowed", drive{owner: "maya", borrowed: true, job: bridgeJob{RequesterUserID: "jack"}}, false, component.DriveBorrowed},
		{"requester unknown", drive{owner: "maya", job: bridgeJob{}}, false, component.DriveBorrowed},
		{"platform agent", drive{owner: "maya", job: bridgeJob{RequesterUserID: "maya", TargetID: "shared-agent:core-a:codex"}}, false, component.DrivePlatform},
	} {
		if got := tc.d.ownRun(); got != tc.want || tc.d.drive() != tc.wire {
			t.Errorf("%s: ownRun=%v drive=%s, want %v %s", tc.name, got, tc.d.drive(), tc.want, tc.wire)
		}
		if (tc.d.ownTurn() != nil) != tc.want {
			t.Errorf("%s: ownTurn is not tied to ownRun", tc.name)
		}
	}
}

func nopExec(context.Context) (*corepkg.Execution, error) { return nil, nil }

// reportingCore is a core that says which optional capabilities it offers.
type reportingCore struct {
	*fakeCoreProvider
	caps map[corepkg.Capability]bool
}

func (c reportingCore) Supports(_ context.Context, _ corepkg.Endpoint, cap corepkg.Capability) bool {
	return c.caps[cap]
}

// A core that advertises egressProvider is a provider at its own address
// (with its endpoint's bearer), with no descriptor file; a descriptor of the
// same name wins; a core that does not advertise it, or whose address is not
// on this computer, is not asked.
func TestACoreThatAdvertisesEgressProviderIsAProvider(t *testing.T) {
	var mu sync.Mutex
	var saw []string
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		saw = append(saw, r.URL.Path+" auth="+r.Header.Get("Authorization"))
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(component.EgressHosts{Hosts: []string{"api.model.example.com"}})
	}))
	defer core.Close()
	gw := emptyGateway(t)
	b := New(Config{GatewayBaseURL: gw.URL, GatewayToken: "t", OwnerSubject: "maya", StateDir: t.TempDir()})
	b.cores = map[string]coreEntry{
		"core-a": {provider: reportingCore{&fakeCoreProvider{}, map[corepkg.Capability]bool{corepkg.CapabilityEgressProvider: true}}, endpoint: corepkg.Endpoint{BaseURL: core.URL, Token: "core-token"}},
		"core-b": {provider: reportingCore{&fakeCoreProvider{}, nil}, endpoint: corepkg.Endpoint{BaseURL: core.URL}},
		"core-c": {provider: reportingCore{&fakeCoreProvider{}, map[corepkg.Capability]bool{corepkg.CapabilityEgressProvider: true}}, endpoint: corepkg.Endpoint{BaseURL: "http://10.1.2.3:9"}},
	}
	ps := b.localProviders(context.Background())
	if len(ps) != 1 || ps[0].Service() != "local:core-a" {
		t.Fatalf("providers = %v", ps)
	}
	if _, err := ps[0].Hosts(context.Background(), component.ProviderTurn{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(saw) != 1 || saw[0] != "/egress/hosts auth=Bearer core-token" {
		t.Fatalf("the core saw %v", saw)
	}
}

// A provider's files go into the sandboxed workspace's .room/egress/providers/<name>/
// at each turn (replaced whole), invalid names and oversize sets are dropped, and
// `{files}` in an Env value is the folder's path in the guest.
func TestProviderFilesReachTheSandboxedWorkspace(t *testing.T) {
	state := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(component.EgressHosts{Hosts: []string{"api.model.example.com"},
			Env:   map[string]string{"APP_HOME": "{files}", "PLAIN": "x"},
			Files: map[string]string{"config.yaml": "model: x\n", "../escape": "no", ".hidden": "no", "a/b": "no"}})
	}))
	defer srv.Close()
	_ = os.MkdirAll(filepath.Join(state, "egress-providers", "acme"), 0o755)
	raw, _ := json.Marshal(component.EgressProviderFile{Contract: component.EgressProviderContract, URL: srv.URL})
	_ = os.WriteFile(filepath.Join(state, "egress-providers", "acme", component.EgressProviderFileName), raw, 0o600)
	gw := emptyGateway(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	b := New(Config{GatewayBaseURL: gw.URL, GatewayToken: "t", OwnerSubject: "maya", StateDir: state, Sandbox: SandboxConfig{HostProxy: fmt.Sprintf("host.vm.internal:%d", port)}})
	stop, err := b.EnableRoomTools("/opt/bnbloc/bnbloc-bridge")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	workdir := t.TempDir()
	own := &component.ProviderTurn{Turn: component.Turn{Room: "room-1", Job: "job-1", Drive: component.DriveOwn}, Provider: "hermes"}
	env := b.sandbox.spaceEnv(context.Background(), workdir, "room-1", "job-1", own)
	dir := filepath.Join(workdir, ".room", "egress", "providers", "acme")
	if env["APP_HOME"] != dir || env["PLAIN"] != "x" {
		t.Fatalf("env = %v, want APP_HOME=%s", env, dir)
	}
	got, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil || string(got) != "model: x\n" {
		t.Fatalf("config.yaml = %q %v", got, err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("invalid names were written: %v", ents)
	}
	// A stale file is gone on the next turn.
	_ = os.WriteFile(filepath.Join(dir, "stale"), []byte("x"), 0o644)
	b.sandbox.spaceEnv(context.Background(), workdir, "room-1", "job-2", own)
	if _, err := os.Stat(filepath.Join(dir, "stale")); err == nil {
		t.Fatal("a stale provider file stayed")
	}
	// An unchanged file is not replaced: the folder and its files stay (a file removed and
	// recreated on the host is missing in the guest for seconds, and the agent starts at once).
	before, err := os.Stat(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dirBefore, _ := os.Stat(dir)
	b.sandbox.spaceEnv(context.Background(), workdir, "room-1", "job-4", own)
	after, err := os.Stat(filepath.Join(dir, "config.yaml"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("an unchanged provider file was replaced: %v", err)
	}
	if dirAfter, _ := os.Stat(dir); !os.SameFile(dirBefore, dirAfter) {
		t.Fatal("the provider folder was removed and made again")
	}
	// A changed file is rewritten in place.
	_ = os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("tampered"), 0o644)
	b.sandbox.spaceEnv(context.Background(), workdir, "room-1", "job-5", own)
	if got, _ := os.ReadFile(filepath.Join(dir, "config.yaml")); string(got) != "model: x\n" {
		t.Fatalf("a changed provider file was not put back: %q", got)
	}
	// A run that is not the member's own gets no files.
	other := t.TempDir()
	b.sandbox.spaceEnv(context.Background(), other, "room-1", "job-3", nil)
	if _, err := os.Stat(filepath.Join(other, ".room", "egress", "providers")); err == nil {
		t.Fatal("a run that is not own got provider files")
	}
}
