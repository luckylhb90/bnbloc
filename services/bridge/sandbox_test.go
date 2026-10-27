package bridge

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"github.com/luckylhb90/bnbloc/internal/wire"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	corepkg "github.com/luckylhb90/bnbloc/core"
	coreprotocol "github.com/luckylhb90/bnbloc/core/protocol"
	"github.com/luckylhb90/bnbloc/internal/store"
	sandbox "github.com/luckylhb90/bnbloc/sandbox/protocol"
)

const sandboxAdminToken = "admin-secret-bearer"

// fakeSandboxd is a Sandbox Protocol endpoint holding the desktop's machine
// "bnbloc" (and "bnbloc-own" when ownState is set): it records space
// ensures and mints numbered exec tokens.
type fakeSandboxd struct {
	srv     *httptest.Server
	mu      sync.Mutex
	ensures []sandbox.SpaceRequest
	spaces  []string
	minted  int
	ttls    []int64 // each mint's requested TTL (seconds)
	state   sandbox.State
	// ownState is the own machine's state ("" = no such machine).
	ownState sandbox.State
	// ensureMachines and mintMachines name the machine of each ensure and
	// each mint, in order.
	ensureMachines, mintMachines []string
	// calls is every space call in order: "ensure", "delete", "delete?purge".
	calls []string
	// deleteFails makes a space delete answer 500; deleteMissing answers
	// space_not_found (a space that does not exist has no process).
	deleteFails, deleteMissing bool
	// ensureFails makes a space ensure answer with this error (status,
	// code).
	ensureFails      *sandbox.ProtocolError
	ensureFailStatus int
	// listed are the spaces a list of the machine's spaces answers; listFails
	// makes the list answer 500.
	listed    []string
	listFails bool
	// refuseSharedPaths answers an ensure that mounts a host folder outside a
	// per-space tree (".spaces") with a conflict: another space holds its path.
	refuseSharedPaths bool
	// caps replaces the advertised capabilities when set.
	caps []string
}

