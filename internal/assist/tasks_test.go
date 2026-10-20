package assist

import (
	"context"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A task resolves nearest first: the member's own, their organization's,
// the platform's; another member's or organization's never.
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

	// A task that explains a consent (explain-source-use) resolves to the
	// organization's, then the platform's: a member's own copy, or one
	// shared with them, never answers it, in a run or in the status.
	consent := wire.AssistTaskExplainSourceUse
	put("pc", consent, store.ContentScopePlatform, "", "", active)
	put("mc", consent, store.ContentScopePersonal, "acme", "maya", active)
	if got := name(maya, consent); got != "pc" {
		t.Errorf("maya's %s = %q, want the platform's (never her own)", consent, got)
	}
	put("oc", consent, store.ContentScopeOrg, "acme", "ada", active)
	if got := name(maya, consent); got != "oc" {
		t.Errorf("maya's %s = %q, want the organization's", consent, got)
	}
	list, _ = Tasks(ctx, st, maya)
	for _, c := range list {
		if c.Name == consent && c.ID != "oc" {
			t.Errorf("the status lists %s as %s", consent, c.ID)
		}
	}
}

// The library: the experts and skill packs the member may use, one per
// name (the nearest), their own first; others' never.
func TestLibraryOf(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryContentComponents()
	put := func(id, kind, name, display string, scope store.ContentScope, tenant, owner string) {
		t.Helper()
		c := store.ContentComponent{ID: id, Kind: kind, Name: name, DisplayName: display, Description: "about " + name, TenantID: tenant, Owner: owner, Scope: scope, Status: store.ContentStatusActive, LatestVersion: "1.0.0"}
		if err := st.CreateContentComponent(ctx, c, store.ContentComponentVersion{ComponentID: id, Version: "1.0.0", ManifestJSON: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	put("e1", contentpkg.KindExpert, "analyst", "Org analyst", store.ContentScopeOrg, "acme", "ada")
	put("e2", contentpkg.KindExpert, "analyst", "My analyst", store.ContentScopePersonal, "acme", "maya")
	put("e3", contentpkg.KindExpert, "writer", "Writer", store.ContentScopePlatform, "", "")
	put("e4", contentpkg.KindExpert, "spy", "Spy", store.ContentScopePersonal, "acme", "jack")
	put("s1", contentpkg.KindSkill, "brand-voice", "Brand voice", store.ContentScopeOrg, "acme", "ada")
	put("s2", contentpkg.KindSkill, "nimbus-only", "Nimbus", store.ContentScopeOrg, "nimbus", "kim")
	put("a1", contentpkg.KindAssist, "enhance-prompt", "Enhance", store.ContentScopePlatform, "", "")

	lib, err := LibraryOf(ctx, st, extpipe.Viewer{Subject: "maya", Tenant: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	names := func(items []LibraryItem) (out []string) {
		for _, it := range items {
			out = append(out, it.Name+"="+it.DisplayName)
		}
		return out
	}
	if got := strings.Join(names(lib.Experts), ","); got != "analyst=My analyst,writer=Writer" {
		t.Fatalf("experts = %s", got)
	}
	if got := strings.Join(names(lib.Skills), ","); got != "brand-voice=Brand voice" {
		t.Fatalf("skills = %s", got)
	}
}
