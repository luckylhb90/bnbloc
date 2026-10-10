package store

import (
	"errors"
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

// The submit barrier is queryable only after a successful record, and a replay
// reports ErrDuplicateSubmit while remaining queryable.
func TestRecordSubmitProvenanceBarrier(t *testing.T) {
	m := NewMemory()
	if m.SubmitQueryable(testCtx(), "r", "s", "cs") {
		t.Fatal("submit queryable before record")
	}
	if err := m.RecordSubmitProvenance(testCtx(), wire.SubmitProvenanceInput{WorkspaceID: "r", AgentSessionID: "s", ClientSubmitID: "cs"}); err != nil {
		t.Fatalf("first record: %v", err)
	}
	if !m.SubmitQueryable(testCtx(), "r", "s", "cs") {
		t.Fatal("submit not queryable after record")
	}
	if err := m.RecordSubmitProvenance(testCtx(), wire.SubmitProvenanceInput{WorkspaceID: "r", AgentSessionID: "s", ClientSubmitID: "cs"}); !errors.Is(err, ErrDuplicateSubmit) {
		t.Fatalf("replay err = %v, want ErrDuplicateSubmit", err)
	}
}
