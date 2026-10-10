package store

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// handoffStores returns the in-memory backends plus, when
// BNBLOC_TEST_MYSQL_DSN is set, the MySQL ones, so the handoff store contract
// (RoomFeed, RoomImports, DeleteRoom cleanup) is checked against both.
func handoffStores(t *testing.T) map[string]*Backends {
	t.Helper()
	mem, err := NewBackends("")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*Backends{"memory": mem}
	if dsn := os.Getenv("BNBLOC_TEST_MYSQL_DSN"); dsn != "" {
		b, err := NewBackends(dsn)
		if err != nil {
			t.Fatalf("NewBackends: %v", err)
		}
		t.Cleanup(func() { _ = b.DB.Close() })
		out["mysql"] = b
	}
	return out
}

func TestRoomFeedAcrossChannelsExcludesDeleted(t *testing.T) {
	for name, b := range handoffStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := testCtx()
			roomID := "feed-" + itoaBase(time.Now().UnixNano())
			for i, ch := range []string{"ch_a", "ch_b", "", "ch_a"} {
				if err := b.Chat.AppendRoomMessage(ctx, RoomMessage{RoomID: roomID, ChannelID: ch, Seq: int64(i + 1), Kind: "chat_message",
					Author: "alice", Payload: json.RawMessage(`{"userId":"alice","text":"m"}`), CreatedAtMs: int64(100 + i)}); err != nil {
					t.Fatal(err)
				}
			}
			if ok, err := b.Chat.DeleteRoomMessage(ctx, roomID, 3, "alice", 5); err != nil || !ok {
				t.Fatalf("delete = %v %v", ok, err)
			}
			if _, err := b.Chat.EditRoomMessage(ctx, roomID, 4, "alice", "edited", 9); err != nil {
				t.Fatal(err)
			}
			page, err := b.Chat.RoomFeed(ctx, roomID, 0, 2)
			if err != nil || len(page) != 2 || page[0].Seq != 1 || page[1].Seq != 2 || page[1].ChannelID != "ch_b" {
				t.Fatalf("page 1 = %+v %v", page, err)
			}
			page, err = b.Chat.RoomFeed(ctx, roomID, 2, 10)
			if err != nil || len(page) != 1 || page[0].Seq != 4 || page[0].EditedAtMs == nil || page[0].CreatedAtMs != 103 {
				t.Fatalf("page 2 = %+v %v", page, err)
			}
			// Tenant-scoped: another tenant sees nothing.
			other, _ := b.Chat.RoomFeed(tenant.NewContext(ctx, "acme"), roomID, 0, 10)
			if len(other) != 0 {
				t.Fatalf("cross-tenant feed = %+v", other)
			}
		})
	}
}

func TestRoomImportsRoundTripAndRoomDelete(t *testing.T) {
	for name, b := range handoffStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := testCtx()
			now := time.Now().UnixMilli()
			roomID := "imp-" + itoaBase(time.Now().UnixNano())
			if _, ok, err := b.RoomImports.GetRoomImport(ctx, roomID); err != nil || ok {
				t.Fatalf("absent provenance = %v %v", ok, err)
			}
			info := wire.RoomHandoffInfo{
				RoomID: roomID, SourceRoomID: "src", SourceRoomName: "Source", SourceExportedAtMs: now - 10,
				SourceExportedBy: "alice", ImportedBy: "dave", ImportedAtMs: now,
				SuggestedMembers: []wire.RoomExportMember{{UserID: "alice", Role: "owner"}, {UserID: "bob", Role: "member"}},
			}
			if err := b.RoomImports.PutRoomImport(ctx, info); err != nil {
				t.Fatal(err)
			}
			got, ok, err := b.RoomImports.GetRoomImport(ctx, roomID)
			if err != nil || !ok || !reflect.DeepEqual(got, info) {
				t.Fatalf("provenance = %+v %v %v", got, ok, err)
			}
			if _, ok, _ := b.RoomImports.GetRoomImport(tenant.NewContext(ctx, "acme"), roomID); ok {
				t.Fatal("provenance visible across tenants")
			}
			if name != "mysql" {
				return
			}
			// The room's delete removes its provenance with every other row.
			if err := b.Room.CreateRoom(ctx, Room{RoomID: roomID, Name: "x", CreatedBy: "dave", CreatedAtMs: now}); err != nil {
				t.Fatal(err)
			}
			if err := b.Room.DeleteRoom(ctx, roomID); err != nil {
				t.Fatal(err)
			}
			if _, ok, _ := b.RoomImports.GetRoomImport(ctx, roomID); ok {
				t.Fatal("DeleteRoom left the room's import provenance behind")
			}
		})
	}
}
