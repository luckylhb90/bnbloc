package store

import (
	"context"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/testmysql"
)

// TestContentUseCountsTenantMigrationConvergesOldShape: a database with the
// shared counters (content_prompt_uses without tenant_id, the component-only
// use index) converges on the per-tenant shape; the old counters, which no
// organization can be told apart in, are gone; a rerun is a no-op.
func TestContentUseCountsTenantMigrationConvergesOldShape(t *testing.T) {
	b, err := NewBackends(testmysql.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	defer b.DB.Close()
	ctx := context.Background()
	run := migrationRun(t, "2026_10_content_use_counts_tenant")
	for _, q := range []string{
		"DROP TABLE content_prompt_uses",
		`CREATE TABLE content_prompt_uses (component_id VARCHAR(64) NOT NULL, prompt_id VARCHAR(64) NOT NULL,
  use_count BIGINT NOT NULL DEFAULT 0, last_used_at_ms BIGINT NOT NULL, PRIMARY KEY (component_id, prompt_id))`,
		"INSERT INTO content_prompt_uses VALUES ('cc_builtin_starters', 'notes', 7, 1)",
		"ALTER TABLE content_component_uses DROP INDEX idx_content_uses_component, ADD KEY idx_content_uses_component (component_id)",
	} {
		if _, err := b.DB.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := run(ctx, b.DB); err != nil {
			t.Fatalf("migration run %d: %v", i+1, err)
		}
		if i == 0 {
			if err := b.ContentComponents.RecordContentPromptUse(ctx, "org-a", "cc_builtin_starters", "notes", 2); err != nil {
				t.Fatal(err)
			}
		}
	}
	cols, err := indexColumns(ctx, b.DB, "content_prompt_uses", "PRIMARY")
	if err != nil || strings.Join(cols, ",") != "component_id,tenant_id,prompt_id" {
		t.Fatalf("content_prompt_uses key = %v, %v", cols, err)
	}
	cols, err = indexColumns(ctx, b.DB, "content_component_uses", "idx_content_uses_component")
	if err != nil || strings.Join(cols, ",") != "component_id,tenant_id,scope_kind" {
		t.Fatalf("use index = %v, %v", cols, err)
	}
	got, err := b.ContentComponents.ContentPromptUses(ctx, "org-a", []string{"cc_builtin_starters"})
	if err != nil || got["cc_builtin_starters"]["notes"] != 1 {
		t.Fatalf("counts after the migration = %+v, %v; the shared counter is dropped, the new one kept", got, err)
	}
}
