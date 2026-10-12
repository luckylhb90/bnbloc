package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// exerciseMemberUses checks member-scope use counts and that a room expert
// is not counted as a second use of the room.
func exerciseMemberUses(t *testing.T, s ContentComponentStore, prefix string) {
	t.Helper()
	ctx := context.Background()
	pack, expert := prefix+"-pack", prefix+"-expert"
	for _, c := range []ContentComponent{
		{ID: pack, Kind: "bnbloc.skill.package/v1", Name: "kit"},
		{ID: expert, Kind: "bnbloc.expert.package/v1", Name: "reviewer"},
	} {
		c.TenantID, c.Owner, c.Scope, c.Status = prefix+"-t", "maya", ContentScopePersonal, ContentStatusActive
		if err := s.CreateContentComponent(ctx, c, contentVersion(c.ID, "1.0.0", "sha256/"+strings.Repeat("c", 64), 1)); err != nil {
			t.Fatalf("create %s: %v", c.ID, err)
		}
	}
	for _, u := range []ContentUse{
		{ScopeKind: ContentUseMember, ScopeID: "maya", ComponentID: pack, Kind: "bnbloc.skill.package/v1"},
		{ScopeKind: ContentUseMember, ScopeID: "bob", ComponentID: pack, Kind: "bnbloc.skill.package/v1"},
		{ScopeKind: ContentUseRoom, ScopeID: "room-1", ComponentID: pack, Kind: "bnbloc.skill.package/v1"},
		{ScopeKind: ContentUseRoom, ScopeID: "room-1", ComponentID: expert, Kind: "bnbloc.expert.package/v1"},
		{ScopeKind: ContentUseRoomExpert, ScopeID: "room-1", ComponentID: expert, Kind: "bnbloc.expert.package/v1"},
	} {
		u.CreatedBy, u.CreatedAtMs = "maya", 1
		if err := s.PutContentUse(ctx, u); err != nil {
			t.Fatalf("put use: %v", err)
		}
	}
	members, err := s.CountMemberUses(ctx, []string{pack, expert})
	if err != nil || members[pack] != 2 || members[expert] != 0 {
		t.Fatalf("member uses = %+v, %v", members, err)
	}
	uses, err := s.CountContentUses(ctx, []string{pack, expert})
	if err != nil || uses[pack] != 3 || uses[expert] != 1 {
		t.Fatalf("uses = %+v, %v; the room expert is not a second use", uses, err)
	}
	re, err := s.ListContentUses(ctx, ContentUseRoomExpert, "room-1")
	if err != nil || len(re) != 1 || re[0].ComponentID != expert {
		t.Fatalf("room expert = %+v, %v", re, err)
	}
}

func TestMemoryMemberUses(t *testing.T) {
	exerciseMemberUses(t, NewMemoryContentComponents(), "cc-mem")
}

func TestMySQLMemberUses(t *testing.T) {
	dsn := mysqlTestDSN(t)
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	prefix := "cc-use-" + itoaBase(time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = b.DB.Exec(`DELETE FROM content_component_uses WHERE component_id LIKE ?`, prefix+"%")
		_, _ = b.DB.Exec(`DELETE FROM content_component_versions WHERE component_id LIKE ?`, prefix+"%")
		_, _ = b.DB.Exec(`DELETE FROM content_components WHERE id LIKE ?`, prefix+"%")
		_ = b.DB.Close()
	})
	exerciseMemberUses(t, b.ContentComponents, prefix)
}
