package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

func TestRoomHistoryScopedByChannel(t *testing.T) {
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	s := NewMemoryChat()
	msg := func(seq int64, ch string) RoomMessage {
		return RoomMessage{RoomID: "room-1", ChannelID: ch, Seq: seq, Kind: "chat_message", Author: "alice", Payload: json.RawMessage(`{"text":"x"}`), CreatedAtMs: seq}
	}
	for _, m := range []RoomMessage{msg(1, "ch_a"), msg(2, "ch_b"), msg(3, "ch_a")} {
		if err := s.AppendRoomMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.RoomHistory(ctx, "room-1", "ch_a", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 3 {
		t.Fatalf("channel history = %+v, want seq 1,3 for ch_a", got)
	}
	// max seq is room-scoped (single monotonic counter), not per channel
	if mx, _ := s.MaxRoomSeq(ctx, "room-1", ""); mx != 3 {
		t.Fatalf("max seq = %d, want 3 (room-scoped)", mx)
	}
	// read cursors are per channel
	if err := s.MarkRead(ctx, "room-1", "ch_a", "alice", 1, 10); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.UnreadCount(ctx, "room-1", "ch_a", "alice"); n != 1 {
		t.Fatalf("unread ch_a = %d, want 1", n)
	}
	if n, _ := s.UnreadCount(ctx, "room-1", "ch_b", "alice"); n != 1 {
		t.Fatalf("unread ch_b = %d, want 1 (independent cursor)", n)
	}
	// "" is the default channel, not a wildcard: it matches only messages
	// stored with an empty channel id (none here).
	if got, err := s.RoomHistory(ctx, "room-1", "", 0, 50); err != nil || len(got) != 0 {
		t.Fatalf("empty-channel history = %+v (err %v), want none", got, err)
	}
	// MarkRead is monotonic per channel: a stale cursor must not regress.
	if err := s.MarkRead(ctx, "room-1", "ch_a", "alice", 3, 11); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRead(ctx, "room-1", "ch_a", "alice", 1, 12); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.UnreadCount(ctx, "room-1", "ch_a", "alice"); n != 0 {
		t.Fatalf("unread ch_a = %d, want 0 after stale mark-read", n)
	}
}
