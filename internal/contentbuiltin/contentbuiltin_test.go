package contentbuiltin

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/contentuse"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/store"
)

func TestLoadRefusesInvalidPackage(t *testing.T) {
	bad := fstest.MapFS{
		"packages/broken/component.json": {Data: []byte(`{"kind":"bnbloc.expert.package/v1","name":"broken","version":"1.0.0","displayName":"Broken"}`)},
	}
	if _, err := load(bad); err == nil || !strings.Contains(err.Error(), "INSTRUCTIONS.md") {
		t.Fatalf("load = %v, want the validator's refusal", err)
	}
	misnamed := fstest.MapFS{
		"packages/other/component.json":  {Data: []byte(`{"kind":"bnbloc.expert.package/v1","name":"broken","version":"1.0.0","displayName":"Broken"}`)},
		"packages/other/INSTRUCTIONS.md": {Data: []byte("Do things.\n")},
	}
	if _, err := load(misnamed); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("load = %v, want the name/directory mismatch", err)
	}
}

// Junk a checkout or an editor leaves in the packages tree (Finder
// metadata, AppleDouble files, a stray metadata directory) is ignored: it
// must never fail boot or change a built-in's bytes.
func TestLoadIgnoresJunkFiles(t *testing.T) {
	pkg := fstest.MapFS{
		"packages/helper/component.json":  {Data: []byte(`{"kind":"bnbloc.expert.package/v1","name":"helper","version":"1.0.0","displayName":"Helper"}`)},
		"packages/helper/INSTRUCTIONS.md": {Data: []byte("Do things.\n")},
	}
	clean, err := load(pkg)
	if err != nil {
		t.Fatal(err)
	}
	junk := fstest.MapFS{
		"packages/.DS_Store":                {Data: []byte("x")},
		"packages/__MACOSX/helper/._x":      {Data: []byte("x")},
		"packages/.idea/workspace.xml":      {Data: []byte("x")},
		"packages/helper/.DS_Store":         {Data: []byte("\x00\x01binary")},
		"packages/helper/._INSTRUCTIONS.md": {Data: []byte("\x00\x05")},
		"packages/helper/Thumbs.db":         {Data: []byte("x")},
	}
	for k, v := range pkg {
		junk[k] = v
	}
	got, err := load(junk)
	if err != nil {
		t.Fatalf("load with junk files = %v", err)
	}
	if len(got) != 1 || got[IDPrefix+"helper"].Version.SHA256 != clean[IDPrefix+"helper"].Version.SHA256 {
		t.Fatalf("junk changed the built-ins: %d packages", len(got))
	}
}

