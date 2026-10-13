package assist

import (
	"context"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// A task resolves nearest first: the member's own, their organization's,
// the platform's, the built-in; another member's or organization's never.
func TestTasksResolveNearestFirst(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryContentComponents()
	put := func(id, name string, scope store.ContentScope, tenant, owner string, status store.ContentStatus) {
		t.Helper()
		c := store.ContentComponent{ID: id, Kind: contentpkg.KindAssist, Name: name, TenantID: tenant, Owner: owner, Scope: scope, Status: status, LatestVersion: "1.0.0"}
		if err := st.CreateContentComponent(ctx, c, store.ContentComponentVersion{ComponentID: id, Version: "1.0.0", ManifestJSON: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	active := store.ContentStatusActive
	put("p1", "enhance-prompt", store.ContentScopePlatform, "", "", active)
	put("o1", "enhance-prompt", store.ContentScopeOrg, "acme", "ada", active)
	put("o2", "describe-work", store.ContentScopeOrg, "acme", "ada", store.ContentStatusRetired)
	put("p2", "describe-work", store.ContentScopePlatform, "", "", active)
	put("m1", "enhance-prompt", store.ContentScopePersonal, "acme", "maya", active)
	put("x1", "polish", store.ContentScopeOrg, "nimbus", "kim", active)
	put("j1", "polish", store.ContentScopePersonal, "acme", "jack", active)

	name := func(v extpipe.Viewer, task string) string {
		c, ok, err := Task(ctx, st, v, task)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return ""
		}
		return c.ID
	}
	maya, jack := extpipe.Viewer{Subject: "maya", Tenant: "acme"}, extpipe.Viewer{Subject: "jack", Tenant: "acme"}
	if got := name(maya, "enhance-prompt"); got != "m1" {
		t.Errorf("maya's enhance-prompt = %q, want her own", got)
	}
	if got := name(jack, "enhance-prompt"); got != "o1" {
		t.Errorf("jack's enhance-prompt = %q, want the organization's", got)
	}
	if got := name(jack, "describe-work"); got != "p2" {
		t.Errorf("describe-work = %q, want the platform's (the org's is retired)", got)
	}
	if got := name(maya, "polish"); got != "" {
		t.Errorf("maya resolved %q: another member's or organization's assist", got)
	}
	list, _ := Tasks(ctx, st, jack)
	if len(list) != 3 || list[0].Name != "describe-work" || list[1].Name != "enhance-prompt" || list[2].ID != "j1" {
		t.Fatalf("jack's tasks = %+v", list)
	}
}
