package realtimehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func newHandoffServer(t *testing.T) (*Service, *store.MemoryChat, *realtime.HandoffClient, context.Context) {
	t.Helper()
	const key = "internal-key"
	t.Setenv("BNBLOC_INTERNAL_KEY", key)
	mem := membership.NewStatic()
	mem.Add("room-1", "alice")
	chat := store.NewMemoryChat()
	svc := &Service{Hub: realtime.NewHub(64, 16), Membership: mem, Chat: chat}
	srv := httptest.NewServer(svcauth.Middleware(key, svc.Handler()))
	t.Cleanup(srv.Close)
	return svc, chat, realtime.NewHandoffClient(srv.URL, "room"), tenant.NewContext(context.Background(), tenant.Default)
}

func importRows(rows ...wire.FeedMessage) wire.RoomImportBatch {
	return wire.RoomImportBatch{FromRoomID: "src", FromRoomName: "Source", ImportedAtMs: 99, Messages: rows}
}

func chatRow(seq int64, text string) wire.FeedMessage {
	p, _ := json.Marshal(map[string]any{"userId": "bob", "text": text,
		"recipient": map[string]any{"ownerId": "bob", "provider": "codex"}})
	return wire.FeedMessage{Sequence: seq, Kind: wire.KindChatMessage, Payload: p, AtUnixMs: 1000 + seq}
}

// Imported history is persisted on the room's monotonic sequence but never
// reaches a subscriber: a bridge (live subscriber) cannot drive an agent from
// an imported agent-addressed chat, and no UI stream replays it.
func TestImportPersistsWithoutPublishing(t *testing.T) {
	svc, chat, client, ctx := newHandoffServer(t)
	live := svc.Hub.SubscribeLive("room-1")
	defer svc.Hub.Unsubscribe("room-1", live)

	preEv := wire.Event{RoomID: "room-1", Kind: wire.KindChatMessage, Payload: map[string]any{"userId": "alice", "text": "before"}}
	d, err := svc.durable(ctx, preEv, "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	pre, err := svc.Hub.PublishCommit(preEv, d)
	if err != nil {
		t.Fatal(err)
	}
	<-live.Events() // the pre-existing live chat

	agentPayload, _ := json.Marshal(map[string]any{"agentSessionId": "s1", "messages": []map[string]any{{"payload": map[string]any{"text": "done", "ownerUserId": "bob", "provider": "codex"}}}})
	reply, err := client.ImportMessages(ctx, "alice", "room-1", importRows(chatRow(7, "do it"), wire.FeedMessage{Sequence: 8, Kind: wire.KindAgentMessages, Payload: agentPayload, AtUnixMs: 1008}))
	if err != nil {
		t.Fatal(err)
	}
	if reply.Imported != 2 || reply.LastSequence != pre.Sequence+2 {
		t.Fatalf("reply = %+v, want 2 rows ending at %d", reply, pre.Sequence+2)
	}
	select {
	case ev := <-live.Events():
		t.Fatalf("import delivered %s to a live subscriber", ev.Kind)
	default:
	}
	replay := svc.Hub.Subscribe("room-1")
	defer svc.Hub.Unsubscribe("room-1", replay)
	if ev := <-replay.Events(); ev.Sequence != pre.Sequence {
		t.Fatalf("replay buffer holds %+v, want only the pre-existing chat", ev)
	}
	select {
	case ev := <-replay.Events():
		t.Fatalf("import retained %s for replay", ev.Kind)
	default:
	}

	msgs, _ := chat.RoomHistory(ctx, "room-1", "", 0, 50)
	if len(msgs) != 3 || msgs[1].Author != "" || msgs[1].CreatedAtMs != 1007 {
		t.Fatalf("history = %+v", msgs)
	}
	var p struct {
		Text     string              `json:"text"`
		Imported wire.ImportedMarker `json:"imported"`
	}
	_ = json.Unmarshal(msgs[1].Payload, &p)
	if p.Text != "do it" || p.Imported != (wire.ImportedMarker{FromRoomID: "src", FromRoomName: "Source", OriginalSequence: 7, ImportedAtMs: 99}) {
		t.Fatalf("imported payload = %+v", p)
	}
	// Live chat continues after the imported rows.
	next, err := svc.Hub.PublishCommit(wire.Event{RoomID: "room-1", Kind: wire.KindChatMessage},
		realtime.Durable{Journal: chat, Row: store.RoomMessage{TenantID: tenant.Default}})
	if err != nil || next.Sequence != reply.LastSequence+1 {
		t.Fatalf("next seq = %d (err %v), want %d", next.Sequence, err, reply.LastSequence+1)
	}
}

func TestImportValidatesBeforeWriting(t *testing.T) {
	_, chat, client, ctx := newHandoffServer(t)
	bad := wire.FeedMessage{Sequence: 2, Kind: "presence_join", Payload: json.RawMessage(`{}`)}
	_, err := client.ImportMessages(ctx, "alice", "room-1", importRows(chatRow(1, "ok"), bad))
	var se *realtime.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusBadRequest {
		t.Fatalf("err = %v, want 400", err)
	}
	long := chatRow(3, strings.Repeat("x", maxChatTextBytes+1))
	if _, err := client.ImportMessages(ctx, "alice", "room-1", importRows(long)); !errors.As(err, &se) || se.Status != http.StatusBadRequest {
		t.Fatalf("over-long chat err = %v, want 400", err)
	}
	rows := make([]wire.FeedMessage, wire.MaxImportBatchMessages+1)
	for i := range rows {
		rows[i] = chatRow(int64(i+1), "x")
	}
	if _, err := client.ImportMessages(ctx, "alice", "room-1", importRows(rows...)); !errors.As(err, &se) || se.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized batch err = %v, want 413", err)
	}
	if msgs, _ := chat.RoomHistory(ctx, "room-1", "", 0, 50); len(msgs) != 0 {
		t.Fatalf("rejected batches wrote %d rows", len(msgs))
	}
	if _, err := client.ImportMessages(ctx, "mallory", "room-1", importRows(chatRow(1, "x"))); !errors.As(err, &se) || se.Status != http.StatusForbidden {
		t.Fatalf("non-member import err = %v, want 403", err)
	}
}