func TestOverlayListsAndReadsBuiltins(t *testing.T) {
	ctx := context.Background()
	base := store.NewMemoryContentComponents()
	own := store.ContentComponent{ID: "cc_own", Kind: contentpkg.KindExpert, Name: "mine", TenantID: "t1", Owner: "maya", Scope: store.ContentScopePersonal, Status: store.ContentStatusActive, LatestVersion: "1.0.0"}
	if err := base.CreateContentComponent(ctx, own, store.ContentComponentVersion{Version: "1.0.0", ManifestJSON: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	st := Overlay(base)
	if Overlay(st) != st {
		t.Fatal("overlaying twice must not stack")
	}

	got, err := st.ListContentComponents(ctx, store.ContentComponentQuery{Kind: contentpkg.KindExpert, Scopes: []store.ContentScope{store.ContentScopeBuiltin}})
	var experts []string
	for _, p := range All() {
		if p.Component.Kind == contentpkg.KindExpert {
			experts = append(experts, p.Component.ID)
		}
	}
	var listed []string
	for _, c := range got {
		listed = append(listed, c.ID)
	}
	if err != nil || strings.Join(listed, ",") != strings.Join(experts, ",") || !slices.Contains(listed, BuilderID) {
		t.Fatalf("builtin experts listed = %v (want %v), %v", listed, experts, err)
	}
	// Skill-kind and owner/tenant-filtered queries do not list it.
	for _, q := range []store.ContentComponentQuery{
		{Kind: contentpkg.KindSkill, Scopes: []store.ContentScope{store.ContentScopeBuiltin}},
		{TenantID: "t1", Owner: "maya", Scopes: []store.ContentScope{store.ContentScopePersonal}},
		{TenantID: "t1", Kind: contentpkg.KindExpert},
		{Scopes: []store.ContentScope{store.ContentScopePlatform}},
	} {
		list, err := st.ListContentComponents(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range list {
			if c.ID == BuilderID {
				t.Fatalf("query %+v listed the builder", q)
			}
		}
	}
	// By id, mixed with stored ids.
	list, err := st.ListContentComponents(ctx, store.ContentComponentQuery{IDs: []string{BuilderID, "cc_own"}})
	if err != nil || len(list) != 2 {
		t.Fatalf("by ids = %+v, %v", list, err)
	}

	c, err := st.GetContentComponent(ctx, BuilderID)
	if err != nil || c.Scope != store.ContentScopeBuiltin {
		t.Fatalf("get = %+v, %v", c, err)
	}
	if _, err := st.GetContentComponent(ctx, IDPrefix+"nope"); !errors.Is(err, store.ErrContentNotFound) {
		t.Fatalf("missing builtin = %v", err)
	}
	vs, err := st.ListContentComponentVersions(ctx, BuilderID)
	if err != nil || len(vs) != 1 {
		t.Fatalf("versions = %+v, %v", vs, err)
	}
	if _, err := st.GetContentComponentVersion(ctx, BuilderID, "0.0.1"); !errors.Is(err, store.ErrContentNotFound) {
		t.Fatalf("unknown version = %v", err)
	}
	byBlob, err := st.ContentComponentsByBlob(ctx, store.ContentBlobQuery{Key: vs[0].BundleBlobKey})
	if err != nil || len(byBlob) != 1 || byBlob[0].ID != BuilderID {
		t.Fatalf("by blob = %+v, %v", byBlob, err)
	}

	// Uses of a built-in are stored; of an unknown built-in id, refused.
	if err := st.PutContentUse(ctx, store.ContentUse{ScopeKind: store.ContentUseRoom, ScopeID: "r1", ComponentID: BuilderID, Kind: c.Kind}); err != nil {
		t.Fatalf("pin builtin = %v", err)
	}
	if err := st.PutContentUse(ctx, store.ContentUse{ScopeKind: store.ContentUseRoom, ScopeID: "r1", ComponentID: IDPrefix + "nope"}); !errors.Is(err, store.ErrContentNotFound) {
		t.Fatalf("pin unknown builtin = %v", err)
	}
	// So are a built-in pack's prompt uses, counted per organization.
	starters := IDPrefix + "starters"
	if err := st.RecordContentPromptUse(ctx, "org-a", starters, "p1", 1); err != nil {
		t.Fatalf("count a built-in prompt use = %v", err)
	}
	if got, err := st.ContentPromptUses(ctx, "org-a", []string{starters}); err != nil || got[starters]["p1"] != 1 {
		t.Fatalf("built-in prompt uses = %+v, %v", got, err)
	}
	if err := st.RecordContentPromptUse(ctx, "org-a", IDPrefix+"nope", "p1", 1); !errors.Is(err, store.ErrContentNotFound) {
		t.Fatalf("count an unknown built-in's prompt use = %v", err)
	}

	// Read-only.
	if err := st.DeleteContentComponent(ctx, BuilderID); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("delete = %v", err)
	}
	if err := st.AddContentComponentVersion(ctx, c, c.LatestVersion, store.ContentComponentVersion{Version: "9.0.0"}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("new version = %v", err)
	}
	if err := st.CreateContentComponent(ctx, store.ContentComponent{ID: IDPrefix + "x"}, store.ContentComponentVersion{}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("create = %v", err)
	}
}

// Every member sees and may use built-in content, through the one
// visibility function; nobody owns it.
func TestBuiltinVisibleToEveryMember(t *testing.T) {
	c, _ := Get(BuilderID)
	g := extpipe.GateForContent(c.Component)
	for _, v := range []extpipe.Viewer{{Subject: "maya", Tenant: "t1"}, {Subject: "kim", Tenant: "t9"}} {
		if got := extpipe.MemberVisibility(v, g, 0); !got.Visible || !got.Installed {
			t.Fatalf("%+v: %+v", v, got)
		}
	}
	if got := extpipe.MemberVisibility(extpipe.Viewer{Tenant: "t1"}, g, 0); got.Visible {
		t.Fatalf("anonymous viewer = %+v", got)
	}
}

// A chat may wear the builder, and a job wearing it resolves to the
// built-in bundle (the key the bridge downloads).
func TestBuiltinExpertResolvesForChatAndJob(t *testing.T) {
	ctx := context.Background()
	st := Overlay(store.NewMemoryContentComponents())
	maya := extpipe.Viewer{Subject: "maya", Tenant: "t1"}
	c, v, err := contentuse.Usable(ctx, st, maya, "r1", BuilderID, "")
	if err != nil || c.ID != BuilderID || v.Version == "" {
		t.Fatalf("usable = %+v %+v %v", c, v, err)
	}
	eff, err := contentuse.Effective(ctx, st, store.AgentJob{RoomID: "r1", TenantID: "t1", RequesterUserID: "maya", OwnerUserID: "jack", ExpertID: BuilderID, ExpertVersion: v.Version})
	if err != nil || eff.Expert == nil || eff.Expert.BundleKey != v.BundleBlobKey || len(eff.Missing) != 0 {
		t.Fatalf("effective = %+v, %v", eff, err)
	}
	if !contentuse.BundleKeys(eff)[v.BundleBlobKey] {
		t.Fatal("the job's bundle grant misses the built-in bundle")
	}
}
