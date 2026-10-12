package contentuse

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// scoped stores a component of another scope (a copy, an org publication,
// platform content) with one version.
func (f fixture) scoped(c store.ContentComponent, manifest map[string]any) {
	f.t.Helper()
	manifest["kind"], manifest["name"] = c.Kind, c.Name
	raw, _ := json.Marshal(manifest)
	v := manifest["version"].(string)
	if c.Status == "" {
		c.Status = store.ContentStatusActive
	}
	ver := store.ContentComponentVersion{Version: v, ManifestJSON: raw, BundleBlobKey: "sha256/" + c.ID + "-" + v, SHA256: c.ID + v}
	if err := f.st.CreateContentComponent(context.Background(), c, ver); err != nil {
		f.t.Fatal(err)
	}
}

// A shared copy is held by the recipient but is its author's content: the
// bridge sees the author as its owner, so the copy's scripts still force
// the sandbox on the recipient's own agent.
func TestRefOwnerIsTheAuthorOfACopy(t *testing.T) {
	f := fixture{t: t, st: store.NewMemoryContentComponents()}
	f.scoped(store.ContentComponent{ID: "copy", Kind: contentpkg.KindExpert, Name: "reviewer", TenantID: "t1", Owner: "jack",
		Scope: store.ContentScopeShared, SourceID: "src", SharedBy: "maya"},
		map[string]any{"version": "1.0.0", "requires": map[string]any{"permissions": []string{"scripts"}}})
	job := store.AgentJob{JobID: "j1", TenantID: "t1", RoomID: "room-1", RequesterUserID: "jack", OwnerUserID: "jack", ExpertID: "copy", ExpertVersion: "1.0.0"}
	e, err := Effective(context.Background(), f.st, job)
	if err != nil {
		t.Fatal(err)
	}
	if e.Expert == nil || e.Expert.Owner != "maya" || !e.Expert.NeedsSandbox {
		t.Fatalf("expert ref = %+v", e.Expert)
	}
}

// Visible is the Library: own (personal and held copies), the org's, the
// platform's; never another member's personal content, a retired org row
// or another organization's.
func TestVisible(t *testing.T) {
	f := fixture{t: t, st: store.NewMemoryContentComponents()}
	f.put("mine", contentpkg.KindSkill, "kit", "maya", map[string]any{"version": "1.0.0"})
	f.put("jacks", contentpkg.KindSkill, "kit", "jack", map[string]any{"version": "1.0.0"})
	f.scoped(store.ContentComponent{ID: "copy", Kind: contentpkg.KindSkill, Name: "kit", TenantID: "t1", Owner: "maya", Scope: store.ContentScopeShared, SourceID: "jacks", SharedBy: "jack"}, map[string]any{"version": "1.0.0"})
	f.scoped(store.ContentComponent{ID: "org", Kind: contentpkg.KindSkill, Name: "kit", TenantID: "t1", Owner: "jack", Scope: store.ContentScopeOrg, SourceID: "jacks"}, map[string]any{"version": "1.0.0"})
	f.scoped(store.ContentComponent{ID: "gone", Kind: contentpkg.KindSkill, Name: "old", TenantID: "t1", Owner: "jack", Scope: store.ContentScopeOrg, SourceID: "x", Status: store.ContentStatusRetired}, map[string]any{"version": "1.0.0"})
	f.scoped(store.ContentComponent{ID: "other-org", Kind: contentpkg.KindSkill, Name: "kit", TenantID: "t2", Owner: "olga", Scope: store.ContentScopeOrg, SourceID: "y"}, map[string]any{"version": "1.0.0"})
	f.scoped(store.ContentComponent{ID: "plat", Kind: contentpkg.KindSkill, Name: "kit", Scope: store.ContentScopePlatform}, map[string]any{"version": "3.0.0"})
	got, err := Visible(context.Background(), f.st, viewer("maya"), contentpkg.KindSkill)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, c := range got {
		ids[c.ID] = true
	}
	if len(got) != 4 || !ids["mine"] || !ids["copy"] || !ids["org"] || !ids["plat"] {
		t.Fatalf("visible = %+v", ids)
	}
	// A requirement resolves among them: the platform's 3.0.0 is the
	// highest kit@>=1.
	c, v, ok, err := resolveReference(context.Background(), f.st, viewer("maya"), "t1", nil, "kit@>=1", contentpkg.KindSkill)
	if err != nil || !ok || c.ID != "plat" || v.Version != "3.0.0" {
		t.Fatalf("resolve = %s %s %v %v", c.ID, v.Version, ok, err)
	}
}

// Prompt search finds prompts by title, body, tag or command in the packs
// the viewer can see.
func TestSearchPrompts(t *testing.T) {
	f := fixture{t: t, st: store.NewMemoryContentComponents()}
	p := func(id, title, body string, tags ...string) map[string]any {
		return map[string]any{"id": id, "title": title, "body": body, "tags": tags}
	}
	f.put("mine", contentpkg.KindPrompt, "mine", "maya", promptPack("1.0.0", p("a", "Release notes", "Write release notes"), p("b", "Standup", "Summarize", "daily")))
	f.put("jacks", contentpkg.KindPrompt, "jacks", "jack", promptPack("1.0.0", p("c", "Release plan", "secret")))
	f.scoped(store.ContentComponent{ID: "org", Kind: contentpkg.KindPrompt, Name: "jacks", TenantID: "t1", Owner: "jack", Scope: store.ContentScopeOrg, SourceID: "jacks", UpdatedAtMs: 9},
		promptPack("1.0.0", p("c", "Release plan", "Plan the release")))
	hits, err := SearchPrompts(context.Background(), f.st, viewer("maya"), "RELEASE", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].Component.ID != "org" || hits[0].Prompt.ID != "c" || hits[1].Prompt.ID != "a" {
		t.Fatalf("hits = %+v", hits)
	}
	if hits, _ := SearchPrompts(context.Background(), f.st, viewer("maya"), "daily", 10); len(hits) != 1 || hits[0].Prompt.ID != "b" {
		t.Fatalf("tag hit = %+v", hits)
	}
	if hits, _ := SearchPrompts(context.Background(), f.st, viewer("maya"), "release", 1); len(hits) != 1 {
		t.Fatalf("limit = %+v", hits)
	}
}
