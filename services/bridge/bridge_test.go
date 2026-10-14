package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// isBorrow flags a chat whose author differs from the agent's owner (borrowing);
// a self-addressed or unattributed chat is not a borrow.
func TestIsBorrow(t *testing.T) {
	cases := []struct {
		name   string
		author string
		owner  string
		borrow bool
	}{
		{"other user borrows", "maya", "jack", true},
		{"self is not a borrow", "jack", "jack", false},
		{"a case variant is another user", "Jack", "jack", true},
		{"empty author is not a borrow", "", "jack", false},
		{"whitespace author is not a borrow", "   ", "jack", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isBorrow(c.author, c.owner); got != c.borrow {
				t.Fatalf("isBorrow(%q, %q) = %v, want %v", c.author, c.owner, got, c.borrow)
			}
		})
	}
}

// firstNonEmpty backs reply attribution: prefer the session's real owner, fall
// back to the bridge's configured owner.
func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "jack"); got != "jack" {
		t.Fatalf("firstNonEmpty(\"\", \"jack\") = %q, want jack", got)
	}
	if got := firstNonEmpty("jack", "maya"); got != "jack" {
		t.Fatalf("firstNonEmpty(\"jack\", \"maya\") = %q, want jack", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Fatalf("firstNonEmpty of empties = %q, want empty", got)
	}
}

type recordingProv struct {
	targets      []corepkg.Target
	lastTargetID string
}

func (f *recordingProv) Name() string { return "core-a" }
func (f *recordingProv) CreateWorkspace(context.Context, corepkg.Endpoint) (string, error) {
	return "w", nil
}
func (f *recordingProv) CreateSession(_ context.Context, _ corepkg.Endpoint, req corepkg.SessionRequest) (corepkg.SessionResult, error) {
	f.lastTargetID = req.TargetID
	return corepkg.SessionResult{SessionID: "s"}, nil
}
func (f *recordingProv) SendInput(context.Context, corepkg.Endpoint, string, string, string, corepkg.SessionOptions) error {
	return nil
}
func (f *recordingProv) ListTargets(context.Context, corepkg.Endpoint) ([]corepkg.Target, error) {
	return f.targets, nil
}
func (f *recordingProv) SessionIdle(context.Context, corepkg.Endpoint, string, string) (bool, error) {
	return true, nil
}
func (f *recordingProv) LatestSession(context.Context, corepkg.Endpoint, string) (string, error) {
	return "", nil
}
func (f *recordingProv) Messages(context.Context, corepkg.Endpoint, string, string, int64) ([]corepkg.Message, int64, error) {
	return nil, 0, nil
}
func (f *recordingProv) PollTurn(context.Context, corepkg.Endpoint, string, string) (corepkg.TurnStatus, error) {
	return corepkg.TurnStatus{Idle: true}, nil
}

type resolvingProv struct{ recordingProv }

func (f *resolvingProv) ResolveTarget(provider string) (string, bool) {
	return "native:" + provider, true
}

// workdirProv offers core.SessionWorkdirReporter from a fixed
// "workspace/session" -> dir table and records each lookup.
type workdirProv struct {
	recordingProv
	dirs    map[string]string
	lookups []string
}

func (f *workdirProv) SessionWorkdir(_ context.Context, _ corepkg.Endpoint, workspaceID, sessionID string) (string, error) {
	key := workspaceID + "/" + sessionID
	f.lookups = append(f.lookups, key)
	dir, ok := f.dirs[key]
	if !ok {
		return "", fmt.Errorf("unknown session %s", key)
	}
	return dir, nil
}

func newTestCore(prov corepkg.Provider) []CoreConfig {
	return []CoreConfig{{Name: "core-a", Provider: prov, Endpoint: corepkg.Endpoint{BaseURL: "http://local"}}}
}

func TestResolveTargetUsesProviderCapability(t *testing.T) {
	prov := &resolvingProv{}
	b := New(Config{
		GatewayBaseURL: "http://gw",
		OwnerSubject:   "a",
		Cores: []CoreConfig{{
			Name:     "native",
			Provider: prov,
			Endpoint: corepkg.Endpoint{BaseURL: "http://local"},
		}},
	})
	if got := b.resolveTarget(context.Background(), "native", "codex"); got != "native:codex" {
		t.Fatalf("resolveTarget = %q, want native:codex", got)
	}
}

