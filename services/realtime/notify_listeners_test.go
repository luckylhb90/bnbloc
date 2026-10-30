package realtimehttp

import (
	"reflect"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Who listens to a channel (notify.Observer.Listeners): readers only (a
// members-only section's channel never lists a room member outside it),
// each at their effective level; @here adds who read it in the last hour;
// @channel every person who may read it, never an agent host.
func TestNotifyListeners(t *testing.T) {
	_, svc, ch := newMembersOnlyServer(t)
	svc.RoomRoles = schedRoles{rooms: map[string]bool{"room-1": true}, roles: map[string]store.Role{
		"room-1\x00alice": store.RoleOwner, "room-1\x00bob": store.RoleMember, "room-1\x00carl": store.RoleMember,
	}}
	ctx := dctx()
	all, muted := wire.NotifyLevelAll, wire.NotifyLevelMuted
	// alice posted in ch_a (all by default), bob muted it, carl only reads.
	if _, err := ch.ClaimChannelPost(ctx, "room-1", "ch_a", "alice", 10, 0); err != nil {
		t.Fatal(err)
	}
	if err := ch.FollowChannel(ctx, "room-1", "ch_a", "bob", 10, store.FollowMarks{Level: &muted}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Chat.MarkRead(ctx, "room-1", "ch_a", "carl", 1, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	listeners := func(channelID, reach string) []notify.Listener {
		t.Helper()
		ls, err := svc.notifyListeners(ctx, "room-1", channelID, reach)
		if err != nil {
			t.Fatal(err)
		}
		return ls
	}
	if got, want := listeners("ch_a", notify.ReachFollowers), []notify.Listener{
		{UserID: "alice", Level: all, Here: true}, {UserID: "bob", Level: muted, Here: true},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("followers = %+v, want %+v", got, want)
	}
	if got := listeners("ch_a", notify.ReachHere); len(got) != 3 || got[2] != (notify.Listener{UserID: "carl", Level: wire.NotifyLevelMentions, Here: true}) {
		t.Fatalf("here = %+v, want carl here by his read", got)
	}
	// ch_b is members-only (alice, carl): bob is no listener even by @channel.
	if err := ch.FollowChannel(ctx, "room-1", "ch_b", "bob", 10, store.FollowMarks{Level: &all}); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, l := range listeners("ch_b", notify.ReachChannel) {
		ids = append(ids, l.UserID)
	}
	if !reflect.DeepEqual(ids, []string{"alice", "carl"}) {
		t.Fatalf("@channel in a members-only channel = %v, want [alice carl]", ids)
	}
}
