package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/store"
	sandbox "github.com/luckylhb90/bnbloc/sandbox/protocol"
)

const sandboxAdminToken = "admin-secret-bearer"

// fakeSandboxd is a Sandbox Protocol endpoint holding the desktop's machine
// "bnbloc": it records space ensures and mints numbered exec tokens.
type fakeSandboxd struct {
	srv     *httptest.Server
	mu      sync.Mutex
	ensures []sandbox.SpaceRequest
	spaces  []string
	minted  int
	state   sandbox.State
	// calls is every space call in order: "ensure", "delete", "delete?purge".
	calls []string
	// deleteFails makes a space delete answer 500; deleteMissing answers
	// space_not_found (a space that does not exist has no process).
	deleteFails, deleteMissing bool
	// ensureFails makes a space ensure answer with this error (status,
	// code).
	ensureFails      *sandbox.ProtocolError
	ensureFailStatus int
}

// spaceCalls is the fake's space call log.
func (f *fakeSandboxd) spaceCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func newFakeSandboxd(t *testing.T) *fakeSandboxd {
	f := &fakeSandboxd{state: sandbox.StateRunning}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+sandboxAdminToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"bad bearer"}}`))
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		p := r.URL.Path
		switch {
		case p == sandbox.PathDiscover:
			_ = json.NewEncoder(w).Encode(sandbox.Discovery{Name: "fake", Version: "1", Contract: sandbox.ContractV1,
				Protocol:     sandbox.ProtocolRange{Min: "1.0", Max: "1.0"},
				Capabilities: []string{sandbox.CapabilityExec, sandbox.CapabilitySpaces, sandbox.CapabilityShares, sandbox.CapabilityExecTokens},
				Backend:      sandbox.Backend{Name: "fake", Isolation: sandbox.IsolationMicroVM}})
		case p == sandbox.MachinePath("bnbloc") && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(sandbox.Machine{ID: "m1", Name: "bnbloc", State: f.state})
		case strings.HasPrefix(p, sandbox.SpacesPath("bnbloc")+"/") && strings.HasSuffix(p, "/exec-tokens"):
			f.minted++
			_ = json.NewEncoder(w).Encode(sandbox.ExecToken{Token: "tok-" + string(rune('0'+f.minted)), ExpiresAtMs: 99})
		case strings.HasPrefix(p, sandbox.SpacesPath("bnbloc")+"/") && r.Method == http.MethodDelete:
			call := "delete"
			if r.URL.Query().Get("purge") == "1" {
				call += "?purge"
			}
			f.calls = append(f.calls, call)
			if f.deleteFails {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"down"}}`))
				return
			}
			if f.deleteMissing {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"code":"space_not_found","message":"no such space"}}`))
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(p, sandbox.SpacesPath("bnbloc")+"/") && r.Method == http.MethodPut:
			var req sandbox.SpaceRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.calls = append(f.calls, "ensure")
			if f.ensureFails != nil {
				w.WriteHeader(f.ensureFailStatus)
				_ = json.NewEncoder(w).Encode(sandbox.ErrorEnvelope{Error: *f.ensureFails})
				return
			}
			f.ensures = append(f.ensures, req)
			f.spaces = append(f.spaces, strings.TrimPrefix(p, sandbox.SpacesPath("bnbloc")+"/"))
			_ = json.NewEncoder(w).Encode(sandbox.Space{Name: "s", User: "sp-s", UID: 20001})
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"` + p + `"}}`))
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// sandboxProv is a core that (optionally) advertises sandboxExec and records
// where workspaces are created and what each turn carried.
type sandboxProv struct {
	jobProv
	supports      bool
	hostWorkspace int
	workdirs      []string
	inputs        []corepkg.SessionOptions
	turns         int
	busy          bool
	// createErr fails every session create (the turn's start).
	createErr error
}

func (p *sandboxProv) Supports(_ context.Context, _ corepkg.Endpoint, c corepkg.Capability) bool {
	return c == corepkg.CapabilitySandboxExec && p.supports
}

func (p *sandboxProv) CreateWorkspace(context.Context, corepkg.Endpoint) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hostWorkspace++
	return "host-ws", nil
}

func (p *sandboxProv) CreateWorkspaceAt(_ context.Context, _ corepkg.Endpoint, workdir string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.workdirs = append(p.workdirs, workdir)
	return "sbx-ws", nil
}