func TestWorkspaceDirUsesSessionWorkdirCapability(t *testing.T) {
	prov := &workdirProv{dirs: map[string]string{"ws/s": " /tmp/project "}}
	b := New(Config{
		GatewayBaseURL: "http://gw",
		OwnerSubject:   "a",
		Cores: []CoreConfig{{
			Name:     "native",
			Provider: prov,
			Endpoint: corepkg.Endpoint{BaseURL: "http://local"},
		}},
	})
	got := b.workspaceDir(context.Background(), "native", "ws", "s")
	if got != "/tmp/project" {
		t.Fatalf("workspaceDir = %q, want /tmp/project", got)
	}
	if len(prov.lookups) != 1 || prov.lookups[0] != "ws/s" {
		t.Fatalf("workdir lookups = %v", prov.lookups)
	}
}

func TestWorkspaceDirSkipsCoreWithoutWorkdirCapability(t *testing.T) {
	b := New(Config{
		GatewayBaseURL: "http://gw",
		OwnerSubject:   "a",
		Cores: []CoreConfig{{
			Name:     "native",
			Provider: &recordingProv{},
			Endpoint: corepkg.Endpoint{BaseURL: "http://127.0.0.1:1"},
		}},
	})
	if got := b.workspaceDir(context.Background(), "native", "ws", "s"); got != "" {
		t.Fatalf("workspaceDir without workdir capability = %q, want empty", got)
	}
}

// advertiseParam must omit a "core/provider" the user turned off, so a disabled
// harness never appears in the composer roster the realtime service exposes.
func TestAdvertiseParamOmitsDisabled(t *testing.T) {
	prov := &recordingProv{targets: []corepkg.Target{{ID: "local:codex", Provider: "codex"}, {ID: "local:cursor", Provider: "cursor"}}}
	b := New(Config{
		GatewayBaseURL:    "http://gw",
		OwnerSubject:      "a",
		DisabledProviders: map[string]bool{"core-a/cursor": true},
		Cores:             newTestCore(prov),
	})
	b.refreshTargets(context.Background())
	got := b.advertiseParam()
	if got != "core-a/codex" {
		t.Fatalf("advertiseParam = %q, want core-a/codex (cursor disabled)", got)
	}
}

// New wires DisabledProviders into providerDisabled: a disabled "core/provider"
// key is off while others stay on.
func TestDisabledProviderFiltering(t *testing.T) {
	b := New(Config{
		GatewayBaseURL:    "http://gw",
		OwnerSubject:      "alice",
		DisabledProviders: map[string]bool{"core-a/cursor": true},
	})
	if !b.providerDisabled("core-a", "cursor") {
		t.Fatal("core-a/cursor should be disabled")
	}
	if b.providerDisabled("core-a", "codex") {
		t.Fatal("core-a/codex should be enabled")
	}
}

// Refusing a locally-disabled provider must be side-effect-free: a borrowed job
// (requester != owner) naming a disabled (core, provider) must resolve+refuse
// BEFORE the borrow gate, so it never checks authorization, records a borrow, or
// posts a borrow-denied message. Only the job's own status report and its
// refusal notice may leave the bridge. Regression guard: the disabled guard
// sat after the borrow block, so a disabled borrow still hit the gateway.
func TestDisabledRecipientNotDrivenBeforeBorrow(t *testing.T) {
	var borrowHits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/status") && !strings.HasSuffix(r.URL.Path, "/agent-activity/messages") {
			atomic.AddInt64(&borrowHits, 1)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	b := New(Config{
		GatewayBaseURL:    srv.URL,
		GatewayToken:      "t", // static token: avoid a login round-trip
		OwnerSubject:      "alice",
		ServeAllOwners:    true,
		DisabledProviders: map[string]bool{"core-a/cursor": true},
		Cores:             newTestCore(&recordingProv{}),
	})

	// Borrowed job (requester bob != owner alice) for the disabled core/provider.
	b.runBridgeJob(context.Background(), bridgeJob{
		JobID:           "job-1",
		RoomID:          "room1",
		RequesterUserID: "bob",
		OwnerUserID:     "alice",
		Core:            "core-a",
		Provider:        "cursor",
		MessageText:     "hi",
	})

	if n := atomic.LoadInt64(&borrowHits); n != 0 {
		t.Fatalf("disabled borrow hit the gateway %d time(s); refusal must be side-effect-free (no borrow authz/record/denied)", n)
	}
}

