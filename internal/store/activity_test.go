package store

import (
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Stale session-state writes never overwrite a newer canonical version.
func TestPutSessionStateMonotonic(t *testing.T) {
	m := NewMemory()
	if v, _ := m.PutSessionState(testCtx(), wire.ReportSessionStateInput{WorkspaceID: "r", AgentSessionID: "s", MessageVersion: 10}); v != 10 {
		t.Fatalf("first version = %d, want 10", v)
	}
	// Older write is ignored; stored version stays 10.
	if v, _ := m.PutSessionState(testCtx(), wire.ReportSessionStateInput{WorkspaceID: "r", AgentSessionID: "s", MessageVersion: 4}); v != 10 {
		t.Fatalf("stale write version = %d, want 10", v)
	}
	// Newer write advances.
	if v, _ := m.PutSessionState(testCtx(), wire.ReportSessionStateInput{WorkspaceID: "r", AgentSessionID: "s", MessageVersion: 12}); v != 12 {
		t.Fatalf("newer write version = %d, want 12", v)
	}
}
