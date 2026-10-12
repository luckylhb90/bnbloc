package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMemoryContentComponentStore(t *testing.T) {
	testContentComponentStore(t, NewMemoryContentComponents(), "cc-mem")
}

func TestMySQLContentComponentStore(t *testing.T) {
	dsn := os.Getenv("BNBLOC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set BNBLOC_TEST_MYSQL_DSN to run the MySQL integration test")
	}
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	prefix := "cc-it-" + itoaBase(time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = b.DB.Exec(`DELETE FROM content_permission_acks WHERE component_id LIKE ?`, prefix+"%")
		_, _ = b.DB.Exec(`DELETE FROM content_component_uses WHERE component_id LIKE ?`, prefix+"%")
		_, _ = b.DB.Exec(`DELETE FROM content_component_versions WHERE component_id LIKE ?`, prefix+"%")
		_, _ = b.DB.Exec(`DELETE FROM content_components WHERE id LIKE ?`, prefix+"%")
		_ = b.DB.Close()
	})
	testContentComponentStore(t, b.ContentComponents, prefix)

	// jack's remaining version keeps its bundle referenced (blob GC keeps it).
	kept := "sha256/" + strings.Repeat("b", 64)
	unused := "sha256/" + strings.Repeat("9", 64)
	refs, err := NewRetention(b).ReferencedBlobKeys(context.Background(), []string{kept, unused})
	if err != nil || !refs[kept] || refs[unused] {
		t.Fatalf("refs = %+v, %v", refs, err)
	}
}

func contentVersion(id, version, blob string, at int64) ContentComponentVersion {
	return ContentComponentVersion{ComponentID: id, Version: version, ManifestJSON: json.RawMessage(`{"kind":"bnbloc.expert.package/v1","name":"reviewer","version":"` + version + `"}`),
		BundleBlobKey: blob, SHA256: strings.TrimPrefix(blob, "sha256/"), SizeBytes: 10, CreatedBy: "maya", CreatedAtMs: at}
}