// cleanDriveError extracts the member-readable reason from a core REST error so
// a failed drive shows the actionable cause, not raw transport noise. The
// regression it guards: posting the whole "core METHOD path: status N: {json}"
// string (or a multi-line developerMessage) into the room feed.
func TestCleanDriveError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil error is empty", nil, ""},
		{
			"extracts developerMessage first line",
			errors.New(`create session: core-a POST /v1/workspaces/x/agent-sessions: status 502: {"error":{"code":"workspace_operation_failed","developerMessage":"agent runtime session is disconnected\nagent session is not connected","reason":"workspace_operation_failed"}}`),
			"agent runtime session is disconnected",
		},
		{
			"falls back to reason when developerMessage empty",
			errors.New(`x: status 500: {"error":{"code":"boom","reason":"provider_unavailable"}}`),
			"provider_unavailable",
		},
		{
			"non-JSON error truncates to raw text",
			errors.New("dial tcp 127.0.0.1:62501: connect: connection refused"),
			"dial tcp 127.0.0.1:62501: connect: connection refused",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cleanDriveError(c.err); got != c.want {
				t.Fatalf("cleanDriveError() = %q, want %q", got, c.want)
			}
		})
	}
}

// retryCore retries only what core.Retryable allows: a request that provably
// never reached the daemon (dial failure / 503) is retried a bounded number of
// times; an ambiguous failure of a non-idempotent call (a 502 after the daemon
// may already have started the turn) is NOT retried, so a member's prompt
// never runs twice (BR-13).
func TestRetryCore(t *testing.T) {
	unavailable := &corepkg.HTTPError{Core: "core-a", Method: "POST", Path: "/x", Status: http.StatusServiceUnavailable}
	calls := 0
	err := retryCore(context.Background(), false, func() error {
		calls++
		return unavailable
	})
	if calls != 3 || err == nil {
		t.Fatalf("503 calls = %d err = %v, want 3 (1 try + 2 retries) and the last error", calls, err)
	}

	calls = 0
	err = retryCore(context.Background(), false, func() error {
		calls++
		return &corepkg.HTTPError{Status: http.StatusBadGateway}
	})
	if calls != 1 || err == nil {
		t.Fatalf("ambiguous 502 on a non-idempotent call: calls = %d, want 1 (no retry)", calls)
	}

	calls = 0
	err = retryCore(context.Background(), false, func() error {
		calls++
		if calls == 1 {
			return &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		}
		return nil
	})
	if calls != 2 || err != nil {
		t.Fatalf("dial failure recovery: calls = %d err = %v, want 2 calls and nil", calls, err)
	}
}

// discoverProv is a fake whose Discover return can be swapped between calls, so
// a test can simulate a local daemon restarting onto a new port/token.
type discoverProv struct {
	recordingProv
	ep corepkg.Endpoint
}

func (f *discoverProv) Discover(context.Context) (corepkg.Endpoint, bool) {
	return f.ep, f.ep.BaseURL != ""
}

// refreshRoster must follow a discovery-capable core across a
// daemon restart: when Discover reports a new address/token, the bridge
// hot-swaps the core endpoint AND drops sessions bound to that core, whose
// in-memory runtime the restarted daemon no longer has. This guards the exact
// outage where a daemon restart changed its random port and every drive
// failed against the dead endpoint.
func TestRefreshRosterFollowsEndpointChange(t *testing.T) {
	prov := &discoverProv{ep: corepkg.Endpoint{BaseURL: "http://127.0.0.1:1111", Token: "tok-a"}}
	b := New(Config{
		GatewayBaseURL: "http://gw",
		OwnerSubject:   "maya",
		Cores:          []CoreConfig{{Name: "core-a", Provider: prov, Endpoint: prov.ep}},
	})
	// A live bound session on the discovery-capable core.
	key := sessionKey{driveKey: driveKey{room: "room1", core: "core-a", provider: "codex"}, channel: "ch"}
	b.sessions[key] = &roomSession{core: "core-a", workspaceID: "w", sessionID: "s"}

	// Same endpoint: refresh must NOT evict the session.
	b.refreshRoster(context.Background())
	if _, ok := b.sessions[key]; !ok {
		t.Fatal("unchanged endpoint must keep the bound session")
	}
	if ep := b.cores["core-a"].endpoint; ep.BaseURL != "http://127.0.0.1:1111" {
		t.Fatalf("endpoint = %q, want unchanged", ep.BaseURL)
	}

	// Daemon restarts onto a new port/token: refresh must hot-swap and evict.
	prov.ep = corepkg.Endpoint{BaseURL: "http://127.0.0.1:2222", Token: "tok-b"}
	b.refreshRoster(context.Background())
	if ep := b.cores["core-a"].endpoint; ep.BaseURL != "http://127.0.0.1:2222" || ep.Token != "tok-b" {
		t.Fatalf("endpoint = %+v, want hot-swapped to :2222/tok-b", ep)
	}
	if _, ok := b.sessions[key]; ok {
		t.Fatal("endpoint change must evict sessions bound to that core")
	}
}