func TestFeedPagesWholeRoomExcludingDeleted(t *testing.T) {
	_, chat, client, ctx := newHandoffServer(t)
	for i, ch := range []string{"ch_a", "ch_b", "ch_a", "ch_b"} {
		if err := chat.AppendRoomMessage(ctx, store.RoomMessage{RoomID: "room-1", ChannelID: ch, Seq: int64(i + 1), Kind: wire.KindChatMessage, Author: "alice", Payload: json.RawMessage(`{"userId":"alice"}`), CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if ok, _ := chat.DeleteRoomMessage(ctx, "room-1", 2, "alice", 5); !ok {
		t.Fatal("delete failed")
	}
	page, err := client.Feed(ctx, "alice", "room-1", 0, 2)
	if err != nil || len(page) != 2 || page[0].Sequence != 1 || page[1].Sequence != 3 || page[1].ChannelID != "ch_a" {
		t.Fatalf("page 1 = %+v, %v", page, err)
	}
	page, err = client.Feed(ctx, "alice", "room-1", 3, 2)
	if err != nil || len(page) != 1 || page[0].Sequence != 4 || page[0].ChannelID != "ch_b" {
		t.Fatalf("page 2 = %+v, %v", page, err)
	}
	if _, err := client.Feed(ctx, "mallory", "room-1", 0, 2); err == nil {
		t.Fatal("non-member read the feed")
	}
	// Unsigned callers never reach the internal routes.
	resp, err := http.Post(client.BaseURL+"/internal/v1/rooms/room-1/import", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned import = %d, want 401", resp.StatusCode)
	}
}