func (p *sandboxProv) CreateSession(ctx context.Context, ep corepkg.Endpoint, req corepkg.SessionRequest) (corepkg.SessionResult, error) {
	if p.createErr != nil {
		return corepkg.SessionResult{}, p.createErr
	}
	p.mu.Lock()
	p.turns++
	p.busy = true
	p.mu.Unlock()
	return p.jobProv.CreateSession(ctx, ep, req)
}

func (p *sandboxProv) SendInput(_ context.Context, _ corepkg.Endpoint, _, _, _ string, opts corepkg.SessionOptions) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inputs = append(p.inputs, opts)
	p.turns++
	p.busy = true
	return nil
}

// PollTurn reports each turn busy once, then idle; Messages answers one
// reply per turn, so every turn drains with a visible reply.
func (p *sandboxProv) PollTurn(context.Context, corepkg.Endpoint, string, string) (corepkg.TurnStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	busy := p.busy
	p.busy = false
	return corepkg.TurnStatus{Idle: !busy}, nil
}

func (p *sandboxProv) Messages(context.Context, corepkg.Endpoint, string, string, int64) ([]corepkg.Message, int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := int64(p.turns)
	return []corepkg.Message{{Version: n, Role: "assistant", Text: "done", UserVisible: true, Completed: true}}, n, nil
}

// sandboxGateway grants borrows, answers the tenant sandbox policy and
// records job status reports; room routes go to the real activity fake.
type sandboxGateway struct {
	*activityGateway
	srv     *httptest.Server
	require bool
	// policyDown makes the tenant policy unreadable (503).
	policyDown bool
	// platform makes the reservation authority answer as for a platform
	// agent's grant: sandboxRequired, and sandbox_required (nothing reserved)
	// for a drive the bridge does not run sandboxed.
	platform bool
	// defaultModel is the platform agent's default model the reservation
	// answers ("" = none).
	defaultModel string
	// deny makes the reservation authority refuse with this reason.
	deny     string
	mu       sync.Mutex
	statuses []map[string]any
	tenants  []string
	reserves []map[string]any
}

