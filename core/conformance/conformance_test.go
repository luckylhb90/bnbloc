package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/protocol"
)

type fakeProvider struct {
	name      string
	workspace string
	session   string
	targets   []core.Target
}

func (p *fakeProvider) Name() string { return p.name }

func (p *fakeProvider) CreateWorkspace(context.Context, core.Endpoint) (string, error) {
	return p.workspace, nil
}

func (p *fakeProvider) CreateSession(context.Context, core.Endpoint, core.SessionRequest) (core.SessionResult, error) {
	return core.SessionResult{SessionID: p.session, TurnID: "t-1", Phase: "running"}, nil
}

func (p *fakeProvider) SendInput(context.Context, core.Endpoint, string, string, string, core.SessionOptions) error {
	return nil
}

func (p *fakeProvider) ListTargets(context.Context, core.Endpoint) ([]core.Target, error) {
	return p.targets, nil
}

func (p *fakeProvider) Messages(context.Context, core.Endpoint, string, string, int64) ([]core.Message, int64, error) {
	return []core.Message{{Version: 1, Role: "assistant", Text: "ok", UserVisible: true, Completed: true}}, 1, nil
}

func (p *fakeProvider) PollTurn(context.Context, core.Endpoint, string, string) (core.TurnStatus, error) {
	return core.TurnStatus{Idle: true}, nil
}

func TestRunProvider_passes_whenRequiredProviderMethodsSucceed(t *testing.T) {
	// Given
	provider := &fakeProvider{name: "fake", workspace: "ws-1", session: "s-1", targets: []core.Target{{ID: "local:codex", Provider: "codex"}}}

	// When / Then
	RunProvider(t, provider, Config{})
}

