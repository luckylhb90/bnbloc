package store

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

// R7: the search backfill walks the feed in primary-key order with a
// resumable cursor and bounded batches.

func TestSearchCursorRoundTrip(t *testing.T) {
	for _, c := range []searchCursor{{roomID: "r1", seq: 7}, {roomID: "a/b", seq: 0}, {roomID: "x", seq: 1 << 40}} {
		got, err := parseSearchCursor(c.String())
		if err != nil || got != c {
			t.Fatalf("parse(%q) = %+v, %v; want %+v", c.String(), got, err, c)
		}
	}
	for _, bad := range []string{"r1", "/5", "r1/x", "r1/"} {
		if _, err := parseSearchCursor(bad); !errors.Is(err, ErrInvalidSearchCursor) {
			t.Fatalf("parse(%q) err = %v, want ErrInvalidSearchCursor", bad, err)
		}
	}
}

func TestMemoryChatReindexSearchTextWalksAllRows(t *testing.T) {
	s := NewMemoryChat()
	ctx := testCtx()
	for _, room := range []string{"rb", "ra"} {
		for seq := int64(1); seq <= 3; seq++ {
			if err := s.AppendRoomMessage(ctx, RoomMessage{RoomID: room, Seq: seq, Kind: "chat_message",
				Payload: json.RawMessage(`{"text":"hello"}`), CreatedAtMs: seq}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.AppendRoomMessage(tenantCtx("other"), RoomMessage{RoomID: "rc", Seq: 1, Kind: "chat_message",
		Payload: json.RawMessage(`{"text":"x"}`), CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	var cursors []string
	total, cursor := 0, ""
	for i := 0; ; i++ {
		b, err := s.ReindexSearchText(ctx, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if b.Updated != 0 {
			t.Fatalf("memory backend updated %d rows; it indexes on read", b.Updated)
		}
		total += b.Scanned
		cursor = b.Cursor
		cursors = append(cursors, cursor)
		if b.Done {
			break
		}
		if i > 10 {
			t.Fatal("reindex never finished")
		}
	}
	if total != 7 {
		t.Fatalf("scanned %d rows across tenants, want 7 (cursors %v)", total, cursors)
	}
	if want := []string{"ra/2", "rb/1", "rb/3", "rc/1"}; !equalStrings(cursors, want) {
		t.Fatalf("cursors = %v, want %v (primary-key order)", cursors, want)
	}
	if _, err := s.ReindexSearchText(ctx, "nope", 2); !errors.Is(err, ErrInvalidSearchCursor) {
		t.Fatalf("bad cursor err = %v", err)
	}
}

// Rows stored before the search migration (search_text NULL) become
// searchable after the reindex; already-indexed, empty-text and non-message
// rows are left alone; a second pass updates nothing.
func TestMySQLChatReindexSearchTextBackfills(t *testing.T) {
	dsn := os.Getenv("BNBLOC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set BNBLOC_TEST_MYSQL_DSN to run the MySQL integration test")
	}
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	defer b.DB.Close()
	ctx := testCtx()
	room := "itest-reindex-" + itoaBase(time.Now().UnixNano())
	const agentPayload = `{"messages":[{"payload":{"text":"reply one"}},{"payload":{"text":"部署窗口"}}]}`
	legacy := func(seq int64, kind, payload string) {
		t.Helper()
		if _, err := b.DB.ExecContext(ctx,
			`INSERT INTO room_messages (tenant_id, room_id, channel_id, seq, kind, author, payload_json, search_text, created_at_ms)
			 VALUES (?, ?, '', ?, ?, 'alice', ?, NULL, ?)`, testTenant, room, seq, kind, payload, seq*100); err != nil {
			t.Fatalf("insert legacy row %d: %v", seq, err)
		}
	}
	legacy(1, "chat_message", `{"text":"legacy deploy window"}`)
	legacy(2, "agent_messages", agentPayload)
	legacy(3, "chat_message", `{"text":""}`)
	legacy(4, "task_updated", `{"text":"not a feed message"}`)
	if err := b.Chat.AppendRoomMessage(ctx, RoomMessage{RoomID: room, Seq: 5, Kind: "chat_message", Author: "alice",
		Payload: json.RawMessage(`{"text":"fresh deploy note"}`), CreatedAtMs: 500}); err != nil {
		t.Fatal(err)
	}
	search := func(text string) int {
		t.Helper()
		ms, err := b.Chat.SearchRoomMessages(ctx, MessageSearch{RoomIDs: []string{room}, Text: text})
		if err != nil {
			t.Fatalf("search %q: %v", text, err)
		}
		return len(ms)
	}
	if n := search("deploy"); n != 1 {
		t.Fatalf("before reindex: %d hits for deploy, want only the fresh row", n)
	}

	// Walk from just before this test's room (the DB is shared) in batches
	// of 2 until the cursor leaves the room.
	pass := func() (scanned, updated int) {
		t.Helper()
		cursor := searchCursor{roomID: room, seq: 0}.String()
		for i := 0; i < 50; i++ {
			bt, err := b.Chat.ReindexSearchText(ctx, cursor, 2)
			if err != nil {
				t.Fatalf("reindex: %v", err)
			}
			scanned += bt.Scanned
			updated += bt.Updated
			at, err := parseSearchCursor(bt.Cursor)
			if err != nil {
				t.Fatal(err)
			}
			if bt.Done || at.roomID != room {
				return
			}
			cursor = bt.Cursor
		}
		t.Fatal("reindex never left the room")
		return
	}
	scanned, updated := pass()
	if scanned < 5 || updated < 2 {
		t.Fatalf("first pass scanned=%d updated=%d, want >=5 scanned and >=2 updated", scanned, updated)
	}
	if n := search("deploy"); n != 2 {
		t.Fatalf("after reindex: %d hits for deploy, want 2", n)
	}
	if n := search("窗口"); n != 1 {
		t.Fatalf("after reindex: %d hits for the CJK term, want 1", n)
	}
	var got string
	if err := b.DB.QueryRowContext(ctx, `SELECT search_text FROM room_messages WHERE room_id = ? AND seq = 2`, room).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want := MessageText("agent_messages", json.RawMessage(agentPayload)); got != want {
		t.Fatalf("search_text = %q, want the append-path text %q", got, want)
	}
	var untouched int
	if err := b.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM room_messages WHERE room_id = ? AND seq IN (3, 4) AND search_text IS NULL`, room).Scan(&untouched); err != nil {
		t.Fatal(err)
	}
	if untouched != 2 {
		t.Fatalf("%d of the empty/non-message rows kept NULL search text, want 2", untouched)
	}
	var stillEmpty int
	if err := b.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM room_messages WHERE room_id = ? AND seq IN (1, 2, 5) AND (search_text IS NULL OR search_text = '')`, room).Scan(&stillEmpty); err != nil {
		t.Fatal(err)
	}
	if stillEmpty != 0 {
		t.Fatalf("%d message rows still lack search text", stillEmpty)
	}
	// A second pass finds nothing left in this room to index.
	if _, err := b.DB.ExecContext(ctx, `UPDATE room_messages SET search_text = 'edited text' WHERE room_id = ? AND seq = 1`, room); err != nil {
		t.Fatal(err)
	}
	pass()
	if err := b.DB.QueryRowContext(ctx, `SELECT search_text FROM room_messages WHERE room_id = ? AND seq = 1`, room).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "edited text" {
		t.Fatalf("second pass overwrote an indexed row: %q", got)
	}
}
