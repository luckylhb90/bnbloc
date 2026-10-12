package store

import (
	"context"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/testmysql"
)

// TestContentSharingMigrationConvergesOldShape: a database deployed before
// step 4 (content_components without source_id/shared_by, the old unique
// name key, no content_publish_requests) converges on the current shape, and
// a rerun is a no-op.
func TestContentSharingMigrationConvergesOldShape(t *testing.T) {
	b, err := NewBackends(testmysql.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	defer b.DB.Close()
	ctx := context.Background()
	run := migrationRun(t, "2026_10_content_sharing")
	for _, q := range []string{
		"DROP TABLE IF EXISTS content_publish_requests",
		"ALTER TABLE content_components DROP INDEX idx_content_components_source",
		"ALTER TABLE content_components DROP INDEX uq_content_components_name, ADD UNIQUE KEY uq_content_components_name (tenant_id, scope, owner, kind, name)",
		"ALTER TABLE content_components DROP COLUMN shared_by, DROP COLUMN source_id",
	} {
		if _, err := b.DB.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := run(ctx, b.DB); err != nil {
			t.Fatalf("migration run %d: %v", i+1, err)
		}
	}
	cols, err := indexColumns(ctx, b.DB, "content_components", "uq_content_components_name")
	if err != nil || strings.Join(cols, ",") != "tenant_id,scope,owner,kind,name,source_id" {
		t.Fatalf("unique key = %v, %v", cols, err)
	}
	if ok, err := indexExists(ctx, b.DB, "content_components", "idx_content_components_source"); err != nil || !ok {
		t.Fatalf("source index = %v, %v", ok, err)
	}
	if _, err := b.ContentComponents.ListContentRequests(ctx, ContentRequestQuery{TenantID: "none"}); err != nil {
		t.Fatalf("requests table: %v", err)
	}
	if _, err := b.ContentComponents.ListContentComponents(ctx, ContentComponentQuery{SourceID: "none"}); err != nil {
		t.Fatalf("source column: %v", err)
	}
}
