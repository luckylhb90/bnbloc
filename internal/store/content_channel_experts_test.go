package store

import (
	"context"
	"testing"
)

// A room's channel experts are the channel-expert uses scoped "<room>:<ch>"
// of that room only: a room whose id extends this one's ("room-1" and
// "room-10") is a second candidate the prefix must not reach, and a room
// expert of the same room is not a channel's (channels PRD C9).
func TestContractContentComponentStoreChannelExperts(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s := b.ContentComponents
		ctx := context.Background()
		prefix := uniq("cx-")
		tid := prefix + "-t"
		room, neighbour := prefix+"-room-1", prefix+"-room-10"
		for _, name := range []string{"review", "write", "other", "house"} {
			c := ContentComponent{ID: prefix + "-" + name, Kind: "bnbloc.expert.package/v1", Name: prefix + name, TenantID: tid, Owner: "maya",
				Scope: ContentScopePersonal, Status: ContentStatusActive, CreatedAtMs: 1, UpdatedAtMs: 1}
			if err := s.CreateContentComponent(ctx, c, contentVersion(c.ID, "1.0.0", uniqBlobKey(), 1)); err != nil {
				t.Fatal(err)
			}
		}
		put := func(kind ContentUseScope, scope, comp string) {
			t.Helper()
			if err := s.PutContentUse(ctx, ContentUse{TenantID: tid, ScopeKind: kind, ScopeID: scope, ComponentID: comp, Kind: "bnbloc.expert.package/v1", CreatedBy: "maya", CreatedAtMs: 1}); err != nil {
				t.Fatal(err)
			}
		}
		put(ContentUseChannelExpert, ChannelExpertScopeID(room, "ch-b"), prefix+"-review")
		put(ContentUseChannelExpert, ChannelExpertScopeID(room, "ch-a"), prefix+"-write")
		put(ContentUseChannelExpert, ChannelExpertScopeID(neighbour, "ch-a"), prefix+"-other")
		put(ContentUseRoomExpert, room, prefix+"-house")

		got, err := s.ChannelExperts(ctx, room)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].ScopeID != ChannelExpertScopeID(room, "ch-a") || got[0].ComponentID != prefix+"-write" ||
			got[1].ScopeID != ChannelExpertScopeID(room, "ch-b") || got[1].ComponentID != prefix+"-review" {
			t.Fatalf("channel experts = %+v", got)
		}
		if got, err := s.ChannelExperts(ctx, prefix+"-none"); err != nil || len(got) != 0 {
			t.Fatalf("a room without channel experts = %+v, %v", got, err)
		}
	})
}
