package contentuse

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// put stores a component with one version per manifest (each manifest
// carries its version).
func (f fixture) put(id, kind, name, owner string, manifests ...map[string]any) {
	f.t.Helper()
	ctx := context.Background()
	c := store.ContentComponent{ID: id, Kind: kind, Name: name, TenantID: "t1", Owner: owner, Scope: store.ContentScopePersonal, Status: store.ContentStatusActive}
	prev := ""
	for _, m := range manifests {
		m["kind"], m["name"] = kind, name
		raw, _ := json.Marshal(m)
		v := m["version"].(string)
		ver := store.ContentComponentVersion{Version: v, ManifestJSON: raw, BundleBlobKey: "sha256/" + id + "-" + v, SHA256: id + v}
		var err error
		if prev == "" {
			err = f.st.CreateContentComponent(ctx, c, ver)
		} else {
			err = f.st.AddContentComponentVersion(ctx, c, prev, ver)
		}
		if err != nil {
			f.t.Fatal(err)
		}
		prev = v
	}
}

func teamManifest(members ...map[string]any) map[string]any {
	return map[string]any{"version": "1.0.0", "displayName": "Release team", "team": map[string]any{"lead": "coordinator", "members": members}}
}

// A team's roles resolve as the requester: the highest version satisfying
// the range among their own experts and the room's pinned ones; an expert
// they may not use, or one that is itself a team, is reported per role.
func TestEffectiveResolvesTeam(t *testing.T) {
	f := fixture{t, store.NewMemoryContentComponents()}
	f.put("rev", contentpkg.KindExpert, "code-reviewer", "maya",
		map[string]any{"version": "1.0.0", "displayName": "Code reviewer", "description": "Reviews diffs."},
		map[string]any{"version": "1.3.0", "displayName": "Code reviewer", "description": "Reviews diffs carefully."},
		map[string]any{"version": "2.0.0"})
	f.put("tst", contentpkg.KindExpert, "tester", "jack", map[string]any{"version": "1.1.0", "displayName": "Tester"}) // jack's: maya gets it via the room
	f.put("doc", contentpkg.KindExpert, "doc-writer", "kim", map[string]any{"version": "1.0.0"})                       // kim's: not usable
	f.put("sub", contentpkg.KindExpert, "sub-team", "maya", teamManifest(map[string]any{"role": "x", "expert": "tester", "defaultAgent": "platform"}))
	f.put("team", contentpkg.KindExpert, "release-team", "maya", teamManifest(
		map[string]any{"role": "reviewer", "expert": "code-reviewer@^1", "defaultAgent": "requester"},
		map[string]any{"role": "tester", "expert": "tester@^1", "defaultAgent": "member:jack", "description": "Runs the tests."},
		map[string]any{"role": "writer", "expert": "doc-writer", "defaultAgent": "platform"},
		map[string]any{"role": "nested", "expert": "sub-team", "defaultAgent": "platform"},
	))
	f.use(store.ContentUseRoom, "room-1", "tst", contentpkg.KindExpert, "")

	job := store.AgentJob{JobID: "j1", TenantID: "t1", RoomID: "room-1", RequesterUserID: "maya", OwnerUserID: "maya", ExpertID: "team", ExpertVersion: "1.0.0"}
	e, err := Effective(context.Background(), f.st, job)
	if err != nil {
		t.Fatal(err)
	}
	if e.Expert == nil || e.Expert.ComponentID != "team" || e.Team == nil || e.Team.Lead != "coordinator" || len(e.Team.Members) != 4 {
		t.Fatalf("effective = %+v team %+v", e.Expert, e.Team)
	}
	m := e.Team.Members
	if m[0].Resolved == nil || m[0].Resolved.ComponentID != "rev" || m[0].Resolved.Version != "1.3.0" || m[0].Resolved.Description != "Reviews diffs carefully." {
		t.Fatalf("reviewer = %+v", m[0].Resolved)
	}
	if m[1].Resolved == nil || m[1].Resolved.ComponentID != "tst" || m[1].Description != "Runs the tests." || m[1].DefaultAgent != "member:jack" {
		t.Fatalf("tester = %+v", m[1])
	}
	if m[2].Resolved != nil || m[2].Missing == "" {
		t.Fatalf("writer = %+v", m[2])
	}
	if m[3].Resolved != nil || m[3].Missing != "the expert sub-team is itself a team (teams do not nest)" {
		t.Fatalf("nested = %+v", m[3])
	}
	// Member experts are not the lead's to wear: no bundle is granted.
	if keys := BundleKeys(e); keys["sha256/rev-1.3.0"] || !keys["sha256/team-1.0.0"] {
		t.Fatalf("keys = %+v", keys)
	}
}

