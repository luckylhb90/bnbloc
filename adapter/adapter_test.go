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
