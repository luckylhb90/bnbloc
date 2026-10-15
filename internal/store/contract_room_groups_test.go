package store

import (
	"errors"
	"testing"
)

// A code binds once, before it expires, the group under its name in its
// room (replacing a group of that name); groups are listed and deleted per
// room and tenant, and go with the room.
func TestContractRoomGroupStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-grp-"))
		room := newContractRoom(t, ctx, b, "maya")
		code := uniq("c-")
		if err := b.RoomGroups.PutRoomGroupCode(ctx, RoomGroupCode{Code: code, RoomID: room, Name: "weekly", CreatedBy: "maya", ExpiresAtMs: 100}); err != nil {
			t.Fatal(err)
		}
		if n, err := b.RoomGroups.OpenRoomGroupCodes(ctx, room, 50); err != nil || n != 1 {
			t.Fatalf("open codes = %d, %v", n, err)
		}
		if _, err := b.RoomGroups.BindRoomGroup(ctx, code, "chat-bot", "g-1", 100); !errors.Is(err, ErrRoomGroupCode) {
			t.Fatalf("an expired code bound: %v", err)
		}
		code = uniq("c-")
		_ = b.RoomGroups.PutRoomGroupCode(ctx, RoomGroupCode{Code: code, RoomID: room, Name: "weekly", CreatedBy: "maya", ExpiresAtMs: 1000})
		g, err := b.RoomGroups.BindRoomGroup(tenantCtx("someone-else"), code, "chat-bot", "g-1", 50)
		if err != nil || g.RoomID != room || g.Name != "weekly" || g.Address != "g-1" || g.ConnectedBy != "maya" || g.TenantID == "someone-else" {
			t.Fatalf("bind = %+v, %v", g, err)
		}
		if _, err := b.RoomGroups.BindRoomGroup(ctx, code, "chat-bot", "g-2", 50); !errors.Is(err, ErrRoomGroupCode) {
			t.Fatalf("a spent code bound again: %v", err)
		}
		again := uniq("c-")
		_ = b.RoomGroups.PutRoomGroupCode(ctx, RoomGroupCode{Code: again, RoomID: room, Name: "weekly", CreatedBy: "jack", ExpiresAtMs: 1000})
		if _, err := b.RoomGroups.BindRoomGroup(ctx, again, "chat-bot", "g-9", 60); err != nil {
			t.Fatal(err)
		}
		if got, err := b.RoomGroups.GetRoomGroup(ctx, room, "weekly"); err != nil || got.Address != "g-9" || got.ConnectedBy != "jack" {
			t.Fatalf("rebound group = %+v, %v", got, err)
		}
		if list, _ := b.RoomGroups.ListRoomGroups(tenantCtx(uniq("t-x-")), room); len(list) != 0 {
			t.Fatalf("another tenant lists %v", list)
		}
		if list, _ := b.RoomGroups.ListRoomGroups(ctx, room); len(list) != 1 {
			t.Fatalf("list = %v", list)
		}
		if err := b.Room.DeleteRoom(ctx, room); err != nil {
			t.Fatal(err)
		}
		if _, err := b.RoomGroups.GetRoomGroup(ctx, room, "weekly"); !errors.Is(err, ErrRoomGroupNotFound) {
			t.Fatalf("a group outlived its room: %v", err)
		}
	})
}
