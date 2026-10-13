package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/manifestcore"
	"github.com/luckylhb90/bnbloc/core/protocol"
)

// policyManifest is a manifest core with a writeWorkspaceFile op and no
// profile mapping, interrupt or sandboxExec.
const policyManifest = `{
  "name": "acme-policy",
  "createWorkspace": {"method":"POST","path":"/w"},
  "createSession": {"method":"POST","path":"/s","body":{"prompt":"{prompt}"}},
  "sendInput": {"method":"POST","path":"/i"},
  "listTargets": {"method":"GET","path":"/t"},
  "messages": {"method":"GET","path":"/m","items":{"list":"messages","fields":{"text":"text"}}},
  "pollTurn": {"method":"GET","path":"/turn"},
  "writeWorkspaceFile": {"method":"PUT","path":"/f","body":{"path":"{relPath}","content":"{content}"}}
}`

// countingDaemon answers discovery with caps and every other request with
// 200 {}, counting the non-discovery requests.
func countingDaemon(t *testing.T, caps ...string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == protocol.PathDiscover {
			_ = json.NewEncoder(w).Encode(protocol.Discovery{Name: "nimbus", Version: "1", Protocol: protocol.ProtocolRange{Min: "1.0", Max: "1.0"}, Capabilities: caps})
			return
		}
		n.Add(1)
		_, _ = w.Write([]byte(`{"sessionId":"s-1"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// writeFileDaemon is a conforming writeFile daemon, except that enforce=false
// accepts a write outside the bnbloc-owned paths.
func writeFileDaemon(t *testing.T, enforce bool) *httptest.Server {
	return cappedWriteFileDaemon(t, enforce, 0, 0)
}

// cappedWriteFileDaemon is writeFileDaemon whose server refuses a request
// body over maxBody (0 = none), stating limits.maxInputBytes = stated.
func cappedWriteFileDaemon(t *testing.T, enforce bool, maxBody, stated int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		switch {
		case path == protocol.PathDiscover:
			_ = json.NewEncoder(w).Encode(protocol.Discovery{Name: "nimbus", Version: "1", Protocol: protocol.ProtocolRange{Min: "1.0", Max: "1.0"}, Capabilities: []string{protocol.CapabilityWriteFile}, Limits: protocol.Limits{MaxInputBytes: stated}})
		case path == protocol.PathWorkspaces:
			_ = json.NewEncoder(w).Encode(protocol.CreateWorkspaceResponse{WorkspaceID: "ws-1"})
		case path == protocol.PathTargets:
			_ = json.NewEncoder(w).Encode(protocol.ListTargetsResponse{})
		case path == "/core/v1/workspaces/ws-1/sessions":
			_ = json.NewEncoder(w).Encode(protocol.CreateSessionResponse{SessionID: "s-1"})
		case r.Method == http.MethodPut && path == "/core/v1/workspaces/ws-1/files/conformance-outside.txt" && enforce:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(protocol.ErrorEnvelope{Error: protocol.ProtocolError{Code: protocol.ErrorBadRequest, Message: "path not allowed"}})
		case r.Method == http.MethodPut:
			n, _ := io.Copy(io.Discard, r.Body)
			if maxBody > 0 && n > maxBody {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(protocol.ErrorEnvelope{Error: protocol.ProtocolError{Code: protocol.ErrorBadRequest, Message: "invalid json body"}})
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case path == "/core/v1/workspaces/ws-1/sessions/s-1/messages", path == "/core/v1/workspaces/ws-1/sessions/s-1/turn", path == "/core/v1/workspaces/ws-1/sessions/s-1/input":
			_, _ = w.Write([]byte(`{"idle":true,"messages":[],"latestVersion":0}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(protocol.ErrorEnvelope{Error: protocol.ProtocolError{Code: protocol.ErrorSessionNotFound, Message: "missing session"}})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The protocol client refuses a disallowed path before the wire, so the
// live suite sends it raw: a daemon that accepts it fails conformance.
func TestCheckLive_writeFile_daemonRefusesOutsidePaths(t *testing.T) {
	good := writeFileDaemon(t, true)
	if err := CheckLive(context.Background(), Config{Endpoint: core.Endpoint{BaseURL: good.URL}, HTTPClient: good.Client()}); err != nil {
		t.Fatalf("enforcing daemon = %v, want pass", err)
	}
	bad := writeFileDaemon(t, false)
	if err := CheckLive(context.Background(), Config{Endpoint: core.Endpoint{BaseURL: bad.URL}, HTTPClient: bad.Client()}); err == nil {
		t.Fatal("a daemon accepting a write outside the owned paths passed conformance")
	}
}

// A writeFile daemon takes a file at the chat attachment cap, or states the
// smaller limit it holds to: one that silently caps its request bodies
// fails conformance (docs/bugs/2026-10-13-attachments-dropped-silently.md).
func TestCheckLive_writeFile_takesAnAttachmentAtTheCap(t *testing.T) {
	const capped = 2 << 20
	silent := cappedWriteFileDaemon(t, true, capped, 0)
	if err := CheckLive(context.Background(), Config{Endpoint: core.Endpoint{BaseURL: silent.URL}, HTTPClient: silent.Client()}); err == nil || !strings.Contains(err.Error(), "limits.maxInputBytes") {
		t.Fatalf("a daemon capping bodies at %d without saying so = %v, want a failure", capped, err)
	}
	stated := cappedWriteFileDaemon(t, true, capped, capped)
	if err := CheckLive(context.Background(), Config{Endpoint: core.Endpoint{BaseURL: stated.URL}, HTTPClient: stated.Client()}); err != nil {
		t.Fatalf("a daemon stating its limit = %v, want pass", err)
	}
}

type workspaceWriter interface {
	core.WorkspaceFileWriter
	core.WorkspaceFileBytesWriter
}

// Every workspace file writer refuses a path outside
// core.WorkspaceFileAllowed locally, before any HTTP call, and still writes
// a bnbloc-owned path.
func TestWorkspaceWriterEnforcesPolicy(t *testing.T) {
	ctx := context.Background()
	manifestSrv, manifestCalls := countingDaemon(t)
	mp, err := manifestcore.Parse([]byte(policyManifest))
	if err != nil {
		t.Fatal(err)
	}
	protoSrv, protoCalls := countingDaemon(t, protocol.CapabilityWriteFile)
	writers := []struct {
		name   string
		w      workspaceWriter
		ep     core.Endpoint
		served *atomic.Int64
	}{
		{"manifest", mp, core.Endpoint{BaseURL: manifestSrv.URL}, manifestCalls},
		{"protocol", protocol.NewClient("nimbus", protoSrv.Client()), core.Endpoint{BaseURL: protoSrv.URL}, protoCalls},
	}
	for _, tc := range writers {
		t.Run(tc.name, func(t *testing.T) {
			for _, rel := range []string{"outside.txt", "../.room/x.md", ".claude/settings.json", "/abs/.room/x"} {
				if err := tc.w.WriteWorkspaceFile(ctx, tc.ep, "ws-1", rel, "x"); !errors.Is(err, core.ErrWorkspacePathNotAllowed) {
					t.Fatalf("WriteWorkspaceFile(%q) = %v, want ErrWorkspacePathNotAllowed", rel, err)
				}
				if err := tc.w.WriteWorkspaceFileBytes(ctx, tc.ep, "ws-1", rel, []byte("x")); !errors.Is(err, core.ErrWorkspacePathNotAllowed) {
					t.Fatalf("WriteWorkspaceFileBytes(%q) = %v, want ErrWorkspacePathNotAllowed", rel, err)
				}
			}
			if n := tc.served.Load(); n != 0 {
				t.Fatalf("%d request(s) reached the core for refused paths", n)
			}
			if err := tc.w.WriteWorkspaceFile(ctx, tc.ep, "ws-1", ".room/history.md", "x"); err != nil {
				t.Fatalf("owned path refused: %v", err)
			}
			if tc.served.Load() != 1 {
				t.Fatalf("owned path write did not reach the core")
			}
		})
	}
}

// A manifest core and the Core Protocol client refuse what the core does not
// offer with the same error (core.CapabilityMissingError, errors.Is
// core.ErrCapabilityMissing) and never send it: a profile without
// agentProfile, an execution without sandboxExec, an optional method whose
// op or capability is absent. A daemon's own capability_missing envelope
// matches too.
func TestCapabilityMissingReportedAlike(t *testing.T) {
	ctx := context.Background()
	manifestSrv, manifestCalls := countingDaemon(t)
	mp, err := manifestcore.Parse([]byte(policyManifest))
	if err != nil {
		t.Fatal(err)
	}
	protoSrv, protoCalls := countingDaemon(t)
	providers := []struct {
		name   string
		p      core.Provider
		ep     core.Endpoint
		served *atomic.Int64
	}{
		{"manifest", mp, core.Endpoint{BaseURL: manifestSrv.URL}, manifestCalls},
		{"protocol", protocol.NewClient("nimbus", protoSrv.Client()), core.Endpoint{BaseURL: protoSrv.URL}, protoCalls},
	}
	exec := &core.Execution{Sandbox: core.SandboxExecution{URL: "http://127.0.0.1:1", ExecToken: "tok", Workdir: "/w"}}
	profile := &core.AgentProfile{Instructions: "review"}
	for _, tc := range providers {
		t.Run(tc.name, func(t *testing.T) {
			checks := []struct {
				what string
				want core.Capability
				err  error
			}{
				{"session with a profile", core.CapabilityAgentProfile, func() error {
					_, err := tc.p.CreateSession(ctx, tc.ep, core.SessionRequest{WorkspaceID: "ws-1", Prompt: "go", Profile: profile})
					return err
				}()},
				{"session with an execution", core.CapabilitySandboxExec, func() error {
					_, err := tc.p.CreateSession(ctx, tc.ep, core.SessionRequest{WorkspaceID: "ws-1", Prompt: "go", Options: core.SessionOptions{Execution: exec}})
					return err
				}()},
				{"input with an execution", core.CapabilitySandboxExec, tc.p.SendInput(ctx, tc.ep, "ws-1", "s-1", "go", core.SessionOptions{Execution: exec})},
				{"interrupt", core.CapabilityInterrupt, tc.p.(core.TurnInterrupter).Interrupt(ctx, tc.ep, "ws-1", "s-1")},
				{"delete workspace", core.CapabilityDeleteWorkspace, tc.p.(core.WorkspaceDeleter).DeleteWorkspace(ctx, tc.ep, "ws-1")},
			}
			for _, c := range checks {
				var missing *core.CapabilityMissingError
				if !errors.As(c.err, &missing) || missing.Capability != c.want || !errors.Is(c.err, core.ErrCapabilityMissing) {
					t.Errorf("%s: err = %v, want capability_missing %q", c.what, c.err, c.want)
				}
			}
			if n := tc.served.Load(); n != 0 {
				t.Fatalf("%d refused request(s) reached the core", n)
			}
		})
	}

	envelope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == protocol.PathDiscover {
			_ = json.NewEncoder(w).Encode(protocol.Discovery{Name: "nimbus", Version: "1", Protocol: protocol.ProtocolRange{Min: "1.0", Max: "1.0"}})
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(protocol.ErrorEnvelope{Error: protocol.ProtocolError{Code: protocol.ErrorCapabilityMissing, Message: "no"}})
	}))
	defer envelope.Close()
	_, err = protocol.NewClient("nimbus", envelope.Client()).CreateSession(ctx, core.Endpoint{BaseURL: envelope.URL}, core.SessionRequest{WorkspaceID: "ws-1", Prompt: "go"})
	if !errors.Is(err, core.ErrCapabilityMissing) {
		t.Fatalf("daemon capability_missing envelope = %v, want errors.Is core.ErrCapabilityMissing", err)
	}
}