// spaceCalls is the fake's space call log.
func (f *fakeSandboxd) spaceCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeSandboxd) advertised() []string {
	if f.caps != nil {
		return f.caps
	}
	return []string{sandbox.CapabilityExec, sandbox.CapabilitySpaces, sandbox.CapabilityShares, sandbox.CapabilityExecTokens}
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
		machine := "bnbloc"
		if strings.HasPrefix(p, sandbox.MachinePath("bnbloc-own")) && f.ownState != "" {
			machine = "bnbloc-own"
		}
		switch {
		case strings.HasPrefix(p, sandbox.MachinePath("bnbloc-own")) && f.ownState == "":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"machine_not_found","message":"no such machine"}}`))
		case p == sandbox.PathDiscover:
			_ = json.NewEncoder(w).Encode(sandbox.Discovery{Name: "fake", Version: "1", Contract: sandbox.ContractV1,
				Protocol:     sandbox.ProtocolRange{Min: "1.0", Max: "1.0"},
				Capabilities: f.advertised(),
				Backend:      sandbox.Backend{Name: "fake", Isolation: sandbox.IsolationMicroVM}})
		case p == sandbox.MachinePath(machine) && r.Method == http.MethodGet:
			state := f.state
			if machine == "bnbloc-own" {
				state = f.ownState
			}
			_ = json.NewEncoder(w).Encode(sandbox.Machine{ID: "m-" + machine, Name: machine, State: state})
		case strings.HasPrefix(p, sandbox.SpacesPath(machine)+"/") && strings.HasSuffix(p, "/exec-tokens"):
			f.minted++
			f.mintMachines = append(f.mintMachines, machine)
			var req sandbox.ExecTokenRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.ttls = append(f.ttls, req.TTLSeconds)
			_ = json.NewEncoder(w).Encode(sandbox.ExecToken{Token: "tok-" + string(rune('0'+f.minted)), ExpiresAtMs: 99})
		case strings.HasPrefix(p, sandbox.SpacesPath(machine)+"/") && r.Method == http.MethodDelete:
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
		case p == sandbox.SpacesPath(machine) && r.Method == http.MethodGet:
			if f.listFails {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"down"}}`))
				return
			}
			out := sandbox.ListSpacesResponse{}
			for _, n := range f.listed {
				out.Spaces = append(out.Spaces, sandbox.Space{Name: n})
			}
			_ = json.NewEncoder(w).Encode(out)
		case strings.HasPrefix(p, sandbox.SpacesPath(machine)+"/") && r.Method == http.MethodPut:
			var req sandbox.SpaceRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.calls = append(f.calls, "ensure")
			if f.refuseSharedPaths {
				for _, m := range req.Mounts[1:] {
					if !strings.Contains(m.HostPath, "/.spaces/") {
						w.WriteHeader(http.StatusConflict)
						_ = json.NewEncoder(w).Encode(sandbox.ErrorEnvelope{Error: sandbox.ProtocolError{Code: sandbox.ErrorConflict, Message: "guest path " + m.GuestPath + " collides with another space"}})
						return
					}
				}
			}
			if f.ensureFails != nil {
				w.WriteHeader(f.ensureFailStatus)
				_ = json.NewEncoder(w).Encode(sandbox.ErrorEnvelope{Error: *f.ensureFails})
				return
			}
			f.ensures = append(f.ensures, req)
			f.ensureMachines = append(f.ensureMachines, machine)
			f.spaces = append(f.spaces, strings.TrimPrefix(p, sandbox.SpacesPath(machine)+"/"))
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
	// deny makes the reservation authority refuse with this reason;
	// accessRequested makes it say it recorded an access request.
	deny            string
	accessRequested bool
	mu              sync.Mutex
	statuses        []map[string]any
	tenants         []string
	reserves        []map[string]any
	progress        []wire.JobProgressInput
	// leases counts lease renewals per job.
	leases map[string]int
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
			_ = json.NewEncoder(w).Encode(map[string]any{"tenantId": "t1", "requireSandboxForBorrow": g.require, "shareAccess": []string{"org"}})
		case strings.HasSuffix(r.URL.Path, "/authorization/reserve"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			g.mu.Lock()
			g.reserves = append(g.reserves, body)
			g.mu.Unlock()
			switch {
			case g.deny != "":
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]any{"reserved": false, "denyReason": g.deny, "accessRequested": g.accessRequested})
			case g.platform && body["sandboxed"] != true:
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"reserved":false,"denyReason":"sandbox_required","sandboxRequired":true}`))
			case g.platform:
				_ = json.NewEncoder(w).Encode(map[string]any{"reserved": true, "reservationId": "rsv", "sandboxRequired": true, "defaultModel": g.defaultModel})
			default:
				_, _ = w.Write([]byte(`{"reserved":true,"reservationId":"rsv"}`))
			}
		case strings.HasSuffix(r.URL.Path, "/lease"):
			g.mu.Lock()
			if g.leases == nil {
				g.leases = map[string]int{}
			}
			g.leases[strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/bridge/jobs/"), "/lease")]++
			g.mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/progress"):
			var body wire.JobProgressInput
			_ = json.NewDecoder(r.Body).Decode(&body)
			g.mu.Lock()
			g.progress = append(g.progress, body)
			g.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
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

func sandboxBridge(t *testing.T, gw *sandboxGateway, sbx *fakeSandboxd, mode SandboxMode, prov corepkg.Provider) (*Bridge, string) {
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

	// bob's turns take bob's seat: a space and workdir of their own.
	space := sandboxSpaceName(driveKey{room: "room-1", core: "core-a", provider: "codex", requester: "bob"}, "")
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
		{"core unsupported", SandboxBorrowed, true, false, "cannot run sandboxed"},
		// A sandbox the mode wants and that is not up yet is waited for
		// (TestSandbox_aDriveTheModeSandboxesWaitsForIt), not refused.
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
	b, _ := sandboxBridge(t, gw, nil, SandboxOff, prov)

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
	b, _ := sandboxBridge(t, gw, nil, SandboxOff, prov)
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
		requires                 bool
		mode                     SandboxMode
		ready, supports          bool
		policy                   func() (bool, error)
		wantSandboxed, wantRefus bool
	}{
		// An unreadable policy refuses an unsandboxed borrowed drive (fail
		// closed); a drive the sandbox takes never reads it.
		{"borrowed, mode off, policy unreadable", true, nil, false, SandboxOff, true, true, unreadable, false, true},
		{"borrowed, unsupported, policy unreadable", true, nil, false, SandboxBorrowed, true, false, unreadable, false, true},
		{"borrowed sandboxed, policy unreadable", true, nil, false, SandboxBorrowed, true, true, never, true, false},
		{"own, policy unreadable", false, nil, false, SandboxOff, true, true, never, false, false},
		{"borrowed sandboxed", true, nil, false, SandboxBorrowed, true, true, never, true, false},
		{"borrowed in mode all", true, nil, false, SandboxAll, true, true, never, true, false},
		{"own in mode borrowed", false, nil, false, SandboxBorrowed, true, true, never, false, false},
		{"own in mode all", false, nil, false, SandboxAll, true, true, never, true, false},
		{"own, sandbox down", false, nil, false, SandboxAll, false, true, never, false, false}, // waits
		{"borrowed, mode off, not required", true, nil, false, SandboxOff, true, true, off, false, false},
		{"borrowed, mode off, required", true, nil, false, SandboxOff, true, true, on, false, true},
		{"borrowed, not ready, required", true, nil, false, SandboxBorrowed, false, true, never, false, false},  // waits
		{"borrowed, not ready, mode all", true, nil, false, SandboxAll, false, true, never, false, false},       // waits
		{"own, not ready, mode borrowed", false, nil, false, SandboxBorrowed, false, true, never, false, false}, // the host, as the mode says
		{"own, sandbox down, unsupported", false, nil, false, SandboxAll, false, false, never, false, false},
		// The desktop does not know the mode yet: whatever might be
		// sandboxed waits.
		{"own, mode pending", false, nil, false, SandboxPending, false, true, never, false, false},
		{"borrowed, mode pending", true, nil, false, SandboxPending, false, true, never, false, false},
		{"borrowed, unsupported, not required", true, nil, false, SandboxBorrowed, true, false, off, false, false},
		// Someone else's script-bearing content forces the sandbox, own
		// drive or not, and is refused where the sandbox cannot take it.
		{"foreign content, own drive", false, []string{"lint kit"}, false, SandboxBorrowed, true, true, never, true, false},
		{"foreign content, own drive, mode all", false, []string{"lint kit"}, false, SandboxAll, true, true, never, true, false},
		{"foreign content, mode off", false, []string{"lint kit"}, false, SandboxOff, false, true, never, false, true},
		{"foreign content, unsupported", true, []string{"lint kit"}, false, SandboxBorrowed, true, false, never, false, true},
		{"foreign content, not ready", false, []string{"lint kit"}, false, SandboxAll, false, true, never, false, false}, // waits
		// A target that exists only in the sandbox: sandboxed, or refused, never
		// the host, in every mode. It is the owner's own run, so the owner's own
		// environment applies (unlike foreign content).
		{"sandbox-only, own, mode off", false, nil, true, SandboxOff, true, true, never, false, true},
		{"sandbox-only, own, mode borrowed", false, nil, true, SandboxBorrowed, true, true, never, true, false},
		{"sandbox-only, own, mode all", false, nil, true, SandboxAll, true, true, never, true, false},
		{"sandbox-only, core cannot sandbox", false, nil, true, SandboxAll, true, false, never, false, true},
		{"sandbox-only, sandbox not ready", false, nil, true, SandboxAll, false, true, never, false, false}, // waits
	}
	// The owner's forwarded environment goes to exactly one kind of drive:
	// their own, sandboxed, wearing nothing from someone else that runs
	// scripts. Never a borrowed one, never one forced by foreign content.
	ownerEnv := map[string]bool{"own in mode all": true, "sandbox-only, own, mode borrowed": true, "sandbox-only, own, mode all": true}
	// A drive the mode puts in the sandbox waits while it is not ready:
	// never the host meanwhile, never a refusal.
	wait := map[string]bool{"own, mode pending": true, "borrowed, mode pending": true, "own, sandbox down": true, "borrowed, not ready, required": true, "borrowed, not ready, mode all": true, "foreign content, not ready": true, "sandbox-only, sandbox not ready": true}
	for _, tc := range cases {
		d := decideSandbox(tc.borrowed, tc.foreign, tc.requires, tc.mode, tc.ready, "", tc.supports, tc.policy, "alice", "codex")
		if d.sandboxed != tc.wantSandboxed || (d.refuse != "") != tc.wantRefus || d.ownerEnv != ownerEnv[tc.name] || d.wait != wait[tc.name] {
			t.Errorf("%s: %+v", tc.name, d)
		}
	}
}

// The member's forwarded names reach their own sandboxed drive (the space
// allowlist and the descriptor), and a borrowed turn of the same agent
// right after converges back to the default list: never the owner's names.
func TestSandbox_ownDriveForwardsTheOwnersEnv_borrowedNever(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	sbx := newFakeSandboxd(t)
	prov := &sandboxProv{supports: true}
	b, _ := sandboxBridge(t, gw, sbx, SandboxAll, prov)
	owners := []string{"EXAMPLE_API_KEY", "EXAMPLE_*"}
	b.sandbox.cfg.Env = owners
	ctx := context.Background()

	b.runBridgeJob(ctx, job("job-1", "alice"))
	if prov.opts.Execution == nil || !slices.Equal(prov.opts.Execution.Sandbox.Env, owners) {
		t.Fatalf("own drive execution env = %+v; want the owner's names", prov.opts.Execution)
	}
	if got := sbx.ensures[0].EnvAllowlist; !slices.Equal(got, coreprotocol.ForwardEnvAllowlist(owners)) {
		t.Fatalf("own drive space allowlist = %v", got)
	}

	b.runBridgeJob(ctx, job("job-2", "bob"))
	// bob's turn is a session of bob's own seat, never an input to alice's.
	if len(prov.inputs) != 0 || prov.opts.Execution == nil || len(prov.opts.Execution.Sandbox.Env) != 0 {
		t.Fatalf("borrowed turn execution = %+v (inputs %d); want its own session, no forwarded names", prov.opts.Execution, len(prov.inputs))
	}
	if got := sbx.ensures[1].EnvAllowlist; !slices.Equal(got, coreprotocol.VMExecForwardEnv) {
		t.Fatalf("borrowed turn space allowlist = %v; want the default list only", got)
	}
	raw, _ := json.Marshal(prov.opts)
	if strings.Contains(string(raw), "EXAMPLE_") {
		t.Fatalf("the owner's names reached a borrowed turn: %s", raw)
	}
}

// An own drive runs on the member's own machine (the one that reaches the
// private hosts they listed) in a space and workspace of its own; a
// borrowed turn stays on the shared machine.
func TestSandbox_ownDriveRunsOnTheOwnMachine_borrowedOnTheShared(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	sbx := newFakeSandboxd(t)
	sbx.ownState = sandbox.StateRunning
	prov := &sandboxProv{supports: true}
	b, _ := sandboxBridge(t, gw, sbx, SandboxAll, prov)
	b.sandbox.cfg.OwnMachine = "bnbloc-own"
	ctx := context.Background()

	b.runBridgeJob(ctx, job("job-1", "alice"))
	if prov.opts.Execution == nil || prov.opts.Execution.Sandbox.Machine != "bnbloc-own" {
		t.Fatalf("own drive execution = %+v; want the own machine", prov.opts.Execution)
	}
	b.runBridgeJob(ctx, job("job-2", "bob"))
	if prov.opts.Execution == nil || prov.opts.Execution.Sandbox.Machine != "bnbloc" {
		t.Fatalf("borrowed turn execution = %+v; want the shared machine", prov.opts.Execution)
	}
	if !slices.Equal(sbx.ensureMachines, []string{"bnbloc-own", "bnbloc"}) || !slices.Equal(sbx.mintMachines, []string{"bnbloc-own", "bnbloc"}) {
		t.Fatalf("ensures on %v, mints on %v", sbx.ensureMachines, sbx.mintMachines)
	}
	if len(prov.workdirs) != 2 || prov.workdirs[0] == prov.workdirs[1] {
		t.Fatalf("workdirs = %v; want one per machine", prov.workdirs)
	}
}

// The own machine missing (the desktop recreating it for a change of
// hosts) is a sandbox not up yet: retried, never run on the shared machine,
// whose session is another one.
func TestSandbox_ownMachineMissingIsSandboxStarting_neverTheShared(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	sbx := newFakeSandboxd(t)
	prov := &sandboxProv{supports: true}
	b, _ := sandboxBridge(t, gw, sbx, SandboxAll, prov)
	b.sandbox.cfg.OwnMachine = "bnbloc-own" // handed over, but not there now

	b.runBridgeJob(context.Background(), job("job-1", "alice"))
	if len(sbx.ensureMachines) != 0 || prov.turns != 0 {
		t.Fatalf("ensures on %v, %d turns; want none", sbx.ensureMachines, prov.turns)
	}
	if st := gw.lastStatus(); st["status"] != "failed" || st["failureReason"] != string(store.FailureSandboxStarting) {
		t.Fatalf("status = %v", st)
	}
}

// failingSandboxProv is a sandboxExec core whose every turn fails: busy
// once, then idle and failed with the agent's message (default: a missing
// key).
type failingSandboxProv struct {
	sandboxProv
	message string
}

func (p *failingSandboxProv) PollTurn(ctx context.Context, ep corepkg.Endpoint, ws, s string) (corepkg.TurnStatus, error) {
	ts, err := p.sandboxProv.PollTurn(ctx, ep, ws, s)
	if ts.Idle {
		ts.Failed, ts.FailMessage = true, cmp.Or(p.message, "Missing environment variable: `EXAMPLE_API_KEY`")
	}
	return ts, err
}

// Cores declare no environment their agent needs, so an own sandboxed run
// whose agent failed says where the owner lets variables into the sandbox;
// a borrowed run's notice never points the borrower at the owner's setting.
func TestSandbox_agentErrorHintsAtTheEnvSetting_onOwnRunsOnly(t *testing.T) {
	for _, tc := range []struct {
		requester string
		hint      bool
	}{{"alice", true}, {"bob", false}} {
		gw := newSandboxGateway(t, "room-1", false)
		prov := &failingSandboxProv{sandboxProv: sandboxProv{supports: true}}
		b, _ := sandboxBridge(t, gw, newFakeSandboxd(t), SandboxAll, prov)
		b.runBridgeJob(context.Background(), job("job-1", tc.requester))

		if st := gw.lastStatus(); st["status"] != "failed" || st["failureReason"] != "agent_error" {
			t.Fatalf("%s: status = %v", tc.requester, st)
		}
		_, fresh := gw.stored()
		if len(fresh) != 1 {
			t.Fatalf("%s: rows = %+v; want the one notice", tc.requester, fresh)
		}
		text := payloadText(fresh[0])
		if got := strings.Contains(text, sandboxEnvHint); got != tc.hint {
			t.Fatalf("%s: notice %q; hint = %v, want %v", tc.requester, text, got, tc.hint)
		}
		if !strings.Contains(payloadField(fresh[0], "detail"), "EXAMPLE_API_KEY") {
			t.Fatalf("%s: notice detail = %q", tc.requester, payloadField(fresh[0], "detail"))
		}
	}
}

// The environment hint is only for an agent that could not authenticate:
// any other failure of an own sandboxed run (a network error, a model the
// key may not use) never sends the owner to the sandbox settings.
func TestSandbox_envHintOnlyForCredentialErrors(t *testing.T) {
	for _, tc := range []struct {
		message string
		hint    bool
	}{
		{"API Error: 401 Unauthorized", true},
		{"Not logged in. Please run /login", true},
		{"API Error: Connection refused (ECONNREFUSED)", false},
		{"unexpected status 403 Forbidden: the model is not available for this key", false},
	} {
		gw := newSandboxGateway(t, "room-1", false)
		prov := &failingSandboxProv{sandboxProv: sandboxProv{supports: true}, message: tc.message}
		b, _ := sandboxBridge(t, gw, newFakeSandboxd(t), SandboxAll, prov)
		b.runBridgeJob(context.Background(), job("job-1", "alice"))
		_, fresh := gw.stored()
		if len(fresh) != 1 {
			t.Fatalf("%q: rows = %+v", tc.message, fresh)
		}
		if got := strings.Contains(payloadText(fresh[0]), sandboxEnvHint); got != tc.hint {
			t.Fatalf("%q: hint = %v, want %v", tc.message, got, tc.hint)
		}
	}
}

func TestParseSandboxEnv(t *testing.T) {
	env, refused := ParseSandboxEnv(" EXAMPLE_API_KEY, EXAMPLE_*\nEXAMPLE_API_KEY  BNBLOC_TOKEN,path,* ")
	if !slices.Equal(env, []string{"EXAMPLE_API_KEY", "EXAMPLE_*"}) || !slices.Equal(refused, []string{"BNBLOC_TOKEN", "path", "*"}) {
		t.Fatalf("ParseSandboxEnv = %v, refused %v", env, refused)
	}
	if env, refused := ParseSandboxEnv(""); env != nil || refused != nil {
		t.Fatalf("empty = %v, %v", env, refused)
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
	a := sandboxSpaceName(driveKey{room: "room-1", core: "core-a", provider: "codex"}, "")
	if a != sandboxSpaceName(driveKey{room: "room-1", core: "core-a", provider: "codex"}, "") || a == sandboxSpaceName(driveKey{room: "room-1", core: "core-a", provider: "claude"}, "") || a == sandboxSpaceName(driveKey{room: "room-2", core: "core-a", provider: "codex"}, "") {
		t.Fatalf("space names must be stable and distinct: %s", a)
	}
	// A borrower's seat is a space of its own; the owner's keeps its name.
	seat := sandboxSpaceName(driveKey{room: "room-1", core: "core-a", provider: "codex", requester: "bob"}, "")
	if seat == a || seat == sandboxSpaceName(driveKey{room: "room-1", core: "core-a", provider: "codex", requester: "carol"}, "") {
		t.Fatalf("seats share a space: %s", seat)
	}
	// The own machine's space is a space of its own.
	if own := sandboxSpaceName(driveKey{room: "room-1", core: "core-a", provider: "codex", ownMachine: true}, ""); own == a {
		t.Fatalf("the own machine shares the space name %s", own)
	}
	long := sandboxSpaceName(driveKey{room: "r", core: "A Very Long Core Name", provider: "acp:hermes/agent"}, "")
	if len("sp-"+long) > 32 || strings.ContainsAny(long, " :/_.") {
		t.Fatalf("unsafe space name %q", long)
	}
}

// Two accounts on one computer, with agents of one core and provider in one
// room, never share a space (or its workspace folder): the name carries the
// account the bridge serves (docs/prd/2026-10-22-multi-account.md §2.4).
func TestSandboxSpaceNameIsPerAccount(t *testing.T) {
	k := driveKey{room: "room-1", core: "core-a", provider: "codex"}
	maya := BridgeAccount("https://gw.example.com/", "maya")
	jack := BridgeAccount("https://gw.example.com", "jack")
	elsewhere := BridgeAccount("https://other.example.com", "maya")
	if maya != BridgeAccount("https://gw.example.com", " maya ") {
		t.Fatalf("one account has one name: %q", maya)
	}
	names := map[string]string{
		"none":      sandboxSpaceName(k, ""),
		"maya":      sandboxSpaceName(k, maya),
		"jack":      sandboxSpaceName(k, jack),
		"elsewhere": sandboxSpaceName(k, elsewhere),
	}
	seen := map[string]string{}
	for who, name := range names {
		if prev, dup := seen[name]; dup {
			t.Fatalf("%s and %s share the space %s", who, prev, name)
		}
		seen[name] = who
		if len("sp-"+name) > 32 {
			t.Fatalf("%s: %q is too long for a guest user", who, name)
		}
	}
	// A bridge that serves no known account keeps the names an agent had.
	if BridgeAccount("", "maya") != "" || BridgeAccount("https://gw.example.com", "") != "" {
		t.Fatal("an unknown account must be empty")
	}
	// Borrowed seats are per account too.
	bob := driveKey{room: "room-1", core: "core-a", provider: "codex", requester: "bob"}
	if sandboxSpaceName(bob, maya) == sandboxSpaceName(bob, jack) {
		t.Fatal("a seat of two accounts shares a space")
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
		{"core unsupported", SandboxBorrowed, true, false, "cannot run sandboxed"},
		// A sandbox the mode wants and that is not up yet is waited for
		// (TestSandbox_aDriveTheModeSandboxesWaitsForIt), not refused.
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

// A drive the member's mode puts in the sandbox waits for it while it is
// not up (the desktop has not handed it over yet, or its machine is
// starting): it fails sandbox_starting, which retries, and never runs on
// the host meanwhile (2026-10-21: an own agent in mode all ran on the host
// while the handoff waited, then in the sandbox without the
// tools it had installed there).
func TestSandbox_aDriveTheModeSandboxesWaitsForIt(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mode      SandboxMode
		requester string
	}{
		{"own drive, mode all", SandboxAll, "alice"},
		{"borrowed drive, mode borrowed", SandboxBorrowed, "bob"},
		{"own drive, mode not known yet", SandboxPending, "alice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw := newSandboxGateway(t, "room-1", false)
			prov := &sandboxProv{supports: true}
			b, _ := sandboxBridge(t, gw, nil, tc.mode, prov)
			b.runBridgeJob(context.Background(), job("job-1", tc.requester))
			if st := gw.lastStatus(); st["status"] != "failed" || st["failureReason"] != "sandbox_starting" {
				t.Fatalf("status = %v; want sandbox_starting (retried)", st)
			}
			if prov.turns != 0 || prov.hostWorkspace != 0 || len(prov.workdirs) != 0 || len(gw.reserves) != 0 {
				t.Fatalf("turns %d, host workspaces %d, sandbox workdirs %v, reserves %v; want nothing reserved or driven", prov.turns, prov.hostWorkspace, prov.workdirs, gw.reserves)
			}
		})
	}
}

// When an agent's commands run somewhere else than its last turn's in the
// channel (the member's mode changed), the room is told: a new session and
// workspace there, without what it had before. The next turn there says
// nothing; going back says it continues the session it had there.
func TestSandbox_aMoveToAnotherPlaceIsSaid(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	sbx := newFakeSandboxd(t)
	prov := &sandboxProv{supports: true}
	b, _ := sandboxBridge(t, gw, sbx, SandboxBorrowed, prov)
	ctx := context.Background()
	moved := func() int { return gw.noticesContaining("until now") }

	b.runBridgeJob(ctx, job("job-1", "alice")) // mode borrowed: the host
	if prov.hostWorkspace != 1 || moved() != 0 {
		t.Fatalf("host workspaces %d, moved notices %d; want the first turn on the host, unannounced", prov.hostWorkspace, moved())
	}
	b.sandbox.cfg.Mode = SandboxAll
	b.runBridgeJob(ctx, job("job-2", "alice"))
	if len(prov.workdirs) != 1 || gw.noticesContaining("now runs in alice's agent sandbox (until now on alice's computer, outside the agent sandbox), in a new session") != 1 {
		t.Fatalf("sandbox workdirs %v, notices %d; want the move said once", prov.workdirs, moved())
	}
	b.runBridgeJob(ctx, job("job-3", "alice"))
	if moved() != 1 {
		t.Fatalf("moved notices %d after a turn in the same place; want still 1", moved())
	}
	b.sandbox.cfg.Mode = SandboxBorrowed
	b.runBridgeJob(ctx, job("job-4", "alice"))
	if gw.noticesContaining("runs on alice's computer, outside the agent sandbox again (until now in alice's agent sandbox) and continues the session it had there") != 1 {
		t.Fatalf("moved notices %d; want the move back said, continuing the host session", moved())
	}
}

// An agent that exists only in its owner's sandbox (Target.RequiresSandbox) is
// never run on the host and never run for anyone else: a borrow and an own run
// the sandbox cannot take are refused before anything reaches the core.
func TestSandbox_aSandboxOnlyAgentNeverRunsOnTheHostOrForSomeoneElse(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mode      SandboxMode
		sbx       bool
		requester string
		want      string
		// reason is the job's failure: a refusal, or the wait for a sandbox
		// the mode wants (retried; never the host meanwhile).
		reason string
	}{
		{"borrowed, mode all", SandboxAll, true, "bob", "cannot be borrowed", "sandbox_required"},
		{"borrowed, mode off", SandboxOff, false, "bob", "cannot be borrowed", "sandbox_required"},
		{"own, sandbox mode off", SandboxOff, false, "alice", "installed only in its owner's agent sandbox", "sandbox_required"},
		{"own, mode all, the sandbox is not up yet", SandboxAll, false, "alice", "", "sandbox_starting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw := newSandboxGateway(t, "room-1", false)
			var sbx *fakeSandboxd
			if tc.sbx {
				sbx = newFakeSandboxd(t)
			}
			prov := &sandboxProv{supports: true}
			prov.targets = []corepkg.Target{{ID: "c", Provider: "codex", RequiresSandbox: true}}
			b, _ := sandboxBridge(t, gw, sbx, tc.mode, prov)
			b.cacheTargets("core-a", prov.targets)

			b.runBridgeJob(context.Background(), job("job-1", tc.requester))

			if prov.prompt != "" || prov.hostWorkspace != 0 || len(prov.workdirs) != 0 {
				t.Fatalf("a refused drive must never reach the core (prompt=%q host workspaces=%d workdirs=%v)", prov.prompt, prov.hostWorkspace, prov.workdirs)
			}
			st := gw.lastStatus()
			if st["status"] != "failed" || st["failureReason"] != tc.reason {
				t.Fatalf("status = %v, want failure %s", st, tc.reason)
			}
			if tc.want == "" {
				return
			}
			_, fresh := gw.stored()
			if len(fresh) != 1 || !strings.Contains(payloadText(fresh[0]), tc.want) {
				t.Fatalf("notice = %+v, want it to say %q", fresh, tc.want)
			}
		})
	}
}

// The same agent runs for its owner when the sandbox is there, in every mode
// that is not off: sandboxed in a space, with the owner's own environment.
func TestSandbox_aSandboxOnlyAgentRunsSandboxedForItsOwner(t *testing.T) {
	for _, mode := range []SandboxMode{SandboxBorrowed, SandboxAll} {
		t.Run(string(mode), func(t *testing.T) {
			gw := newSandboxGateway(t, "room-1", false)
			sbx := newFakeSandboxd(t)
			prov := &sandboxProv{supports: true}
			prov.targets = []corepkg.Target{{ID: "c", Provider: "codex", RequiresSandbox: true}}
			b, _ := sandboxBridge(t, gw, sbx, mode, prov)
			b.cacheTargets("core-a", prov.targets)
			owners := []string{"EXAMPLE_API_KEY"}
			b.sandbox.cfg.Env = owners

			b.runBridgeJob(context.Background(), job("job-1", "alice"))

			if prov.hostWorkspace != 0 || len(prov.workdirs) != 1 || prov.opts.Execution == nil {
				t.Fatalf("it must run sandboxed (host workspaces=%d workdirs=%v execution=%+v)", prov.hostWorkspace, prov.workdirs, prov.opts.Execution)
			}
			if !slices.Equal(prov.opts.Execution.Sandbox.Env, owners) {
				t.Errorf("an own run keeps the owner's forwarded environment: %v", prov.opts.Execution.Sandbox.Env)
			}
		})
	}
}

// A target the core does not mark is unaffected: an own run in mode off runs on
// the host as before.
func TestSandbox_anAgentNotMarkedSandboxOnlyIsUnchanged(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	prov := &sandboxProv{supports: true}
	prov.targets = []corepkg.Target{{ID: "c", Provider: "codex"}}
	b, _ := sandboxBridge(t, gw, nil, SandboxOff, prov)
	b.cacheTargets("core-a", prov.targets)

	b.runBridgeJob(context.Background(), job("job-1", "alice"))

	if prov.hostWorkspace != 1 || len(prov.workdirs) != 0 {
		t.Fatalf("host workspaces=%d workdirs=%v", prov.hostWorkspace, prov.workdirs)
	}
}
