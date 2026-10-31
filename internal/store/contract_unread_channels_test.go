package store

import (
	"slices"
	"testing"
)

// UnreadChannels names, room by room, exactly the channels with an unread
// message: those UnreadCount counts, read and deleted messages left out,
// rooms of another tenant never.
func TestContractUnreadChannels(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		tid := uniq("t-unread-ch-")
		ctx := tenantCtx(tid)
		rooms := []string{newContractRoom(t, ctx, b, "maya"), newContractRoom(t, ctx, b, "maya"), newContractRoom(t, ctx, b, "maya")}
		// Channel ids are global: each room has its own a, b and c.
		chans := map[string][]string{}
		for _, room := range rooms {
			chans[room] = []string{uniq("a-"), uniq("b-"), uniq("c-")}
			for _, ch := range chans[room] {
				if err := b.Channel.PutChannel(ctx, RoomChannel{ChannelID: ch, RoomID: room, Name: ch, CreatedBy: "maya", CreatedAtMs: 1}); err != nil {
					t.Fatal(err)
				}
			}
		}
		// Room 0: news in a and b, b read. Room 1: news in c, deleted.
		// Room 2: nothing.
		for _, batch := range [][]RoomMessage{
			{chatRow(tid, rooms[0], chans[rooms[0]][0], 1, "one"), chatRow(tid, rooms[0], chans[rooms[0]][1], 2, "two")},
			{chatRow(tid, rooms[1], chans[rooms[1]][2], 1, "gone")},
		} {
			if err := b.Chat.AppendRoomMessages(ctx, batch); err != nil {
				t.Fatal(err)
			}
		}
		if err := b.Chat.MarkRead(ctx, rooms[0], chans[rooms[0]][1], "jack", 2, 10); err != nil {
			t.Fatal(err)
		}
		if _, err := b.Chat.DeleteRoomMessage(ctx, rooms[1], 1, "maya", 11); err != nil {
			t.Fatal(err)
		}
		got := must(b.Chat.UnreadChannels(ctx, "jack", rooms))(t)
		if len(got) != 1 || !slices.Equal(got[rooms[0]], chans[rooms[0]][:1]) {
			t.Fatalf("unread channels = %v, want only %s of the first room", got, chans[rooms[0]][0])
		}
		for _, room := range rooms {
			for _, ch := range chans[room] {
				n := must(b.Chat.UnreadCount(ctx, room, ch, "jack"))(t)
				if named := slices.Contains(got[room], ch); named != (n > 0) {
					t.Fatalf("room %s channel %s: named=%v, UnreadCount=%d", room, ch, named, n)
				}
			}
		}
		if got := must(b.Chat.UnreadChannels(tenantCtx(uniq("t-other-")), "jack", rooms))(t); len(got) != 0 {
			t.Fatalf("another tenant's read of the rooms = %v, want none", got)
		}
	})
}
