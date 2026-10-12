package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMemoryContentGapClosure(t *testing.T) {
	testContentGapClosure(t, NewMemoryContentComponents(), "cg-mem")
}

func TestMySQLContentGapClosure(t *testing.T) {
	dsn := mysqlTestDSN(t)
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	prefix := "cg-it-" + itoaBase(time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = b.DB.Exec(`DELETE FROM content_room_shares WHERE component_id LIKE ?`, prefix+"%")
		_, _ = b.DB.Exec(`DELETE FROM content_component_versions WHERE component_id LIKE ?`, prefix+"%")
		_, _ = b.DB.Exec(`DELETE FROM content_components WHERE id LIKE ?`, prefix+"%")
		_ = b.DB.Close()
	})
	testContentGapClosure(t, b.ContentComponents, prefix)
}

// testContentGapClosure covers the store side of the content gap closure:
// an organization's names are unique per kind (an unpublished row keeps its
// name, a removed one frees it), platform tenant scope round-trips, the
// name filter, and room shares (upsert keeps the creation time, listing by
// room or component, revoke, deleted with the component).
func testContentGapClosure(t *testing.T, s ContentComponentStore, prefix string) {
	ctx := context.Background()
	tid := prefix + "-t"
	blob := "sha256/" + strings.Repeat("d", 64)
	org := func(id, author string) ContentComponent {
		return ContentComponent{ID: prefix + id, Kind: "bnbloc.skill.package/v1", Name: "review-kit", TenantID: tid, Owner: author,
			Scope: ContentScopeOrg, Status: ContentStatusActive, SourceID: prefix + id + "-src", CreatedAtMs: 1, UpdatedAtMs: 1}
	}
	a := org("-org-a", "maya")
	if err := s.CreateContentComponent(ctx, a, contentVersion(a.ID, "1.0.0", blob, 1)); err != nil {
		t.Fatal(err)
	}
	b := org("-org-b", "jack")
	if err := s.CreateContentComponent(ctx, b, contentVersion(b.ID, "1.0.0", blob, 1)); !errors.Is(err, ErrContentOrgNameTaken) {
		t.Fatalf("a second author's org row of the same name = %v, want ErrContentOrgNameTaken", err)
	}
	// Another kind, another organization, or a personal row of the name is fine.
	for i, c := range []ContentComponent{
		func() ContentComponent { c := org("-org-kind", "jack"); c.Kind = "bnbloc.prompt.package/v1"; return c }(),
		func() ContentComponent { c := org("-org-tenant", "jack"); c.TenantID = tid + "-other"; return c }(),
		func() ContentComponent {
			c := org("-personal", "jack")
			c.Scope, c.SourceID = ContentScopePersonal, ""
			return c
		}(),
	} {
		if err := s.CreateContentComponent(ctx, c, contentVersion(c.ID, "1.0.0", blob, 1)); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
	}
	// An unpublished org row keeps its name and can be restored.
	if err := s.SetContentComponentStatus(ctx, a.ID, ContentStatusRetired, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateContentComponent(ctx, b, contentVersion(b.ID, "1.0.0", blob, 1)); !errors.Is(err, ErrContentOrgNameTaken) {
		t.Fatalf("name of an unpublished org row = %v, want taken", err)
	}
	if err := s.SetContentComponentStatus(ctx, a.ID, ContentStatusActive, 3); err != nil {
		t.Fatalf("restore: %v", err)
	}
	named, err := s.ListContentComponents(ctx, ContentComponentQuery{TenantID: tid, Kind: a.Kind, Scopes: []ContentScope{ContentScopeOrg}, Name: "review-kit"})
	if err != nil || len(named) != 1 || named[0].ID != a.ID {
		t.Fatalf("name filter = %+v, %v", named, err)
	}
	// Removing it frees the name.
	if err := s.DeleteContentComponent(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateContentComponent(ctx, b, contentVersion(b.ID, "1.0.0", blob, 1)); err != nil {
		t.Fatalf("name after removal: %v", err)
	}

	// Platform tenant scope.
	p := ContentComponent{ID: prefix + "-plat", Kind: "bnbloc.expert.package/v1", Name: prefix + "-plat", Scope: ContentScopePlatform,
		Status: ContentStatusActive, CreatedAtMs: 1, UpdatedAtMs: 1}
	if err := s.CreateContentComponent(ctx, p, contentVersion(p.ID, "1.0.0", blob, 1)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetContentComponent(ctx, p.ID); got.EnabledTenants != nil {
		t.Fatalf("new platform row tenants = %v, want nil", got.EnabledTenants)
	}
	if err := s.SetContentComponentTenants(ctx, p.ID, []string{"t1", "t2"}, 5); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetContentComponent(ctx, p.ID); !reflect.DeepEqual(got.EnabledTenants, []string{"t1", "t2"}) || got.UpdatedAtMs != 5 {
		t.Fatalf("tenants = %v at %d", got.EnabledTenants, got.UpdatedAtMs)
	}
	if err := s.SetContentComponentTenants(ctx, p.ID, nil, 6); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetContentComponent(ctx, p.ID); got.EnabledTenants != nil {
		t.Fatalf("cleared tenants = %v", got.EnabledTenants)
	}
	if err := s.SetContentComponentTenants(ctx, prefix+"-missing", []string{"t1"}, 6); !errors.Is(err, ErrContentNotFound) {
		t.Fatalf("tenants of a missing component = %v", err)
	}

	// Room shares.
	src := ContentComponent{ID: prefix + "-share-src", Kind: "bnbloc.expert.package/v1", Name: "reviewer", TenantID: tid, Owner: "maya",
		Scope: ContentScopePersonal, Status: ContentStatusActive, CreatedAtMs: 1, UpdatedAtMs: 1}
	if err := s.CreateContentComponent(ctx, src, contentVersion(src.ID, "1.0.0", blob, 1)); err != nil {
		t.Fatal(err)
	}
	room1, room2 := prefix+"-room1", prefix+"-room2"
	put := func(room, version string, at int64) {
		t.Helper()
		if err := s.PutContentRoomShare(ctx, ContentRoomShare{TenantID: tid, RoomID: room, ComponentID: src.ID, Version: version, SharedBy: "maya",
			Note: "v" + version, CreatedAtMs: at, UpdatedAtMs: at}); err != nil {
			t.Fatal(err)
		}
	}
	put(room1, "1.0.0", 10)
	put(room2, "1.0.0", 11)
	put(room1, "1.1.0", 12) // a later share moves it
	got, err := s.ListContentRoomShares(ctx, ContentRoomShareQuery{RoomID: room1})
	if err != nil || len(got) != 1 || got[0].Version != "1.1.0" || got[0].Note != "v1.1.0" || got[0].CreatedAtMs != 10 || got[0].UpdatedAtMs != 12 {
		t.Fatalf("room1 shares = %+v, %v", got, err)
	}
	got, err = s.ListContentRoomShares(ctx, ContentRoomShareQuery{ComponentID: src.ID})
	if err != nil || len(got) != 2 || got[0].RoomID != room1 || got[1].RoomID != room2 {
		t.Fatalf("component shares = %+v, %v", got, err)
	}
	if err := s.PutContentRoomShare(ctx, ContentRoomShare{TenantID: tid, RoomID: room1, ComponentID: prefix + "-missing", Version: "1.0.0", SharedBy: "maya"}); !errors.Is(err, ErrContentNotFound) {
		t.Fatalf("share of a missing component = %v", err)
	}
	if ok, err := s.DeleteContentRoomShare(ctx, room2, src.ID); err != nil || !ok {
		t.Fatalf("revoke = %v, %v", ok, err)
	}
	if ok, err := s.DeleteContentRoomShare(ctx, room2, src.ID); err != nil || ok {
		t.Fatalf("revoke again = %v, %v", ok, err)
	}
	if err := s.DeleteContentComponent(ctx, src.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListContentRoomShares(ctx, ContentRoomShareQuery{RoomID: room1}); len(got) != 0 {
		t.Fatalf("shares outlived their component: %+v", got)
	}
}

// TestContentOrgNamesMigrationKeepsOldest: a database holding two
// organization rows of one name (possible before the name key) keeps the
// oldest published and unpublishes the other, which cannot be restored
// while the name is held.
func TestContentOrgNamesMigrationKeepsOldest(t *testing.T) {
	dsn := mysqlTestDSN(t)
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer b.DB.Close()
	ctx := context.Background()
	prefix := "cgm-it-" + itoaBase(time.Now().UnixNano())
	defer func() {
		_, _ = b.DB.Exec(`DELETE FROM content_component_versions WHERE component_id LIKE ?`, prefix+"%")
		_, _ = b.DB.Exec(`DELETE FROM content_components WHERE id LIKE ?`, prefix+"%")
	}()
	if _, err := b.DB.ExecContext(ctx, "ALTER TABLE content_components DROP INDEX uq_content_components_org_name, DROP COLUMN org_name"); err != nil {
		t.Fatal(err)
	}
	for i, author := range []string{"maya", "jack"} {
		id := prefix + "-" + author
		if _, err := b.DB.ExecContext(ctx, `INSERT INTO content_components (id, kind, name, tenant_id, owner, scope, status, latest_version, source_id, created_at_ms, updated_at_ms)
  VALUES (?, 'bnbloc.skill.package/v1', 'review-kit', ?, ?, 'org', 'active', '1.0.0', ?, ?, ?)`, id, prefix+"-t", author, id+"-src", i, i); err != nil {
			t.Fatal(err)
		}
	}
	run := migrationRun(t, "2026_10_content_org_names")
	for i := 0; i < 2; i++ {
		if err := run(ctx, b.DB); err != nil {
			t.Fatalf("migration run %d: %v", i+1, err)
		}
	}
	keep, err := b.ContentComponents.GetContentComponent(ctx, prefix+"-jack")
	if err != nil {
		t.Fatal(err)
	}
	// ids order by creation: "-jack" < "-maya" sorts first, so it is the
	// one kept.
	if keep.Status != ContentStatusActive {
		t.Fatalf("oldest row status = %s", keep.Status)
	}
	dup, err := b.ContentComponents.GetContentComponent(ctx, prefix+"-maya")
	if err != nil || dup.Status != ContentStatusRetired {
		t.Fatalf("duplicate = %+v, %v", dup, err)
	}
	if err := b.ContentComponents.SetContentComponentStatus(ctx, dup.ID, ContentStatusActive, 9); !errors.Is(err, ErrContentOrgNameTaken) {
		t.Fatalf("restoring the duplicate = %v, want ErrContentOrgNameTaken", err)
	}
	if ok, err := indexExists(ctx, b.DB, "content_components", "uq_content_components_org_name"); err != nil || !ok {
		t.Fatalf("org name key = %v, %v", ok, err)
	}
}
