package adapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The adapter posts session-state with roomId == workspaceId (no translation)
// and returns the sync cursor.
func TestActivityReporterReportsSessionState(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"syncCursor":7}`))
	}))
	defer srv.Close()

	rep := NewActivityReporter(Config{GatewayBaseURL: srv.URL, SessionToken: "tok"})
	reply, err := rep.ReportSessionState(context.Background(), wire.ReportSessionStateInput{
		WorkspaceID: "room-1", AgentSessionID: "s1", MessageVersion: 7,
	})
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if reply.SyncCursor != 7 {
		t.Fatalf("syncCursor = %d, want 7", reply.SyncCursor)
	}
	if gotPath != "/v1/rooms/room-1/agent-activity/session-state" {
		t.Fatalf("path = %q (roomId must equal workspaceId)", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("auth = %q, want Bearer tok", gotAuth)
	}
}

// A shared-agent denial surfaces as a fail-closed typed error carrying the
// stable reason.
func TestSharedAgentTurnFailsClosedOnDeny(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"denyReason":"owner_offline"}`))
	}))
	defer srv.Close()

	c := NewSharedAgentAccessClient(Config{GatewayBaseURL: srv.URL})
	_, _, err := c.Turn(context.Background(), TurnRequest{RoomID: "room-1", TargetID: "shared-agent:codex", GrantID: "g1", Model: "gpt-5"})
	denied, ok := err.(*ErrSharedAgentDenied)
	if !ok {
		t.Fatalf("err = %v, want *ErrSharedAgentDenied", err)
	}
	if denied.Reason != "owner_offline" {
		t.Fatalf("reason = %q, want owner_offline", denied.Reason)
	}
}

// An allowed turn returns the proxied result and the lease id, with no
// credential in the request the adapter sends.
func TestSharedAgentTurnAllowed(t *testing.T) {
	var sentBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sentBody, _ = readAll(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accepted":true,"leaseId":"lease-1","result":{"text":"ok"}}`))
	}))
	defer srv.Close()

	c := NewSharedAgentAccessClient(Config{GatewayBaseURL: srv.URL})
	result, lease, err := c.Turn(context.Background(), TurnRequest{
		RoomID: "room-1", TargetID: "shared-agent:codex", GrantID: "g1",
		Model: "gpt-5", TurnCapability: "consult",
	})
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	if lease != "lease-1" {
		t.Fatalf("lease = %q, want lease-1", lease)
	}
	if m, ok := result.(map[string]any); !ok || m["text"] != "ok" {
		t.Fatalf("result = %v, want {text:ok}", result)
	}
	if containsAny(string(sentBody), "credential", "apiKey", "secret", "token") {
		t.Fatalf("adapter turn body leaked a credential field: %s", sentBody)
	}
}
