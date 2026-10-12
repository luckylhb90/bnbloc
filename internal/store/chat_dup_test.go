package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
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
	if seq, ok := must2(s.LookupClientMsg(ctx, roomID, "cm-1"))(t); !ok || seq != 1 {
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
	dsn := mysqlTestDSN(t)
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	defer b.DB.Close()
	appendDupContract(t, b.Chat, "itest-dup-"+itoaBase(time.Now().UnixNano()))
}

// appendBatchContract asserts the AppendRoomMessages contract shared by every
// ChatStore: a batch is stored whole or not at all, a client id already in
// history is reported with its stored sequence (every conflicting row of the
// batch), a taken (room, seq) is ErrDuplicateSeq, and each row lands under
// its own tenant.
func appendBatchContract(t *testing.T, s ChatStore, roomID string) {
	t.Helper()
	ctx := testCtx()
	tid, _ := tenant.Require(ctx)
	row := func(seq int64, cmi string) RoomMessage {
		return RoomMessage{TenantID: tid, RoomID: roomID, Seq: seq, Kind: "chat_message", Author: "alice",
			ClientMsgID: cmi, Payload: json.RawMessage(`{"text":"hi"}`), CreatedAtMs: 1}
	}
	stored := func() []int64 {
		t.Helper()
		msgs, err := s.RoomFeed(ctx, roomID, 0, 100)
		if err != nil {
			t.Fatalf("feed: %v", err)
		}
		var seqs []int64
		for _, m := range msgs {
			seqs = append(seqs, m.Seq)
		}
		return seqs
	}
	if err := s.AppendRoomMessages(ctx, []RoomMessage{row(1, "a"), row(2, ""), row(3, "b")}); err != nil {
		t.Fatalf("batch append: %v", err)
	}
	if got := stored(); len(got) != 3 {
		t.Fatalf("stored %v, want seqs 1..3", got)
	}

	// Two rows of the batch repeat stored client ids: both are named, with
	// their stored sequences, and the fresh row is not stored either.
	err := s.AppendRoomMessages(ctx, []RoomMessage{row(4, "a"), row(5, "c"), row(6, "b")})
	var conflict *ClientMsgConflict
	if !errors.As(err, &conflict) || !errors.Is(err, ErrDuplicateClientMsg) {
		t.Fatalf("batch with stored client ids: err = %v, want *ClientMsgConflict", err)
	}
	if len(conflict.Seqs) != 2 || conflict.Seqs["a"] != 1 || conflict.Seqs["b"] != 3 {
		t.Fatalf("conflict = %v, want a:1 b:3", conflict.Seqs)
	}
	if got := stored(); len(got) != 3 {
		t.Fatalf("a failed batch stored rows: %v", got)
	}

	// A taken (room, seq) fails the whole batch.
	if err := s.AppendRoomMessages(ctx, []RoomMessage{row(4, "c"), row(3, "d")}); !errors.Is(err, ErrDuplicateSeq) {
		t.Fatalf("batch with a taken seq: err = %v, want ErrDuplicateSeq", err)
	}
	if got := stored(); len(got) != 3 {
		t.Fatalf("a fenced batch stored rows: %v", got)
	}

	// Caller errors: rows of two rooms, a client id twice, a row without tenant.
	other := row(4, "")
	other.RoomID = roomID + "-other"
	noTenant := row(4, "")
	noTenant.TenantID = ""
	for name, bad := range map[string][]RoomMessage{
		"two rooms":        {row(4, ""), other},
		"repeated id":      {row(4, "x"), row(5, "x")},
		"no tenant on row": {noTenant},
	} {
		if err := s.AppendRoomMessages(ctx, bad); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}

	// Each row is written under its own tenant.
	acme := row(4, "e")
	acme.TenantID = "acme-" + roomID
	if err := s.AppendRoomMessages(ctx, []RoomMessage{acme}); err != nil {
		t.Fatalf("other-tenant row: %v", err)
	}
	if got := stored(); len(got) != 3 {
		t.Fatalf("another tenant's row is visible in this tenant: %v", got)
	}
	if seq, ok := must2(s.LookupClientMsg(tenant.NewContext(context.Background(), acme.TenantID), roomID, "e"))(t); !ok || seq != 4 {
		t.Fatalf("other tenant lookup = %d,%v, want 4", seq, ok)
	}
}

func TestMemoryChatAppendBatch(t *testing.T) {
	appendBatchContract(t, NewMemoryChat(), "room-batch")
}

func TestMySQLChatAppendBatch(t *testing.T) {
	dsn := mysqlTestDSN(t)
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	defer b.DB.Close()
	appendBatchContract(t, b.Chat, "itest-batch-"+itoaBase(time.Now().UnixNano()))
}