func testContentComponentStore(t *testing.T, s ContentComponentStore, prefix string) {
	ctx := context.Background()
	tid := prefix + "-t"
	blob1 := "sha256/" + strings.Repeat("a", 64)
	blob2 := "sha256/" + strings.Repeat("b", 64)
	c := ContentComponent{ID: prefix + "-1", Kind: "bnbloc.expert.package/v1", Name: "reviewer", TenantID: tid, Owner: "maya",
		Scope: ContentScopePersonal, Status: ContentStatusActive, DisplayName: "Reviewer", CreatedAtMs: 1, UpdatedAtMs: 1}
	if err := s.CreateContentComponent(ctx, c, contentVersion(c.ID, "1.0.0", blob1, 1)); err != nil {
		t.Fatal(err)
	}
	dup := c
	dup.ID = prefix + "-dup"
	if err := s.CreateContentComponent(ctx, dup, contentVersion(dup.ID, "1.0.0", blob1, 1)); !errors.Is(err, ErrContentExists) {
		t.Fatalf("duplicate name = %v", err)
	}
	// Another member may use the same name in their personal scope.
	jack := c
	jack.ID, jack.Owner, jack.UpdatedAtMs = prefix+"-2", "jack", 5
	if err := s.CreateContentComponent(ctx, jack, contentVersion(jack.ID, "0.1.0", blob2, 5)); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetContentComponent(ctx, c.ID)
	if err != nil || got.LatestVersion != "1.0.0" || got.DisplayName != "Reviewer" || got.Scope != ContentScopePersonal {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := s.GetContentComponent(ctx, prefix+"-none"); !errors.Is(err, ErrContentNotFound) {
		t.Fatalf("missing = %v", err)
	}

	// A new version moves latest; a stale base or a repeated version is a
	// conflict.
	next := c
	next.DisplayName, next.UpdatedAtMs = "Strict reviewer", 10
	if err := s.AddContentComponentVersion(ctx, next, "1.0.0", contentVersion(c.ID, "1.0.1", blob2, 10)); err != nil {
		t.Fatal(err)
	}
	if err := s.AddContentComponentVersion(ctx, next, "1.0.0", contentVersion(c.ID, "1.0.2", blob2, 11)); !errors.Is(err, ErrContentVersionConflict) {
		t.Fatalf("stale base = %v", err)
	}
	if err := s.AddContentComponentVersion(ctx, next, "1.0.1", contentVersion(c.ID, "1.0.1", blob2, 11)); !errors.Is(err, ErrContentVersionConflict) {
		t.Fatalf("repeated version = %v", err)
	}
	got, _ = s.GetContentComponent(ctx, c.ID)
	if got.LatestVersion != "1.0.1" || got.DisplayName != "Strict reviewer" || got.UpdatedAtMs != 10 {
		t.Fatalf("after version = %+v", got)
	}
	vs, err := s.ListContentComponentVersions(ctx, c.ID)
	if err != nil || len(vs) != 2 || vs[0].Version != "1.0.1" || vs[1].Version != "1.0.0" {
		t.Fatalf("versions = %+v, %v", vs, err)
	}
	v, err := s.GetContentComponentVersion(ctx, c.ID, "1.0.0")
	if err != nil || v.BundleBlobKey != blob1 || !strings.Contains(string(v.ManifestJSON), `"version"`) {
		t.Fatalf("version = %+v, %v", v, err)
	}
	if _, err := s.GetContentComponentVersion(ctx, c.ID, "9.9.9"); !errors.Is(err, ErrContentNotFound) {
		t.Fatalf("missing version = %v", err)
	}
	byBlob, err := s.ContentVersionsByBlob(ctx, blob2)
	if err != nil || len(byBlob) != 2 {
		t.Fatalf("by blob = %+v, %v", byBlob, err)
	}

	// Listing filters by tenant, owner, kind, scope and ids, newest first.
	list, err := s.ListContentComponents(ctx, ContentComponentQuery{TenantID: tid})
	if err != nil || len(list) != 2 || list[0].ID != c.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}
	list, _ = s.ListContentComponents(ctx, ContentComponentQuery{TenantID: tid, Owner: "jack", Scopes: []ContentScope{ContentScopePersonal}})
	if len(list) != 1 || list[0].ID != jack.ID {
		t.Fatalf("jack's = %+v", list)
	}
	list, _ = s.ListContentComponents(ctx, ContentComponentQuery{TenantID: tid, Kind: "bnbloc.skill.package/v1"})
	if len(list) != 0 {
		t.Fatalf("skills = %+v", list)
	}
	list, _ = s.ListContentComponents(ctx, ContentComponentQuery{IDs: []string{jack.ID}})
	if len(list) != 1 {
		t.Fatalf("by id = %+v", list)
	}

	// Uses: upsert keeps the first creator; listing per scope; counting.
	use := ContentUse{TenantID: tid, ScopeKind: ContentUseRoom, ScopeID: prefix + "-room", ComponentID: c.ID, Kind: c.Kind, PinnedVersion: "1.0.0", CreatedBy: "maya", CreatedAtMs: 20}
	if err := s.PutContentUse(ctx, use); err != nil {
		t.Fatal(err)
	}
	use.PinnedVersion, use.CreatedBy = "", "jack"
	if err := s.PutContentUse(ctx, use); err != nil {
		t.Fatal(err)
	}
	if err := s.PutContentUse(ctx, ContentUse{TenantID: tid, ScopeKind: ContentUseMember, ScopeID: "maya", ComponentID: c.ID, Kind: c.Kind, CreatedBy: "maya", CreatedAtMs: 21}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutContentUse(ctx, ContentUse{TenantID: tid, ScopeKind: ContentUseMember, ScopeID: "maya", ComponentID: prefix + "-none", CreatedBy: "maya"}); !errors.Is(err, ErrContentNotFound) {
		t.Fatalf("use of a missing component = %v", err)
	}
	// A built-in component has no row; its use is stored like any other
	// (the built-in overlay vouches for it).
	builtinUse := ContentUse{TenantID: tid, ScopeKind: ContentUseRoom, ScopeID: prefix + "-workshop", ComponentID: BuiltinContentIDPrefix + "expert-builder", Kind: "bnbloc.expert.package/v1", CreatedBy: "maya", CreatedAtMs: 22}
	for i := 0; i < 2; i++ {
		if err := s.PutContentUse(ctx, builtinUse); err != nil {
			t.Fatalf("builtin use %d: %v", i, err)
		}
	}
	if uses, err := s.ListContentUses(ctx, ContentUseRoom, prefix+"-workshop"); err != nil || len(uses) != 1 || uses[0].ComponentID != builtinUse.ComponentID {
		t.Fatalf("builtin uses = %+v, %v", uses, err)
	}
	if err := s.DeleteContentUse(ctx, ContentUseRoom, prefix+"-workshop", builtinUse.ComponentID); err != nil {
		t.Fatal(err)
	}
	uses, err := s.ListContentUses(ctx, ContentUseRoom, prefix+"-room")
	if err != nil || len(uses) != 1 || uses[0].PinnedVersion != "" || uses[0].CreatedBy != "maya" || uses[0].Kind != c.Kind {
		t.Fatalf("uses = %+v, %v", uses, err)
	}
	counts, err := s.CountContentUses(ctx, []string{c.ID, jack.ID})
	if err != nil || counts[c.ID] != 2 || counts[jack.ID] != 0 {
		t.Fatalf("counts = %+v, %v", counts, err)
	}
	if err := s.DeleteContentUse(ctx, ContentUseRoom, prefix+"-room", c.ID); err != nil {
		t.Fatal(err)
	}
	if uses, _ := s.ListContentUses(ctx, ContentUseRoom, prefix+"-room"); len(uses) != 0 {
		t.Fatalf("after delete = %+v", uses)
	}

	// Acks are per (subject, component, version).
	if err := s.PutContentAck(ctx, ContentAck{Subject: "jack", ComponentID: c.ID, Version: "1.0.1", AckedAtMs: 30}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutContentAck(ctx, ContentAck{Subject: "jack", ComponentID: c.ID, Version: "1.0.1", AckedAtMs: 31}); err != nil {
		t.Fatal(err)
	}
	acks, err := s.ListContentAcks(ctx, "jack", []string{c.ID})
	if err != nil || len(acks) != 1 || acks[0].AckedAtMs != 30 {
		t.Fatalf("acks = %+v, %v", acks, err)
	}

	// Delete removes the component, its versions, uses and acks.
	if err := s.DeleteContentComponent(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetContentComponent(ctx, c.ID); !errors.Is(err, ErrContentNotFound) {
		t.Fatalf("after delete = %v", err)
	}
	if vs, _ := s.ListContentComponentVersions(ctx, c.ID); len(vs) != 0 {
		t.Fatalf("versions after delete = %+v", vs)
	}
	if uses, _ := s.ListContentUses(ctx, ContentUseMember, "maya"); len(uses) != 0 {
		t.Fatalf("uses after delete = %+v", uses)
	}
	if acks, _ := s.ListContentAcks(ctx, "jack", []string{c.ID}); len(acks) != 0 {
		t.Fatalf("acks after delete = %+v", acks)
	}
}

// A content bundle stays referenced (blob GC keeps it) while a version
// names it.
func TestMemoryRetentionKeepsContentBundles(t *testing.T) {
	b, err := NewBackends("")
	if err != nil {
		t.Fatal(err)
	}
	key := "sha256/" + strings.Repeat("e", 64)
	c := ContentComponent{ID: "cc-1", Kind: "k", Name: "n", Scope: ContentScopePersonal, Status: ContentStatusActive}
	if err := b.ContentComponents.CreateContentComponent(context.Background(), c, contentVersion("cc-1", "1.0.0", key, 1)); err != nil {
		t.Fatal(err)
	}
	refs, err := NewRetention(b).ReferencedBlobKeys(context.Background(), []string{key, "sha256/" + strings.Repeat("f", 64)})
	if err != nil || !refs[key] || len(refs) != 1 {
		t.Fatalf("refs = %+v, %v", refs, err)
	}
}
