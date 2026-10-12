package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMemoryContentPromptUses(t *testing.T) {
	testContentPromptUses(t, NewMemoryContentComponents(), "pu-mem")
}

func TestMySQLContentPromptUses(t *testing.T) {
	dsn := os.Getenv("BNBLOC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set BNBLOC_TEST_MYSQL_DSN to run the MySQL integration test")
	}
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	prefix := "pu-it-" + itoaBase(time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = b.DB.Exec(`DELETE FROM content_prompt_uses WHERE component_id LIKE ?`, prefix+"%")
		_, _ = b.DB.Exec(`DELETE FROM content_component_versions WHERE component_id LIKE ?`, prefix+"%")
		_, _ = b.DB.Exec(`DELETE FROM content_components WHERE id LIKE ?`, prefix+"%")
		_ = b.DB.Close()
	})
	testContentPromptUses(t, b.ContentComponents, prefix)
	// The prompt library's table is gone (start empty).
	var n int
	if err := b.DB.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'prompts'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("prompts table count = %d, %v", n, err)
	}
}

func testContentPromptUses(t *testing.T, s ContentComponentStore, prefix string) {
	ctx := context.Background()
	blob := "sha256/" + strings.Repeat("c", 64)
	mk := func(id string) {
		c := ContentComponent{ID: id, Kind: "bnbloc.prompt.package/v1", Name: "writing-" + strings.TrimPrefix(id, prefix), TenantID: prefix + "-t", Owner: "maya",
			Scope: ContentScopePersonal, Status: ContentStatusActive, CreatedAtMs: 1, UpdatedAtMs: 1}
		if err := s.CreateContentComponent(ctx, c, contentVersion(id, "1.0.0", blob, 1)); err != nil {
			t.Fatal(err)
		}
	}
	a, b := prefix+"-a", prefix+"-b"
	mk(a)
	mk(b)
	for i := 0; i < 3; i++ {
		if err := s.RecordContentPromptUse(ctx, a, "notes", int64(10+i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordContentPromptUse(ctx, a, "summary", 20); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordContentPromptUse(ctx, b, "notes", 20); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordContentPromptUse(ctx, prefix+"-missing", "notes", 20); !errors.Is(err, ErrContentNotFound) {
		t.Fatalf("missing component = %v", err)
	}
	got, err := s.ContentPromptUses(ctx, []string{a, b, prefix + "-none"})
	if err != nil || got[a]["notes"] != 3 || got[a]["summary"] != 1 || got[b]["notes"] != 1 || len(got) != 2 {
		t.Fatalf("uses = %+v, %v", got, err)
	}
	if empty, err := s.ContentPromptUses(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("no ids = %+v, %v", empty, err)
	}
	// Deleting a component deletes its counts.
	if err := s.DeleteContentComponent(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err = s.ContentPromptUses(ctx, []string{a, b})
	if err != nil || len(got[a]) != 0 || got[b]["notes"] != 1 {
		t.Fatalf("after delete = %+v, %v", got, err)
	}
}
