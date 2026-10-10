package store

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

// appendDupContract asserts the duplicate-insert contract shared by every
// ChatStore: a taken client message id and a taken (room, seq) are reported,
// never silently ignored, and the original row stays authoritative.
func appendDupContract(t *testing.T, s ChatStore, roomID string) {
	t.Helper()
	ctx := testCtx()
	msg := func(seq int64, cmi string) RoomMessage {
		return RoomMessage{RoomID: roomID, Seq: seq, Kind: "chat_message", Author: "alice",
			ClientMsgID: cmi, Payload: json.RawMessage(`{"text":"hi"}`), CreatedAtMs: 1}
	}
	if err := s.AppendRoomMessage(ctx, msg(1, "cm-1")); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := s.AppendRoomMessage(ctx, msg(2, "cm-1")); !errors.Is(err, ErrDuplicateClientMsg) {
		t.Fatalf("same client id at a new seq: err = %v, want ErrDuplicateClientMsg", err)
	}
	if seq, ok := s.LookupClientMsg(ctx, roomID, "cm-1"); !ok || seq != 1 {
		t.Fatalf("LookupClientMsg = %d,%v, want the original seq 1", seq, ok)
	}
	if err := s.AppendRoomMessage(ctx, msg(1, "cm-2")); !errors.Is(err, ErrDuplicateSeq) {
		t.Fatalf("taken seq: err = %v, want ErrDuplicateSeq", err)
	}
	if err := s.AppendRoomMessage(ctx, msg(2, "")); err != nil {
		t.Fatalf("append without client id: %v", err)
	}
}

func TestMemoryChatDuplicateAppendsReported(t *testing.T) {
	appendDupContract(t, NewMemoryChat(), "room-dup")
}

func TestMySQLChatDuplicateAppendsReported(t *testing.T) {
	dsn := os.Getenv("BNBLOC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set BNBLOC_TEST_MYSQL_DSN to run the MySQL integration test")
	}
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	defer b.DB.Close()
	appendDupContract(t, b.Chat, "itest-dup-"+itoaBase(time.Now().UnixNano()))
}
