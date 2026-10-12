package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMemoryContentSharing(t *testing.T) {
	testContentSharing(t, NewMemoryContentComponents(), "cs-mem")
}

func TestMySQLContentSharing(t *testing.T) {
	dsn := os.Getenv("BNBLOC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set BNBLOC_TEST_MYSQL_DSN to run the MySQL integration test")
	}
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	prefix := "cs-it-" + itoaBase(time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = b.DB.Exec(`DELETE FROM content_publish_requests WHERE component_id LIKE ?`, prefix+"%")
		_, _ = b.DB.Exec(`DELETE FROM content_component_versions WHERE component_id LIKE ?`, prefix+"%")
		_, _ = b.DB.Exec(`DELETE FROM content_components WHERE id LIKE ?`, prefix+"%")
		_ = b.DB.Close()
	})
	testContentSharing(t, b.ContentComponents, prefix)
}

// testContentSharing covers the step-4 store surface: copies keyed by
// their source, the author of a copy, status changes and publish requests
// (one pending per component, superseded by a newer one, decided once,
// reopened as the compensating step).
func testContentSharing(t *testing.T, s ContentComponentStore, prefix string) {
	ctx := context.Background()
	tid := prefix + "-t"
	blob := "sha256/" + strings.Repeat("c", 64)
	src := ContentComponent{ID: prefix + "-src", Kind: "bnbloc.expert.package/v1", Name: "reviewer", TenantID: tid, Owner: "maya",
		Scope: ContentScopePersonal, Status: ContentStatusActive, CreatedAtMs: 1, UpdatedAtMs: 1}
	if err := s.CreateContentComponent(ctx, src, contentVersion(src.ID, "1.0.0", blob, 1)); err != nil {
		t.Fatal(err)
	}
	// Two authors' same-named components reach one recipient as two copies.
	other := src
	other.ID, other.Owner = prefix+"-other", "olga"
	if err := s.CreateContentComponent(ctx, other, contentVersion(other.ID, "2.0.0", blob, 1)); err != nil {
		t.Fatal(err)
	}
	copyOf := func(id string, from ContentComponent) ContentComponent {
		return ContentComponent{ID: id, Kind: from.Kind, Name: from.Name, TenantID: tid, Owner: "jack", Scope: ContentScopeShared,
			Status: ContentStatusActive, SourceID: from.ID, SharedBy: from.Owner, CreatedAtMs: 2, UpdatedAtMs: 2}
	}
	c1 := copyOf(prefix+"-copy1", src)
	if err := s.CreateContentComponent(ctx, c1, contentVersion(c1.ID, "1.0.0", blob, 2)); err != nil {
		t.Fatal(err)
	}
	c2 := copyOf(prefix+"-copy2", other)
	if err := s.CreateContentComponent(ctx, c2, contentVersion(c2.ID, "2.0.0", blob, 2)); err != nil {
		t.Fatalf("second author's copy: %v", err)
	}
	dup := copyOf(prefix+"-copy3", src)
	if err := s.CreateContentComponent(ctx, dup, contentVersion(dup.ID, "1.0.0", blob, 2)); !errors.Is(err, ErrContentExists) {
		t.Fatalf("second copy of one source = %v", err)
	}
	got, err := s.GetContentComponent(ctx, c1.ID)
	if err != nil || got.SourceID != src.ID || got.SharedBy != "maya" || got.Author() != "maya" || got.Owner != "jack" {
		t.Fatalf("copy = %+v, %v", got, err)
	}
	if src.Author() != "maya" {
		t.Fatalf("original author = %q", src.Author())
	}
	list, err := s.ListContentComponents(ctx, ContentComponentQuery{SourceID: src.ID})
	if err != nil || len(list) != 1 || list[0].ID != c1.ID {
		t.Fatalf("copies of src = %+v, %v", list, err)
	}

	// Unpublish and restore.
	if err := s.SetContentComponentStatus(ctx, c1.ID, ContentStatusRetired, 9); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetContentComponent(ctx, c1.ID); got.Status != ContentStatusRetired || got.UpdatedAtMs != 9 {
		t.Fatalf("retired = %+v", got)
	}
	if err := s.SetContentComponentStatus(ctx, prefix+"-none", ContentStatusActive, 9); !errors.Is(err, ErrContentNotFound) {
		t.Fatalf("status of a missing component = %v", err)
	}

	// Requests: one pending per component, a newer one supersedes.
	r1, err := s.CreateContentRequest(ctx, ContentPublishRequest{ID: prefix + "-r1", TenantID: tid, ComponentID: src.ID, Kind: src.Kind,
		Name: src.Name, Version: "1.0.0", RequestedBy: "maya", Note: "please", CreatedAtMs: 10})
	if err != nil || r1.Status != ContentRequestPending {
		t.Fatalf("create = %+v, %v", r1, err)
	}
	r2, err := s.CreateContentRequest(ctx, ContentPublishRequest{ID: prefix + "-r2", TenantID: tid, ComponentID: src.ID, Kind: src.Kind,
		Name: src.Name, Version: "1.1.0", RequestedBy: "maya", CreatedAtMs: 11})
	if err != nil {
		t.Fatal(err)
	}
	if old, _ := s.GetContentRequest(ctx, r1.ID); old.Status != ContentRequestWithdrawn {
		t.Fatalf("superseded = %+v", old)
	}
	pending, err := s.ListContentRequests(ctx, ContentRequestQuery{TenantID: tid, Status: ContentRequestPending})
	if err != nil || len(pending) != 1 || pending[0].ID != r2.ID || pending[0].Version != "1.1.0" {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	all, err := s.ListContentRequests(ctx, ContentRequestQuery{ComponentID: src.ID})
	if err != nil || len(all) != 2 || all[0].ID != r2.ID {
		t.Fatalf("history newest first = %+v, %v", all, err)
	}

	// Another organization's admin cannot decide it.
	if _, err := s.DecideContentRequest(ctx, ContentRequestDecision{RequestID: r2.ID, TenantID: "elsewhere", Status: ContentRequestApproved, By: "eve", AtMs: 12}); !errors.Is(err, ErrContentRequestNotFound) {
		t.Fatalf("foreign decision = %v", err)
	}
	d, err := s.DecideContentRequest(ctx, ContentRequestDecision{RequestID: r2.ID, TenantID: tid, Status: ContentRequestApproved, By: "ada", Note: "ok", AtMs: 12})
	if err != nil || d.Status != ContentRequestApproved || d.DecidedBy != "ada" || d.DecisionNote != "ok" || d.DecidedAtMs != 12 {
		t.Fatalf("decide = %+v, %v", d, err)
	}
	if _, err := s.DecideContentRequest(ctx, ContentRequestDecision{RequestID: r2.ID, TenantID: tid, Status: ContentRequestDeclined, By: "ada", AtMs: 13}); !errors.Is(err, ErrContentRequestDecided) {
		t.Fatalf("second decision = %v", err)
	}
	// The compensating step reopens it.
	if err := s.ReopenContentRequest(ctx, r2.ID); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.GetContentRequest(ctx, r2.ID); again.Status != ContentRequestPending || again.DecidedBy != "" {
		t.Fatalf("reopened = %+v", again)
	}
	// A reopen never makes two pending requests.
	if _, err := s.CreateContentRequest(ctx, ContentPublishRequest{ID: prefix + "-r3", TenantID: tid, ComponentID: src.ID, Kind: src.Kind,
		Name: src.Name, Version: "1.2.0", RequestedBy: "maya", CreatedAtMs: 14}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReopenContentRequest(ctx, r2.ID); !errors.Is(err, ErrContentRequestDecided) {
		t.Fatalf("reopen beside a pending request = %v", err)
	}
	if _, err := s.GetContentRequest(ctx, prefix+"-none"); !errors.Is(err, ErrContentRequestNotFound) {
		t.Fatalf("missing request = %v", err)
	}

	// Deleting the component deletes its requests.
	if err := s.DeleteContentComponent(ctx, src.ID); err != nil {
		t.Fatal(err)
	}
	if left, _ := s.ListContentRequests(ctx, ContentRequestQuery{ComponentID: src.ID}); len(left) != 0 {
		t.Fatalf("requests after delete = %+v", left)
	}
}
