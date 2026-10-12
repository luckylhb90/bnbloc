package store

import (
	"context"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// mysqlArtifacts opens the real MySQL artifact store (DSN-gated like the other
// MySQL integration tests).
func mysqlArtifacts(t *testing.T) (*MySQLArtifacts, func()) {
	t.Helper()
	dsn := mysqlTestDSN(t)
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	m, ok := b.Artifacts.(*MySQLArtifacts)
	if !ok {
		t.Fatalf("Artifacts is %T, want *MySQLArtifacts", b.Artifacts)
	}
	return m, func() { b.DB.Close() }
}

// The MySQL CAS UPDATE is the production concurrency guard: two racing
// publishers, exactly one wins, history stays immutable.
func TestMySQLArtifactVersionCAS(t *testing.T) {
	m, closeDB := mysqlArtifacts(t)
	defer closeDB()
	ctx := context.Background()
	tid := "t-artifact-" + itoaBase(time.Now().UnixNano())
	ctx = tenant.NewContext(ctx, tid)
	id := "art-cas-" + itoaBase(time.Now().UnixNano())
	if _, err := m.PutArtifact(ctx, Artifact{ArtifactID: id, RoomID: "r", Kind: ArtifactKindMarkdown, Title: "spec", CreatedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.PublishArtifactVersion(ctx, id, ArtifactVersion{Content: "one", AuthorUserID: "alice"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.PublishArtifactVersion(ctx, id, ArtifactVersion{Content: "two-a", AuthorUserID: "bob"}, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.PublishArtifactVersion(ctx, id, ArtifactVersion{Content: "two-b"}, 1); err != ErrArtifactVersionConflict {
		t.Fatalf("stale publish err = %v, want conflict", err)
	}
	got, err := m.GetArtifactVersion(ctx, id, 2)
	if err != nil || got.Content != "two-a" {
		t.Fatalf("v2 = %+v, %v: the loser's write must not have landed", got, err)
	}
	// List omits content; per-version fetch serves it.
	for _, v := range m.ListArtifactVersions(ctx, id, 10) {
		if v.Content != "" {
			t.Fatal("version list leaked content")
		}
	}
}
