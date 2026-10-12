package contentbuiltin

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/contentuse"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

func TestBuilderShipsAndValidates(t *testing.T) {
	p, ok := Get(BuilderID)
	if !ok {
		t.Fatalf("builder %s missing; have %d built-ins", BuilderID, len(All()))
	}
	if p.Component.Scope != store.ContentScopeBuiltin || p.Component.Kind != contentpkg.KindExpert || p.Component.Owner != "" {
		t.Fatalf("builder component = %+v", p.Component)
	}
	if p.Version.BundleBlobKey != BundleKeyPrefix+p.Version.SHA256 || p.Version.CreatedBy != Author {
		t.Fatalf("builder version = %+v", p.Version)
	}
	// The served bytes are the validated bundle, re-validated by the same
	// validator a bridge uses on download.
	raw, ok := Bundle(p.Version.BundleBlobKey)
	if !ok {
		t.Fatal("bundle not served by key")
	}
	pkg, err := contentpkg.Validate(raw, p.Version.SHA256, contentpkg.Options{})
	if err != nil {
		t.Fatalf("served bundle does not validate: %v", err)
	}
	if pkg.Manifest.NeedsSandbox() || len(pkg.Manifest.Permissions()) != 0 {
		t.Fatal("the builder must declare no permissions")
	}
	for _, want := range []string{"save_component", "get_component", "drafts/<name>/", "Save as", "Save as prompt pack", "bnbloc.prompt.package/v1", "starter", "中文"} {
		if !strings.Contains(pkg.Instructions, want) {
			t.Errorf("builder instructions do not mention %q", want)
		}
	}
	if _, ok := Bundle("builtin/0000"); ok {
		t.Fatal("unknown built-in key served")
	}
	if _, ok := Bundle(strings.TrimPrefix(p.Version.BundleBlobKey, BundleKeyPrefix)); ok {
		t.Fatal("a plain blob key must not resolve to a built-in bundle")
	}
}

// The builder's instructions carry example packages; they must validate with
// the one validator, or the builder teaches agents a format that fails on
// save.
func TestBuilderExamplesValidate(t *testing.T) {
	p, _ := Get(BuilderID)
	re := regexp.MustCompile("(?s)<!-- example: (\\S+) -->\\s*```[a-z]*\\n(.*?)\\n```")
	byPkg := map[string]map[string][]byte{}
	for _, m := range re.FindAllStringSubmatch(p.Validated.Instructions, -1) {
		parts := strings.SplitN(m[1], "/", 3) // drafts/<name>/<file>
		if len(parts) != 3 || parts[0] != "drafts" {
			t.Fatalf("example path %q is not drafts/<name>/<file>", m[1])
		}
		if byPkg[parts[1]] == nil {
			byPkg[parts[1]] = map[string][]byte{}
		}
		byPkg[parts[1]][parts[2]] = []byte(m[2] + "\n")
	}
	if len(byPkg) < 3 {
		t.Fatalf("found %d example packages, want an expert, a skill pack and a prompt pack", len(byPkg))
	}
	kinds := map[string]bool{}
	for name, files := range byPkg {
		raw, err := tarbundle.PackFiles(files, tarbundle.ContentLimits)
		if err != nil {
			t.Fatalf("%s: pack: %v", name, err)
		}
		pkg, err := contentpkg.Validate(raw, "", contentpkg.Options{})
		if err != nil {
			t.Fatalf("example %s does not validate: %v", name, err)
		}
		if pkg.Manifest.Name != name {
			t.Fatalf("example %s names %q", name, pkg.Manifest.Name)
		}
		kinds[pkg.Manifest.Kind] = true
	}
	if !kinds[contentpkg.KindExpert] || !kinds[contentpkg.KindSkill] || !kinds[contentpkg.KindPrompt] {
		t.Fatalf("example kinds = %v", kinds)
	}
}

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
	if err != nil || len(got) != 1 || got[0].ID != BuilderID {
		t.Fatalf("builtin listing = %+v, %v", got, err)
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
	byBlob, err := st.ContentVersionsByBlob(ctx, vs[0].BundleBlobKey)
	if err != nil || len(byBlob) != 1 || byBlob[0].ComponentID != BuilderID {
		t.Fatalf("by blob = %+v, %v", byBlob, err)
	}

	// Uses of a built-in are stored; of an unknown built-in id, refused.
	if err := st.PutContentUse(ctx, store.ContentUse{ScopeKind: store.ContentUseRoom, ScopeID: "r1", ComponentID: BuilderID, Kind: c.Kind}); err != nil {
		t.Fatalf("pin builtin = %v", err)
	}
	if err := st.PutContentUse(ctx, store.ContentUse{ScopeKind: store.ContentUseRoom, ScopeID: "r1", ComponentID: IDPrefix + "nope"}); !errors.Is(err, store.ErrContentNotFound) {
		t.Fatalf("pin unknown builtin = %v", err)
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
