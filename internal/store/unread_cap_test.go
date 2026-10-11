package store

import (
	"encoding/json"
	"strconv"
	"testing"
)

// UnreadCount stops at MaxUnreadCount however long the backlog: a member who
// skipped a busy room for months must not make every room-list refresh scan
// it all (docs/prd/2026-10-11-unread-cap-and-ordered-ids.md).
func TestUnreadCountIsCapped(t *testing.T) {
	for _, rig := range retentionRigs(t) {
		t.Run(rig.name, func(t *testing.T) {
			tid, room := uniq("acme-"), uniq("unread-cap-")
			ctx := tenantCtx(tid)
			payload, _ := json.Marshal(map[string]any{"text": "hi"})
			rows := make([]RoomMessage, 0, MaxUnreadCount+20)
			for seq := int64(1); seq <= MaxUnreadCount+20; seq++ {
				rows = append(rows, RoomMessage{TenantID: tid, RoomID: room, Seq: seq, Kind: "chat_message", Author: "jack",
					ClientMsgID: room + "-" + strconv.FormatInt(seq, 10), Payload: payload, CreatedAtMs: seq})
			}
			if err := rig.b.Chat.AppendRoomMessages(ctx, rows); err != nil {
				t.Fatal(err)
			}
			if n, err := rig.b.Chat.UnreadCount(ctx, room, "", "maya"); err != nil || n != MaxUnreadCount {
				t.Fatalf("backlog of %d = %d, %v; want the cap %d", len(rows), n, err, MaxUnreadCount)
			}
			if err := rig.b.Chat.MarkRead(ctx, room, "", "maya", MaxUnreadCount+15, 1); err != nil {
				t.Fatal(err)
			}
			if n, _ := rig.b.Chat.UnreadCount(ctx, room, "", "maya"); n != 5 {
				t.Fatalf("after reading up to the last 5 = %d, want 5", n)
			}
		})
	}
}