func TestRunLive_passes_whenProtocolDaemonConforms(t *testing.T) {
	// Given
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == protocol.PathDiscover:
			_ = json.NewEncoder(w).Encode(protocol.Discovery{Name: "live", Version: "1.0.0", Protocol: protocol.ProtocolRange{Min: "1.0", Max: "1.0"}})
		case r.Method == http.MethodPost && r.URL.Path == protocol.PathWorkspaces:
			_ = json.NewEncoder(w).Encode(protocol.CreateWorkspaceResponse{WorkspaceID: "ws-1"})
		case r.Method == http.MethodGet && r.URL.Path == protocol.PathTargets:
			_ = json.NewEncoder(w).Encode(protocol.ListTargetsResponse{Targets: []protocol.Target{{ID: "local:codex", Provider: "codex"}}})
		case r.Method == http.MethodPost && r.URL.Path == "/core/v1/workspaces/ws-1/sessions":
			_ = json.NewEncoder(w).Encode(protocol.CreateSessionResponse{SessionID: "s-1", TurnID: "t-1", Phase: "running"})
		case r.Method == http.MethodPost && r.URL.Path == "/core/v1/workspaces/ws-1/sessions/s-1/input":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/core/v1/workspaces/ws-1/sessions/s-1/messages":
			_ = json.NewEncoder(w).Encode(protocol.MessagesResponse{Messages: []protocol.Message{{Version: 1, Role: "assistant", Text: "ok", UserVisible: true, Completed: true}}, LatestVersion: 1})
		case r.Method == http.MethodGet && r.URL.Path == "/core/v1/workspaces/ws-1/sessions/s-1/turn":
			_ = json.NewEncoder(w).Encode(protocol.PollTurnResponse{Idle: true})
		case r.Method == http.MethodGet && r.URL.Path == "/core/v1/workspaces/__missing_workspace__/sessions/__missing_session__/turn":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(protocol.ErrorEnvelope{Error: protocol.ProtocolError{Code: protocol.ErrorSessionNotFound, Message: "missing session", Retryable: false}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// When / Then
	RunLive(t, Config{Endpoint: core.Endpoint{BaseURL: srv.URL}, HTTPClient: srv.Client(), TargetID: "local:codex"})
}

// sessionFilesDaemon is a conforming daemon that advertises the given
// capabilities and serves the session-file routes over a fixed workspace.
func sessionFilesDaemon(t *testing.T, caps []string, readBody []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == protocol.PathDiscover:
			_ = json.NewEncoder(w).Encode(protocol.Discovery{Name: "live", Version: "1.0.0", Protocol: protocol.ProtocolRange{Min: "1.0", Max: "1.0"}, Capabilities: caps})
		case r.URL.Path == protocol.PathWorkspaces:
			_ = json.NewEncoder(w).Encode(protocol.CreateWorkspaceResponse{WorkspaceID: "ws-1"})
		case r.URL.Path == protocol.PathTargets:
			_ = json.NewEncoder(w).Encode(protocol.ListTargetsResponse{Targets: []protocol.Target{{ID: "local:codex", Provider: "codex"}}})
		case r.URL.Path == "/core/v1/workspaces/ws-1/sessions":
			_ = json.NewEncoder(w).Encode(protocol.CreateSessionResponse{SessionID: "s-1"})
		case r.URL.Path == "/core/v1/workspaces/ws-1/sessions/s-1/files":
			_ = json.NewEncoder(w).Encode(protocol.ListSessionFilesResponse{Files: []protocol.SessionFile{{Path: "docs/report.md", SizeBytes: 5, ModTimeMs: 1}}})
		case r.URL.EscapedPath() == "/core/v1/workspaces/ws-1/files/docs%2Freport.md":
			_, _ = w.Write(readBody)
		case strings.HasPrefix(r.URL.Path, "/core/v1/workspaces/ws-1/files/"):
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(protocol.ErrorEnvelope{Error: protocol.ProtocolError{Code: protocol.ErrorFileNotFound, Message: "no such file"}})
		case strings.HasPrefix(r.URL.Path, "/core/v1/workspaces/ws-1/sessions/s-1/"):
			_, _ = w.Write([]byte(`{"idle":true,"messages":[],"latestVersion":0}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(protocol.ErrorEnvelope{Error: protocol.ProtocolError{Code: protocol.ErrorSessionNotFound, Message: "missing session"}})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckLive_sessionFiles_passes_whenListAndExactReadConform(t *testing.T) {
	srv := sessionFilesDaemon(t, []string{protocol.CapabilitySessionFiles, protocol.CapabilitySessionFileBytes}, []byte("hello"))
	if err := CheckLive(context.Background(), Config{Endpoint: core.Endpoint{BaseURL: srv.URL}, HTTPClient: srv.Client()}); err != nil {
		t.Fatalf("CheckLive = %v, want pass", err)
	}
}

func TestCheckLive_sessionFiles_fails_whenReadSizeDiffersFromListing(t *testing.T) {
	srv := sessionFilesDaemon(t, []string{protocol.CapabilitySessionFiles, protocol.CapabilitySessionFileBytes}, []byte("hello, truncated?"))
	err := CheckLive(context.Background(), Config{Endpoint: core.Endpoint{BaseURL: srv.URL}, HTTPClient: srv.Client()})
	if err == nil || !strings.Contains(err.Error(), "ReadSessionFile") {
		t.Fatalf("CheckLive = %v, want a ReadSessionFile size failure", err)
	}
}

func TestCheckLive_sessionFiles_fails_whenAdvertisedWithoutBytes(t *testing.T) {
	srv := sessionFilesDaemon(t, []string{protocol.CapabilitySessionFiles}, []byte("hello"))
	err := CheckLive(context.Background(), Config{Endpoint: core.Endpoint{BaseURL: srv.URL}, HTTPClient: srv.Client()})
	if err == nil || !strings.Contains(err.Error(), "both or neither") {
		t.Fatalf("CheckLive = %v, want the sessionFiles/sessionFileBytes pairing failure", err)
	}
}

func TestRunLive_againstLocalCoreProtocolDaemon(t *testing.T) {
	base := os.Getenv("BNBLOC_TEST_CORE_PROTOCOL_URL")
	if base == "" {
		t.Skip("BNBLOC_TEST_CORE_PROTOCOL_URL not set")
	}
	target := os.Getenv("BNBLOC_TEST_CORE_PROTOCOL_TARGET")
	if target == "" {
		target = "local:codex"
	}
	RunLive(t, Config{
		Endpoint: core.Endpoint{BaseURL: base},
		TargetID: target,
		Prompt:   "Core Protocol conformance: reply with the single word OK.",
		Timeout:  120 * time.Second,
	})
}

// writerProvider is a fakeProvider offering writeFile (and, optionally,
// agentProfile); enforce makes it apply core.WorkspaceFileAllowed.
type writerProvider struct {
	fakeProvider
	enforce  bool
	profile  bool
	profiles []*core.AgentProfile
}

func (p *writerProvider) Supports(_ context.Context, _ core.Endpoint, c core.Capability) bool {
	return c == core.CapabilityWriteFile || (p.profile && c == core.CapabilityAgentProfile)
}

func (p *writerProvider) WriteWorkspaceFile(_ context.Context, _ core.Endpoint, _, relPath, _ string) error {
	if p.enforce && !core.WorkspaceFileAllowed(relPath) {
		return errors.New("path not allowed")
	}
	return nil
}

func (p *writerProvider) CreateSession(ctx context.Context, ep core.Endpoint, req core.SessionRequest) (core.SessionResult, error) {
	p.profiles = append(p.profiles, req.Profile)
	return p.fakeProvider.CreateSession(ctx, ep, req)
}

func TestCheckProvider_writeFile_enforcesThePathContract(t *testing.T) {
	base := fakeProvider{name: "fake", workspace: "ws-1", session: "s-1", targets: []core.Target{{ID: "local:codex", Provider: "codex"}}}
	if err := CheckProvider(context.Background(), &writerProvider{fakeProvider: base, enforce: true}, Config{}); err != nil {
		t.Fatalf("enforcing writer = %v, want pass", err)
	}
	err := CheckProvider(context.Background(), &writerProvider{fakeProvider: base}, Config{})
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("permissive writer = %v, want the path contract failure", err)
	}
}

func TestCheckProvider_agentProfile_createsASessionWithAProfile(t *testing.T) {
	p := &writerProvider{fakeProvider: fakeProvider{name: "fake", workspace: "ws-1", session: "s-1"}, enforce: true, profile: true}
	if err := CheckProvider(context.Background(), p, Config{}); err != nil {
		t.Fatal(err)
	}
	var withProfile int
	for _, prof := range p.profiles {
		if prof != nil && prof.Instructions != "" && len(prof.Skills) == 1 {
			withProfile++
		}
	}
	if withProfile != 1 {
		t.Fatalf("profiles = %+v", p.profiles)
	}
}