func TestBridgeIDDefaultsToOwnerTenantAndHonorsExplicit(t *testing.T) {
	b := New(Config{GatewayBaseURL: "http://gw", OwnerSubject: "alice", Tenant: "tenant-a"})
	if got := b.bridgeID(); got != "bridge-alice-tenant-a" {
		t.Fatalf("bridgeID = %q, want bridge-alice-tenant-a", got)
	}
	explicit := New(Config{GatewayBaseURL: "http://gw", OwnerSubject: "alice", Tenant: "tenant-a", BridgeID: "desktop-42"})
	if got := explicit.bridgeID(); got != "desktop-42" {
		t.Fatalf("explicit bridgeID = %q, want desktop-42", got)
	}
}

func TestHeartbeatPostsCurrentAgentCapabilities(t *testing.T) {
	var got struct {
		BridgeID       string `json:"bridgeId"`
		OwnerUserID    string `json:"ownerUserId"`
		ServeAllOwners bool   `json:"serveAllOwners"`
		Agents         []struct {
			Core         string          `json:"core"`
			Provider     string          `json:"provider"`
			TargetID     string          `json:"targetId"`
			ModelOptions json.RawMessage `json:"modelOptions"`
			Disabled     bool            `json:"disabled"`
		} `json:"agents"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/bridge/heartbeat" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	prov := &recordingProv{targets: []corepkg.Target{{ID: "local:codex", Provider: "codex", Models: []string{"gpt-5.5"}, ReasoningEfforts: []string{"low", "high"}}, {ID: "local:cursor", Provider: "cursor"}}}
	b := New(Config{GatewayBaseURL: srv.URL, GatewayToken: "t", OwnerSubject: "alice", Tenant: "tenant-a", ServeAllOwners: true, DisabledProviders: map[string]bool{"core-a/cursor": true}, Cores: newTestCore(prov)})

	b.refreshTargets(context.Background())
	if err := b.reportHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.BridgeID != "bridge-alice-tenant-a" || got.OwnerUserID != "alice" || !got.ServeAllOwners {
		t.Fatalf("heartbeat identity = %+v", got)
	}
	if len(got.Agents) != 2 {
		t.Fatalf("heartbeat agents = %d, want 2", len(got.Agents))
	}
	if got.Agents[0].Core != "core-a" || got.Agents[0].Provider != "codex" || got.Agents[0].TargetID != "local:codex" || got.Agents[0].Disabled {
		t.Fatalf("codex agent = %+v", got.Agents[0])
	}
	if !strings.Contains(string(got.Agents[0].ModelOptions), "gpt-5.5") || !strings.Contains(string(got.Agents[0].ModelOptions), "high") {
		t.Fatalf("model options = %s", got.Agents[0].ModelOptions)
	}
	if got.Agents[1].Provider != "cursor" || !got.Agents[1].Disabled {
		t.Fatalf("disabled cursor agent = %+v", got.Agents[1])
	}
}

type jobProv struct {
	recordingProv
	mu       sync.Mutex
	prompt   string
	opts     corepkg.SessionOptions
	messages []corepkg.Message
	polls    int
	// pollsBusy is how many turn polls report busy before idle (default 1).
	pollsBusy int
}

func (p *jobProv) CreateSession(_ context.Context, _ corepkg.Endpoint, req corepkg.SessionRequest) (corepkg.SessionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prompt = req.Prompt
	p.opts = req.Options
	return corepkg.SessionResult{SessionID: "s"}, nil
}

func (p *jobProv) Messages(context.Context, corepkg.Endpoint, string, string, int64) ([]corepkg.Message, int64, error) {
	return p.messages, 1, nil
}

func (p *jobProv) PollTurn(context.Context, corepkg.Endpoint, string, string) (corepkg.TurnStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.polls++
	if p.polls <= max(1, p.pollsBusy) {
		return corepkg.TurnStatus{Idle: false}, nil
	}
	return corepkg.TurnStatus{Idle: true}, nil
}

// statusRecorder collects a job's status reports. Jobs now execute on their own
// goroutine, so the test observes them through a lock and waits for the terminal
// report instead of assuming claimOnce finished the drive.
type statusRecorder struct {
	mu       sync.Mutex
	statuses []string
	done     chan struct{}
}

func newStatusRecorder() *statusRecorder {
	return &statusRecorder{done: make(chan struct{})}
}

func (s *statusRecorder) add(status, reason string) {
	entry := status
	if reason != "" {
		entry += ":" + reason
	}
	s.mu.Lock()
	s.statuses = append(s.statuses, entry)
	s.mu.Unlock()
	switch status {
	case "replied", "failed", "timed_out", "cancelled":
		close(s.done)
	}
}

// wait blocks until a terminal status arrives and returns the reports in order.
func (s *statusRecorder) wait(t *testing.T) string {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Fatal("job never reported a terminal status")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.statuses, ",")
}

func TestClaimOnceDrivesJobPayloadAndReportsStatuses(t *testing.T) {
	rec := newStatusRecorder()
	var postedReply atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/bridge/jobs/claim":
			_, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jobs":[{"jobId":"job-1","roomId":"room-1","channelId":"chan-1","requesterUserId":"alice","ownerUserId":"alice","core":"core-a","provider":"codex","targetId":"local:codex","messageText":"ship it","model":"gpt-5.5","reasoningEffort":"high"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/bridge/jobs/job-1/status":
			var body struct {
				Status string `json:"status"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			rec.add(body.Status, "")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/rooms/room-1/channels":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"channels":[]}`)) // empty: History falls back to the default channel
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/rooms/room-1/history"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/users/profiles":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"profiles":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/rooms/room-1/agent-activity/session-state":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/content-components/effective":
			// No expert or skill packs in effect for this job.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"skillPacks":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/rooms/room-1/agent-activity/messages":
			postedReply.Store(true)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	prov := &jobProv{recordingProv: recordingProv{targets: []corepkg.Target{{ID: "local:codex", Provider: "codex"}}}, messages: []corepkg.Message{{Version: 1, Role: "assistant", Text: "done", UserVisible: true, Completed: true, MessageID: "m1", Seq: 1, CreatedAtMs: 10}}}
	b := New(Config{GatewayBaseURL: srv.URL, GatewayToken: "t", OwnerSubject: "alice", Tenant: "tenant-a", Cores: newTestCore(prov)})

	if !b.claimOnce(context.Background()) {
		t.Fatal("claimOnce returned false, want one claimed job")
	}
	if got := rec.wait(t); got != "running,reflowing,replied" {
		t.Fatalf("statuses = %s, want running,reflowing,replied", got)
	}
	if !strings.Contains(prov.prompt, "ship it") {
		t.Fatalf("prompt %q does not contain job text", prov.prompt)
	}
	if prov.opts.Model != "gpt-5.5" || prov.opts.ReasoningEffort != "high" {
		t.Fatalf("options = %+v, want model/reasoning from job", prov.opts)
	}
	if !postedReply.Load() {
		t.Fatal("assistant reply was not posted")
	}
}

func TestClaimOnceFailsJobWithMissingPromptWithoutDriving(t *testing.T) {
	rec := newStatusRecorder()
	var createCalls int64
	var notice atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/rooms/room-1/agent-activity/messages":
			var in wire.ReportMessagesInput
			_ = json.NewDecoder(r.Body).Decode(&in)
			if p, ok := in.Messages[0].Payload.(map[string]any); ok {
				notice.Store(p["notice"])
			}
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/bridge/jobs/claim":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jobs":[{"jobId":"job-empty","roomId":"room-1","requesterUserId":"alice","ownerUserId":"alice","core":"core-a","provider":"codex","messageText":"   "}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/bridge/jobs/job-empty/status":
			var body struct {
				Status        string `json:"status"`
				FailureReason string `json:"failureReason"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			rec.add(body.Status, body.FailureReason)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	prov := &noDriveProv{recordingProv: recordingProv{targets: []corepkg.Target{{ID: "local:codex", Provider: "codex"}}}, createCalls: &createCalls}
	b := New(Config{GatewayBaseURL: srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov)})

	if !b.claimOnce(context.Background()) {
		t.Fatal("claimOnce returned false, want one claimed job")
	}
	if got := rec.wait(t); got != "failed:invalid_job" {
		t.Fatalf("statuses = %s, want failed:invalid_job", got)
	}
	if got := notice.Load(); got != "invalid-job" {
		t.Fatalf("notice = %v, want the invalid-job notice (every refusal says why)", got)
	}
	if atomic.LoadInt64(&createCalls) != 0 {
		t.Fatal("job with empty messageText drove the local core")
	}
}

func TestConsumeRoomDoesNotDriveChatMessages(t *testing.T) {
	var createCalls int64
	prov := &noDriveProv{recordingProv: recordingProv{targets: []corepkg.Target{{ID: "local:codex", Provider: "codex"}}}, createCalls: &createCalls}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rooms/room1/events" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: chat_message\n"))
		_, _ = w.Write([]byte(`data: {"atUnixMs":9999999999999,"payload":{"userId":"alice","text":"run","recipient":{"ownerId":"alice","provider":"codex","core":"core-a"}}}` + "\n\n"))
	}))
	defer srv.Close()
	b := New(Config{GatewayBaseURL: srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov)})
	if err := b.consumeRoom(context.Background(), "room1"); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt64(&createCalls) != 0 {
		t.Fatal("consumeRoom drove a chat_message from SSE; durable jobs must be the only drive source")
	}
}

// interruptProv records whether the core was told to stop an in-flight turn.
type interruptProv struct {
	recordingProv
	interrupted chan string
}

func (p *interruptProv) Interrupt(_ context.Context, _ corepkg.Endpoint, _, sessionID string) error {
	select {
	case p.interrupted <- sessionID:
	default:
	}
	return nil
}

// A member pressing stop must halt the local turn as soon as the room stream
// reports it, not at the next lease renewal 20s later. Regression guard: the
// worker only learned it had lost a job when a renewal failed, so a cancelled
// agent kept burning the member's tokens until then.
func TestConsumeRoomAbortsCancelledJobImmediately(t *testing.T) {
	prov := &interruptProv{
		recordingProv: recordingProv{targets: []corepkg.Target{{ID: "local:codex", Provider: "codex"}}},
		interrupted:   make(chan string, 1),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: agent_job_updated\n"))
		_, _ = w.Write([]byte(`data: {"kind":"agent_job_updated","payload":{"jobId":"job-1","status":"cancelled"}}` + "\n\n"))
	}))
	defer srv.Close()
	b := New(Config{GatewayBaseURL: srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov)})

	// A job in flight, bound to a live core session.
	stopped := make(chan struct{})
	rj := &runningJob{stop: func() { close(stopped) }}
	rj.bind("core-a", &roomSession{workspaceID: "w", sessionID: "s-live"})
	b.trackJob("job-1", rj)

	if err := b.consumeRoom(context.Background(), "room1"); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-prov.interrupted:
		if got != "s-live" {
			t.Fatalf("interrupted session %q, want s-live", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled job did not interrupt the core turn")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled job did not stop its drive")
	}
}

// A job this bridge is not running must not be touched by another room's cancel.
func TestJobUpdateForUnknownJobIsIgnored(t *testing.T) {
	prov := &interruptProv{
		recordingProv: recordingProv{targets: []corepkg.Target{{ID: "local:codex", Provider: "codex"}}},
		interrupted:   make(chan string, 1),
	}
	b := New(Config{GatewayBaseURL: "http://gw", GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov)})
	b.handleJobUpdate(`{"kind":"agent_job_updated","payload":{"jobId":"someone-elses-job","status":"cancelled"}}`)
	select {
	case s := <-prov.interrupted:
		t.Fatalf("interrupted session %q for a job this bridge does not run", s)
	default:
	}
}

// A still-live transition must not stop the turn: only a job leaving this
// bridge's hands does.
func TestJobUpdateForRunningStatusDoesNotAbort(t *testing.T) {
	prov := &interruptProv{
		recordingProv: recordingProv{targets: []corepkg.Target{{ID: "local:codex", Provider: "codex"}}},
		interrupted:   make(chan string, 1),
	}
	b := New(Config{GatewayBaseURL: "http://gw", GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov)})
	rj := &runningJob{stop: func() { t.Error("running status stopped the drive") }}
	rj.bind("core-a", &roomSession{workspaceID: "w", sessionID: "s-live"})
	b.trackJob("job-1", rj)

	b.handleJobUpdate(`{"kind":"agent_job_updated","payload":{"jobId":"job-1","status":"running"}}`)
	select {
	case s := <-prov.interrupted:
		t.Fatalf("interrupted session %q on a running status", s)
	default:
	}
}

type noDriveProv struct {
	recordingProv
	createCalls *int64
}

func (p *noDriveProv) CreateWorkspace(context.Context, corepkg.Endpoint) (string, error) {
	atomic.AddInt64(p.createCalls, 1)
	return "w", nil
}