func newSandboxGateway(t *testing.T, room string, require bool) *sandboxGateway {
	g := &sandboxGateway{activityGateway: newActivityGateway(t, room), require: require}
	inner := g.activityGateway.srv.Config.Handler
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/my/tenant-policy":
			g.mu.Lock()
			g.tenants = append(g.tenants, r.URL.Query().Get("tenant"))
			g.mu.Unlock()
			if g.policyDown {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tenantId": "t1", "requireSandboxForBorrow": g.require, "anonymousLinksDisabled": false})
		case strings.HasSuffix(r.URL.Path, "/authorization/reserve"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			g.mu.Lock()
			g.reserves = append(g.reserves, body)
			g.mu.Unlock()
			switch {
			case g.deny != "":
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]any{"reserved": false, "denyReason": g.deny})
			case g.platform && body["sandboxed"] != true:
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"reserved":false,"denyReason":"sandbox_required","sandboxRequired":true}`))
			case g.platform:
				_ = json.NewEncoder(w).Encode(map[string]any{"reserved": true, "reservationId": "rsv", "sandboxRequired": true, "defaultModel": g.defaultModel})
			default:
				_, _ = w.Write([]byte(`{"reserved":true,"reservationId":"rsv"}`))
			}
		case strings.HasSuffix(r.URL.Path, "/status"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			g.mu.Lock()
			g.statuses = append(g.statuses, body)
			g.mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		case strings.HasPrefix(r.URL.Path, "/v1/rooms/"+room+"/"):
			inner.ServeHTTP(w, r)
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *sandboxGateway) lastStatus() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.statuses) == 0 {
		return nil
	}
	return g.statuses[len(g.statuses)-1]
}

func sandboxBridge(t *testing.T, gw *sandboxGateway, sbx *fakeSandboxd, mode SandboxMode, prov *sandboxProv) (*Bridge, string) {
	t.Helper()
	root := t.TempDir()
	cfg := SandboxConfig{Mode: mode}
	if sbx != nil {
		cfg.URL, cfg.Token, cfg.VMExec, cfg.Machine, cfg.WorkspacesRoot = sbx.srv.URL, sandboxAdminToken, "/apps/sandbox/bnbloc-vmexec", "bnbloc", root
	}
	b := New(Config{GatewayBaseURL: gw.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov), Sandbox: cfg})
	return b, root
}

func job(id, requester string) bridgeJob {
	return bridgeJob{JobID: id, TenantID: "t1", RoomID: "room-1", OwnerUserID: "alice", RequesterUserID: requester,
		Core: "core-a", Provider: "codex", MessageText: "build it", ChannelID: "ch"}
}

func TestSandbox_borrowedDriveRunsInTheSpace_withAFreshSpaceTokenPerTurn(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	sbx := newFakeSandboxd(t)
	prov := &sandboxProv{supports: true}
	prov.targets = []corepkg.Target{{ID: "c", Provider: "codex"}}
	b, root := sandboxBridge(t, gw, sbx, SandboxBorrowed, prov)
	ctx := context.Background()

	b.runBridgeJob(ctx, job("job-1", "bob"))
	b.runBridgeJob(ctx, job("job-2", "bob"))

	space := sandboxSpaceName("room-1", "core-a", "codex")
	workdir := filepath.Join(root, space)
	if fi, err := os.Stat(workdir); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("workspace dir %s: %v %v", workdir, fi, err)
	}
	// The workspace is created IN the sandbox workdir, once (the second turn
	// reuses the sandboxed session); no host workspace.
	if len(prov.workdirs) != 1 || prov.workdirs[0] != workdir || prov.hostWorkspace != 0 {
		t.Fatalf("workspaces: sandboxed %v, host %d", prov.workdirs, prov.hostWorkspace)
	}
	// The space is re-ensured every turn, mounting the workspace at the
	// identical guest path, and a new token is minted per turn.
	if len(sbx.ensures) != 2 || sbx.minted != 2 || sbx.spaces[0] != space {
		t.Fatalf("ensures=%d minted=%d spaces=%v", len(sbx.ensures), sbx.minted, sbx.spaces)
	}
	m := sbx.ensures[0].Mounts
	if len(m) != 1 || m[0].Kind != sandbox.MountShared || m[0].HostPath != workdir || m[0].GuestPath != workdir || sbx.ensures[0].Labels.Room != "room-1" {
		t.Fatalf("space request = %+v", sbx.ensures[0])
	}
	first := prov.opts.Execution
	if first == nil {
		t.Fatal("session create carried no execution")
	}
	s := first.Sandbox
	if s.ExecToken != "tok-1" || s.Space != space || s.Machine != "bnbloc" || s.URL != sbx.srv.URL || s.VMExec != "/apps/sandbox/bnbloc-vmexec" ||
		s.Workdir != workdir || s.HostConfine.Workspace != workdir || len(s.Mounts) != 1 || s.Mounts[0].Host != workdir || s.Mounts[0].Guest != workdir {
		t.Fatalf("execution = %+v", s)
	}
	if len(prov.inputs) != 1 || prov.inputs[0].Execution == nil || prov.inputs[0].Execution.Sandbox.ExecToken != "tok-2" {
		t.Fatalf("follow-up input = %+v", prov.inputs)
	}
	// The admin bearer never reaches the core.
	raw, _ := json.Marshal([]any{prov.opts, prov.inputs})
	if strings.Contains(string(raw), sandboxAdminToken) {
		t.Fatal("the sandbox admin token leaked into a core request")
	}
	if st := gw.lastStatus(); st["status"] != "replied" {
		t.Fatalf("last status = %v", st)
	}
	// Sandboxed borrowing needed no policy lookup.
	if len(gw.tenants) != 0 {
		t.Fatalf("policy fetched for a sandboxed drive: %v", gw.tenants)
	}

	// The owner's own drive (mode borrowed) runs on the host in its own
	// session and workspace — never inside the sandboxed one.
	b.runBridgeJob(ctx, job("job-3", "alice"))
	if prov.hostWorkspace != 1 || prov.opts.Execution != nil || len(sbx.ensures) != 2 {
		t.Fatalf("own drive: host workspaces %d, execution %+v, ensures %d", prov.hostWorkspace, prov.opts.Execution, len(sbx.ensures))
	}
}

func TestSandbox_modeAllSandboxesOwnDrives(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	sbx := newFakeSandboxd(t)
	prov := &sandboxProv{supports: true}
	b, _ := sandboxBridge(t, gw, sbx, SandboxAll, prov)
	b.runBridgeJob(context.Background(), job("job-1", "alice"))
	if prov.opts.Execution == nil || len(prov.workdirs) != 1 {
		t.Fatalf("own drive in mode all: execution %+v workdirs %v", prov.opts.Execution, prov.workdirs)
	}
}

func TestSandbox_requiredPolicyRefusesVisibly_whenTheSandboxCannotTakeTheDrive(t *testing.T) {
	cases := []struct {
		name     string
		mode     SandboxMode
		sbx      bool
		supports bool
		want     string
	}{
		{"mode off", SandboxOff, false, true, "agent sandbox is off"},
		{"not running", SandboxBorrowed, false, true, "not running"},
		{"core unsupported", SandboxBorrowed, true, false, "cannot run sandboxed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gw := newSandboxGateway(t, "room-1", true)
			var sbx *fakeSandboxd
			if tc.sbx {
				sbx = newFakeSandboxd(t)
			}
			prov := &sandboxProv{supports: tc.supports}
			b, _ := sandboxBridge(t, gw, sbx, tc.mode, prov)

			b.runBridgeJob(context.Background(), job("job-1", "bob"))

			if prov.prompt != "" || prov.hostWorkspace != 0 || len(prov.workdirs) != 0 {
				t.Fatal("a refused drive must never reach the core")
			}
			st := gw.lastStatus()
			if st["status"] != "failed" || st["failureReason"] != "sandbox_required" {
				t.Fatalf("status = %v", st)
			}
			_, fresh := gw.stored()
			if len(fresh) != 1 || !strings.Contains(payloadText(fresh[0]), tc.want) || !strings.Contains(payloadText(fresh[0]), "requires borrowed agents") {
				t.Fatalf("notice = %+v", fresh)
			}
			if len(gw.tenants) != 1 || gw.tenants[0] != "t1" {
				t.Fatalf("policy must be read for the job's tenant: %v", gw.tenants)
			}
		})
	}
}

// An unreadable tenant policy fails closed: a borrowed drive the sandbox
// cannot take is refused visibly, before anything is reserved or driven
// (docs/prd/2026-10-09-agent-vm-sandbox.md, decision O2 of the 2026-10-12
// remediation plan).
func TestSandbox_unreadablePolicyRefusesTheUnsandboxedBorrow(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	gw.policyDown = true
	prov := &sandboxProv{supports: true}
	b, _ := sandboxBridge(t, gw, nil, SandboxBorrowed, prov)

	b.runBridgeJob(context.Background(), job("job-1", "bob"))

	if prov.prompt != "" || prov.hostWorkspace != 0 {
		t.Fatal("a drive whose policy could not be read ran on the host")
	}
	if len(gw.reserves) != 0 {
		t.Fatalf("a refused drive reserved quota: %v", gw.reserves)
	}
	if st := gw.lastStatus(); st["status"] != "failed" || st["failureReason"] != "sandbox_required" {
		t.Fatalf("status = %v", st)
	}
	_, fresh := gw.stored()
	if len(fresh) != 1 || !strings.Contains(payloadText(fresh[0]), "could not be checked") {
		t.Fatalf("notice = %+v", fresh)
	}

	// The owner's own drive never reads the policy and still runs.
	b.runBridgeJob(context.Background(), job("job-2", "alice"))
	if prov.hostWorkspace != 1 || len(gw.tenants) != 1 {
		t.Fatalf("own drive: host workspaces %d, policy reads %v", prov.hostWorkspace, gw.tenants)
	}
}

func TestSandbox_borrowingWithoutTheSandboxStillWorks_whenNotRequired(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	prov := &sandboxProv{supports: true}
	b, _ := sandboxBridge(t, gw, nil, SandboxBorrowed, prov)
	b.runBridgeJob(context.Background(), job("job-1", "bob"))
	if prov.hostWorkspace != 1 || prov.opts.Execution != nil {
		t.Fatalf("borrow without a sandbox: host workspaces %d, execution %+v", prov.hostWorkspace, prov.opts.Execution)
	}
	if st := gw.lastStatus(); st["status"] != "replied" {
		t.Fatalf("status = %v", st)
	}
}

func TestSandbox_spaceFailureFailsTheDrive_neverFallsBackToTheHost(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	sbx := newFakeSandboxd(t)
	prov := &sandboxProv{supports: true}
	b, _ := sandboxBridge(t, gw, sbx, SandboxBorrowed, prov)
	b.sandbox.status(context.Background()) // ready, cached
	sbx.srv.Close()                        // the sidecar goes away mid-window

	b.runBridgeJob(context.Background(), job("job-1", "bob"))

	if prov.hostWorkspace != 0 || prov.prompt != "" {
		t.Fatal("a sandboxed drive whose space failed ran on the host")
	}
	// The sidecar not answering is a sandbox not up yet: retried, and said so.
	if st := gw.lastStatus(); st["status"] != "failed" || st["failureReason"] != string(store.FailureSandboxStarting) {
		t.Fatalf("status = %v", st)
	}
	if ready, _ := b.sandbox.status(context.Background()); ready {
		t.Fatal("a failed prepare must invalidate the readiness cache")
	}
}

// A desktop that restarted hands the bridge its sandbox while the machine is
// still booting: the space ensure is refused until it runs. That is
// sandbox_starting (retried), not a core that is unreachable and not a final
// failure; a refusal while the machine runs is a real failure.
func TestSandbox_machineStillStartingIsSandboxStarting(t *testing.T) {
	for _, c := range []struct {
		name   string
		state  sandbox.State
		status int
		code   sandbox.ErrorCode
		want   store.AgentJobFailureReason
	}{
		{"machine booting, ensure refused", sandbox.StateCreating, http.StatusConflict, sandbox.ErrorInvalidState, store.FailureSandboxStarting},
		{"backend not up", sandbox.StateRunning, http.StatusConflict, sandbox.ErrorBackendUnavailable, store.FailureSandboxStarting},
		{"machine re-created", sandbox.StateRunning, http.StatusNotFound, sandbox.ErrorMachineNotFound, store.FailureSandboxStarting},
		{"probe finds it stopped", sandbox.StateStopped, http.StatusBadRequest, sandbox.ErrorBadRequest, store.FailureSandboxStarting},
		{"running machine refuses the request", sandbox.StateRunning, http.StatusBadRequest, sandbox.ErrorBadRequest, store.FailureRunnerFailed},
	} {
		t.Run(c.name, func(t *testing.T) {
			gw := newSandboxGateway(t, "room-1", false)
			sbx := newFakeSandboxd(t)
			prov := &sandboxProv{supports: true}
			b, _ := sandboxBridge(t, gw, sbx, SandboxBorrowed, prov)
			b.sandbox.status(context.Background()) // ready, cached
			sbx.mu.Lock()
			sbx.state, sbx.ensureFailStatus, sbx.ensureFails = c.state, c.status, &sandbox.ProtocolError{Code: c.code, Message: "not now"}
			sbx.mu.Unlock()

			b.runBridgeJob(context.Background(), job("job-1", "bob"))

			if prov.hostWorkspace != 0 || prov.prompt != "" {
				t.Fatal("a sandboxed drive whose space failed ran")
			}
			if st := gw.lastStatus(); st["status"] != "failed" || st["failureReason"] != string(c.want) {
				t.Fatalf("status = %v, want failed/%s", st, c.want)
			}
		})
	}
}

func TestDecideSandbox(t *testing.T) {
	never := func() (bool, error) { t.Fatal("policy read when not needed"); return false, nil }
	off := func() (bool, error) { return false, nil }
	on := func() (bool, error) { return true, nil }
	unreadable := func() (bool, error) { return false, errors.New("gateway down") }
	cases := []struct {
		name                     string
		borrowed                 bool
		foreign                  []string
		mode                     SandboxMode
		ready, supports          bool
		policy                   func() (bool, error)
		wantSandboxed, wantRefus bool
	}{
		// An unreadable policy refuses an unsandboxed borrowed drive (fail
		// closed); a drive the sandbox takes never reads it.
		{"borrowed, mode off, policy unreadable", true, nil, SandboxOff, true, true, unreadable, false, true},
		{"borrowed, unsupported, policy unreadable", true, nil, SandboxBorrowed, true, false, unreadable, false, true},
		{"borrowed sandboxed, policy unreadable", true, nil, SandboxBorrowed, true, true, never, true, false},
		{"own, policy unreadable", false, nil, SandboxOff, true, true, never, false, false},
		{"borrowed sandboxed", true, nil, SandboxBorrowed, true, true, never, true, false},
		{"borrowed in mode all", true, nil, SandboxAll, true, true, never, true, false},
		{"own in mode borrowed", false, nil, SandboxBorrowed, true, true, never, false, false},
		{"own in mode all", false, nil, SandboxAll, true, true, never, true, false},
		{"own, sandbox down", false, nil, SandboxAll, false, true, never, false, false},
		{"borrowed, mode off, not required", true, nil, SandboxOff, true, true, off, false, false},
		{"borrowed, mode off, required", true, nil, SandboxOff, true, true, on, false, true},
		{"borrowed, not ready, required", true, nil, SandboxBorrowed, false, true, on, false, true},
		{"borrowed, unsupported, not required", true, nil, SandboxBorrowed, true, false, off, false, false},
		// Someone else's script-bearing content forces the sandbox, own
		// drive or not, and is refused where the sandbox cannot take it.
		{"foreign content, own drive", false, []string{"lint kit"}, SandboxBorrowed, true, true, never, true, false},
		{"foreign content, mode off", false, []string{"lint kit"}, SandboxOff, false, true, never, false, true},
		{"foreign content, unsupported", true, []string{"lint kit"}, SandboxBorrowed, true, false, never, false, true},
		{"foreign content, not ready", false, []string{"lint kit"}, SandboxAll, false, true, never, false, true},
	}
	for _, tc := range cases {
		d := decideSandbox(tc.borrowed, tc.foreign, tc.mode, tc.ready, "", tc.supports, tc.policy, "alice", "codex")
		if d.sandboxed != tc.wantSandboxed || (d.refuse != "") != tc.wantRefus {
			t.Errorf("%s: %+v", tc.name, d)
		}
	}
}

func TestParseSandboxMode(t *testing.T) {
	for in, want := range map[string]SandboxMode{"": SandboxBorrowed, "off": SandboxOff, " ALL ": SandboxAll, "borrowed": SandboxBorrowed} {
		if got, err := ParseSandboxMode(in); err != nil || got != want {
			t.Errorf("ParseSandboxMode(%q) = %q %v", in, got, err)
		}
	}
	if _, err := ParseSandboxMode("vm"); err == nil {
		t.Error("unknown mode accepted")
	}
}

func TestSandboxSpaceName(t *testing.T) {
	a := sandboxSpaceName("room-1", "core-a", "codex")
	if a != sandboxSpaceName("room-1", "core-a", "codex") || a == sandboxSpaceName("room-1", "core-a", "claude") || a == sandboxSpaceName("room-2", "core-a", "codex") {
		t.Fatalf("space names must be stable and distinct: %s", a)
	}
	long := sandboxSpaceName("r", "A Very Long Core Name", "acp:hermes/agent")
	if len("sp-"+long) > 32 || strings.ContainsAny(long, " :/_.") {
		t.Fatalf("unsafe space name %q", long)
	}
}

func TestSandboxConfig_neverLogsTheToken(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logger.Info("x", "sandbox", SandboxConfig{Mode: SandboxAll, URL: "http://127.0.0.1:1", Token: sandboxAdminToken, VMExec: "/v", Machine: "bnbloc", WorkspacesRoot: "/w"})
	if strings.Contains(buf.String(), sandboxAdminToken) || !strings.Contains(buf.String(), "present=true") {
		t.Fatalf("log = %s", buf.String())
	}
}

func TestSandbox_rosterAdvertisesSandboxedAgents(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	sbx := newFakeSandboxd(t)
	prov := &sandboxProv{supports: true}
	prov.targets = []corepkg.Target{{ID: "c", Provider: "codex"}, {ID: "k", Provider: "claude"}}
	b, _ := sandboxBridge(t, gw, sbx, SandboxBorrowed, prov)
	ctx := context.Background()
	b.refreshTargets(ctx)
	if got := b.rosterKey(ctx); got != "core-a/codex,core-a/claude|" {
		t.Fatalf("before the probe: %q (readiness unknown = not sandboxed)", got)
	}
	b.sandbox.status(ctx)
	if got := b.rosterKey(ctx); got != "core-a/codex,core-a/claude|core-a/codex,core-a/claude" {
		t.Fatalf("roster = %q", got)
	}
	off, _ := sandboxBridge(t, gw, sbx, SandboxOff, prov)
	off.refreshTargets(ctx)
	off.sandbox.status(ctx)
	if got := off.rosterKey(ctx); strings.Contains(got, "|core-a") {
		t.Fatalf("mode off advertised sandboxed agents: %q", got)
	}
}

// Platform agents always run in their host's agent sandbox: the reservation
// answers sandboxRequired, and a drive the host's bridge cannot sandbox is
// refused through the same path as the tenant policy — notice, job failure
// sandbox_required, nothing driven, nothing reserved.
func TestSandbox_platformAgentRefusedUnlessSandboxed(t *testing.T) {
	cases := []struct {
		name     string
		mode     SandboxMode
		sbx      bool
		supports bool
		want     string
	}{
		{"mode off", SandboxOff, false, true, "agent sandbox is off"},
		{"not running", SandboxBorrowed, false, true, "not running"},
		{"core unsupported", SandboxBorrowed, true, false, "cannot run sandboxed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gw := newSandboxGateway(t, "room-1", false)
			gw.platform = true
			var sbx *fakeSandboxd
			if tc.sbx {
				sbx = newFakeSandboxd(t)
			}
			prov := &sandboxProv{supports: tc.supports}
			b, _ := sandboxBridge(t, gw, sbx, tc.mode, prov)

			b.runBridgeJob(context.Background(), job("job-1", "bob"))

			if prov.prompt != "" || prov.hostWorkspace != 0 || len(prov.workdirs) != 0 {
				t.Fatal("a refused platform drive must never reach the core")
			}
			st := gw.lastStatus()
			if st["status"] != "failed" || st["failureReason"] != "sandbox_required" {
				t.Fatalf("status = %v", st)
			}
			_, fresh := gw.stored()
			if len(fresh) != 1 || !strings.Contains(payloadText(fresh[0]), tc.want) || !strings.Contains(payloadText(fresh[0]), "Platform agents always run") {
				t.Fatalf("notice = %+v", fresh)
			}
			if len(gw.reserves) != 1 || gw.reserves[0]["sandboxed"] != false {
				t.Fatalf("reserve bodies = %v; want one carrying sandboxed=false", gw.reserves)
			}
		})
	}
}

func TestSandbox_platformAgentRunsSandboxed(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	gw.platform = true
	sbx := newFakeSandboxd(t)
	prov := &sandboxProv{supports: true}
	b, _ := sandboxBridge(t, gw, sbx, SandboxBorrowed, prov)
	b.runBridgeJob(context.Background(), job("job-1", "bob"))
	if prov.opts.Execution == nil || len(prov.workdirs) != 1 || prov.hostWorkspace != 0 {
		t.Fatalf("platform drive: execution %+v workdirs %v host %d", prov.opts.Execution, prov.workdirs, prov.hostWorkspace)
	}
	if st := gw.lastStatus(); st["status"] != "replied" {
		t.Fatalf("status = %v", st)
	}
	if len(gw.reserves) != 1 || gw.reserves[0]["sandboxed"] != true {
		t.Fatalf("reserve bodies = %v", gw.reserves)
	}
}

// A platform agent's turn that named no model runs with the host's default
// model from the reservation; a member's explicit pick wins.
func TestSandbox_platformAgentDefaultModel(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	gw.platform, gw.defaultModel = true, "free-trial-1"
	sbx := newFakeSandboxd(t)
	prov := &sandboxProv{supports: true}
	b, _ := sandboxBridge(t, gw, sbx, SandboxBorrowed, prov)
	ctx := context.Background()

	b.runBridgeJob(ctx, job("job-1", "bob"))
	if prov.opts.Model != "free-trial-1" {
		t.Fatalf("session model = %q, want the host's default", prov.opts.Model)
	}
	picked := job("job-2", "bob")
	picked.Model = "member-pick"
	b.runBridgeJob(ctx, picked)
	if len(prov.inputs) != 1 || prov.inputs[0].Model != "member-pick" {
		t.Fatalf("follow-up inputs = %+v, want the member's model", prov.inputs)
	}
	if len(gw.reserves) != 2 || gw.reserves[0]["model"] != "" || gw.reserves[1]["model"] != "member-pick" {
		t.Fatalf("reserve bodies = %v", gw.reserves)
	}
}
