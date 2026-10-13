package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/testmysql"
)

// TestContentWorksMigrationMovesApps: the rows QA published under the app
// kind (a component, its version, a room's use, a publish request) read as
// works after the migration, their manifests naming the work kind and the
// type "app"; other kinds are untouched; a rerun is a no-op.
func TestContentWorksMigrationMovesApps(t *testing.T) {
	b, err := NewBackends(testmysql.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	defer b.DB.Close()
	ctx := context.Background()
	const appKind, workKind, skillKind = "bnbloc.app.package/v1", "bnbloc.work.package/v1", "bnbloc.skill.package/v1"
	prefix := uniq("cw-")
	for _, c := range []struct{ id, kind, name string }{{prefix + "-app", appKind, "lunch-poll"}, {prefix + "-skill", skillKind, "review"}} {
		manifest, _ := json.Marshal(map[string]any{"kind": c.kind, "name": c.name, "version": "1.0.0", "category": "life"})
		if err := b.ContentComponents.CreateContentComponent(ctx,
			ContentComponent{ID: c.id, Kind: c.kind, Name: c.name, TenantID: prefix + "-t", Owner: "maya", Scope: ContentScopePersonal, Status: ContentStatusActive, CreatedAtMs: 1, UpdatedAtMs: 1},
			ContentComponentVersion{Version: "1.0.0", ManifestJSON: manifest, BundleBlobKey: "sha256/" + c.name, SHA256: c.name, SizeBytes: 1, CreatedBy: "maya", CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
		if err := b.ContentComponents.PutContentUse(ctx, ContentUse{TenantID: prefix + "-t", ScopeKind: ContentUseRoom, ScopeID: prefix + "-room", ComponentID: c.id, Kind: c.kind, CreatedBy: "maya", CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.DB.ExecContext(ctx, `INSERT INTO content_publish_requests (request_id, tenant_id, component_id, kind, name, version, requested_by, status, created_at_ms)
  VALUES (?, ?, ?, ?, 'lunch-poll', '1.0.0', 'maya', 'pending', 1)`, prefix+"-req", prefix+"-t", prefix+"-app", appKind); err != nil {
		t.Fatal(err)
	}
	run := migrationRun(t, "2026_10_content_works")
	for i := 0; i < 2; i++ {
		if err := run(ctx, b.DB); err != nil {
			t.Fatalf("migration run %d: %v", i+1, err)
		}
	}
	app, err := b.ContentComponents.GetContentComponent(ctx, prefix+"-app")
	if err != nil || app.Kind != workKind {
		t.Fatalf("the app = %+v, %v (want the work kind)", app, err)
	}
	v, err := b.ContentComponents.GetContentComponentVersion(ctx, app.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(v.ManifestJSON, &m); err != nil || m["kind"] != workKind || m["type"] != "app" || m["category"] != "life" || m["name"] != "lunch-poll" {
		t.Fatalf("the app's manifest = %s, %v", v.ManifestJSON, err)
	}
	skill, err := b.ContentComponents.GetContentComponent(ctx, prefix+"-skill")
	if err != nil || skill.Kind != skillKind {
		t.Fatalf("the skill pack = %+v, %v (untouched)", skill, err)
	}
	var sm map[string]any
	if sv, err := b.ContentComponents.GetContentComponentVersion(ctx, skill.ID, "1.0.0"); err != nil || json.Unmarshal(sv.ManifestJSON, &sm) != nil || sm["kind"] != skillKind || sm["type"] != nil {
		t.Fatalf("the skill pack's manifest = %s, %v", sv.ManifestJSON, err)
	}
	uses, err := b.ContentComponents.ListContentUses(ctx, ContentUseRoom, prefix+"-room")
	if err != nil || len(uses) != 2 {
		t.Fatalf("uses = %+v, %v", uses, err)
	}
	for _, u := range uses {
		if want := map[string]string{prefix + "-app": workKind, prefix + "-skill": skillKind}[u.ComponentID]; u.Kind != want {
			t.Errorf("use of %s = %s, want %s", u.ComponentID, u.Kind, want)
		}
	}
	var reqKind string
	if err := b.DB.QueryRowContext(ctx, "SELECT kind FROM content_publish_requests WHERE request_id = ?", prefix+"-req").Scan(&reqKind); err != nil || reqKind != workKind {
		t.Fatalf("the publish request's kind = %q, %v", reqKind, err)
	}
}
