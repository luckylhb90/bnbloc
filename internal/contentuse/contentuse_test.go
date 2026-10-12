package contentuse

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

type fixture struct {
	t  *testing.T
	st *store.MemoryContentComponents
}

func (f fixture) component(id, kind, name, owner string, versions ...string) {
	f.t.Helper()
	ctx := context.Background()
	c := store.ContentComponent{ID: id, Kind: kind, Name: name, TenantID: "t1", Owner: owner, Scope: store.ContentScopePersonal, Status: store.ContentStatusActive}
	man := func(v string, extra string) json.RawMessage {
		return json.RawMessage(`{"kind":"` + kind + `","name":"` + name + `","version":"` + v + `"` + extra + `}`)
	}
	for i, v := range versions {
		extra := ""
		if kind == contentpkg.KindExpert {
			extra = `,"requires":{"skills":["checklist@^1","style","absent@^2"],"permissions":["scripts"]}`
		}
		ver := store.ContentComponentVersion{Version: v, ManifestJSON: man(v, extra), BundleBlobKey: "sha256/" + id + "-" + v, SHA256: id + v}
		var err error
		if i == 0 {
			err = f.st.CreateContentComponent(ctx, c, ver)
		} else {
			err = f.st.AddContentComponentVersion(ctx, c, versions[i-1], ver)
		}
		if err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f fixture) use(scope store.ContentUseScope, scopeID, id, kind, pin string) {
	f.t.Helper()
	if err := f.st.PutContentUse(context.Background(), store.ContentUse{TenantID: "t1", ScopeKind: scope, ScopeID: scopeID, ComponentID: id, Kind: kind, PinnedVersion: pin, CreatedBy: "x"}); err != nil {
		f.t.Fatal(err)
	}
}

func refs(rs []wire.ContentRef) string {
	var out []string
	for _, r := range rs {
		out = append(out, r.ComponentID+"@"+r.Version+":"+r.Source)
	}
	return strings.Join(out, ",")
}

// The requester's expert, the skill packs it requires (highest satisfying
// version they may use, or the room's pinned one), the room's packs and the
// agent owner's packs — each once, first source wins.
func TestEffective(t *testing.T) {
	f := fixture{t, store.NewMemoryContentComponents()}
	f.component("ex", contentpkg.KindExpert, "reviewer", "maya", "1.0.0", "1.1.0")
	f.component("chk", contentpkg.KindSkill, "checklist", "maya", "1.0.0", "1.4.0", "2.0.0")
	f.component("sty", contentpkg.KindSkill, "style", "jack", "0.1.0")     // jack's: maya resolves it only via the room
	f.component("own", contentpkg.KindSkill, "owner-kit", "jack", "3.0.0") // the agent owner's
	f.component("hid", contentpkg.KindSkill, "hidden", "kim", "1.0.0")     // enabled for jack but kim's: not usable
	f.use(store.ContentUseRoom, "room-1", "sty", contentpkg.KindSkill, "")
	f.use(store.ContentUseMember, "jack", "own", contentpkg.KindSkill, "")
	f.use(store.ContentUseMember, "jack", "hid", contentpkg.KindSkill, "")
	f.use(store.ContentUseMember, "jack", "chk", contentpkg.KindSkill, "") // maya's pack, enabled by jack: not his

	job := store.AgentJob{JobID: "j1", TenantID: "t1", RoomID: "room-1", RequesterUserID: "maya", OwnerUserID: "jack", ExpertID: "ex", ExpertVersion: "1.0.0", ExpertName: "Reviewer"}
	e, err := Effective(context.Background(), f.st, job)
	if err != nil {
		t.Fatal(err)
	}
	if e.Expert == nil || e.Expert.Version != "1.0.0" || e.Expert.Kind != "expert" || !e.Expert.NeedsSandbox || e.Expert.Owner != "maya" {
		t.Fatalf("expert = %+v", e.Expert)
	}
	if got := refs(e.SkillPacks); got != "chk@1.4.0:expert-requires,sty@0.1.0:expert-requires,own@3.0.0:member" {
		t.Fatalf("packs = %s", got)
	}
	if len(e.Missing) != 1 || e.Missing[0] != "absent@^2" {
		t.Fatalf("missing = %+v", e.Missing)
	}
	keys := BundleKeys(e)
	if !keys["sha256/ex-1.0.0"] || !keys["sha256/chk-1.4.0"] || keys["sha256/hid-1.0.0"] || len(keys) != 4 {
		t.Fatalf("keys = %+v", keys)
	}

	// A requester who may not use the expert gets it reported missing; the
	// room and member packs still apply.
	job.RequesterUserID = "bob"
	e, err = Effective(context.Background(), f.st, job)
	if err != nil {
		t.Fatal(err)
	}
	if e.Expert != nil || len(e.Missing) != 1 || e.Missing[0] != "expert Reviewer" || refs(e.SkillPacks) != "sty@0.1.0:room,own@3.0.0:member" {
		t.Fatalf("bob = %+v %s", e, refs(e.SkillPacks))
	}

	// A room pin fixes the version of a pack a requirement resolves to.
	f.use(store.ContentUseRoom, "room-1", "chk", contentpkg.KindSkill, "1.0.0")
	job.RequesterUserID = "maya"
	e, _ = Effective(context.Background(), f.st, job)
	if got := refs(e.SkillPacks); got != "chk@1.4.0:expert-requires,sty@0.1.0:expert-requires,own@3.0.0:member" {
		// maya's own 1.4.0 outranks the room's pinned 1.0.0 for ^1.
		t.Fatalf("pinned packs = %s", got)
	}
}

func TestUsable(t *testing.T) {
	f := fixture{t, store.NewMemoryContentComponents()}
	f.component("ex", contentpkg.KindExpert, "reviewer", "maya", "1.0.0", "1.1.0")
	ctx := context.Background()
	if _, v, err := Usable(ctx, f.st, viewer("maya"), "room-1", "ex", ""); err != nil || v.Version != "1.1.0" {
		t.Fatalf("own latest = %+v, %v", v, err)
	}
	if _, _, err := Usable(ctx, f.st, viewer("jack"), "room-1", "ex", ""); err != ErrNotUsable {
		t.Fatalf("other = %v", err)
	}
	f.use(store.ContentUseRoom, "room-1", "ex", contentpkg.KindExpert, "1.0.0")
	if _, v, err := Usable(ctx, f.st, viewer("jack"), "room-1", "ex", ""); err != nil || v.Version != "1.0.0" {
		t.Fatalf("room pin = %+v, %v", v, err)
	}
	if _, _, err := Usable(ctx, f.st, viewer("jack"), "room-2", "ex", ""); err != ErrNotUsable {
		t.Fatalf("other room = %v", err)
	}
	if _, _, err := Usable(ctx, f.st, viewer("maya"), "", "missing", ""); err != ErrNotUsable {
		t.Fatalf("missing = %v", err)
	}
}

func viewer(s string) extpipe.Viewer { return extpipe.Viewer{Subject: s, Tenant: "t1"} }