// The room's expert is worn under the chat's own (with what it requires),
// is left out when it is the chat's expert, and is reported missing once
// the room no longer pins it.
func TestEffectiveRoomExpert(t *testing.T) {
	f := fixture{t, store.NewMemoryContentComponents()}
	f.put("house", contentpkg.KindExpert, "house-style", "jack",
		map[string]any{"version": "1.0.0", "requires": map[string]any{"skills": []string{"tone@^1"}}},
		map[string]any{"version": "1.1.0"})
	f.put("tone", contentpkg.KindSkill, "tone", "jack", map[string]any{"version": "1.0.0", "skills": []string{"tone"}})
	f.put("rev", contentpkg.KindExpert, "reviewer", "maya", map[string]any{"version": "1.0.0"})
	f.use(store.ContentUseRoom, "room-1", "house", contentpkg.KindExpert, "1.0.0")
	f.use(store.ContentUseRoom, "room-1", "tone", contentpkg.KindSkill, "")
	f.use(store.ContentUseRoomExpert, "room-1", "house", contentpkg.KindExpert, "")

	job := store.AgentJob{JobID: "j1", TenantID: "t1", RoomID: "room-1", RequesterUserID: "maya", OwnerUserID: "maya", ExpertID: "rev", ExpertVersion: "1.0.0"}
	e, err := Effective(context.Background(), f.st, job)
	if err != nil {
		t.Fatal(err)
	}
	if e.Expert == nil || e.Expert.ComponentID != "rev" || e.RoomExpert == nil || e.RoomExpert.ComponentID != "house" || e.RoomExpert.Version != "1.0.0" || e.RoomExpert.Source != wire.ContentSourceRoomExpert {
		t.Fatalf("experts = %+v / %+v", e.Expert, e.RoomExpert)
	}
	if got := refs(e.SkillPacks); got != "tone@1.0.0:expert-requires" {
		t.Fatalf("packs = %s", got)
	}
	if keys := BundleKeys(e); !keys["sha256/house-1.0.0"] {
		t.Fatalf("room expert bundle not granted: %+v", keys)
	}

	// No chat expert: the room expert alone.
	job.ExpertID, job.ExpertVersion = "", ""
	if e, _ = Effective(context.Background(), f.st, job); e.Expert != nil || e.RoomExpert == nil {
		t.Fatalf("room expert alone = %+v / %+v", e.Expert, e.RoomExpert)
	}
	// The chat picked the room expert itself: worn once.
	job.ExpertID, job.ExpertVersion = "house", "1.0.0"
	if e, _ = Effective(context.Background(), f.st, job); e.Expert == nil || e.RoomExpert != nil {
		t.Fatalf("same expert = %+v / %+v", e.Expert, e.RoomExpert)
	}
	// Unpinned (and not maya's own): reported missing.
	job.ExpertID, job.ExpertVersion = "", ""
	if err := f.st.DeleteContentUse(context.Background(), store.ContentUseRoom, "room-1", "house"); err != nil {
		t.Fatal(err)
	}
	if e, _ = Effective(context.Background(), f.st, job); e.RoomExpert != nil || len(e.Missing) != 1 || e.Missing[0] != "room expert house-style" {
		t.Fatalf("unpinned = %+v missing %v", e.RoomExpert, e.Missing)
	}
}
