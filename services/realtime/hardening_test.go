package realtimehttp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// An explicit channelId must belong to the addressed room: a stale id (e.g.
// the previous room's channel after a room switch) or a foreign room's channel
// is rejected on every channel-scoped surface instead of silently creating a
// stream no channel view can read.
func TestForeignChannelRejected(t *testing.T) {
	srv, svc := newChannelTestServer(t)
	if err := svc.Channels.(*store.MemoryChannel).PutChannel(dctx(), store.RoomChannel{
		ChannelID: "ch_other_room", TenantID: tenant.Default, RoomID: "room-2", CreatedBy: "x", CreatedAtMs: 9,
	}); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []string{"ch_missing", "ch_other_room"} {
		if r := channelPost(t, srv, "/v1/rooms/room-1/chat", "alice", `{"text":"hi","channelId":"`+ch+`"}`); r.StatusCode != http.StatusNotFound {
			t.Errorf("chat to %s: status %d, want 404", ch, r.StatusCode)
		}
		if r := channelPost(t, srv, "/v1/rooms/room-1/read", "alice", `{"seq":1,"channelId":"`+ch+`"}`); r.StatusCode != http.StatusNotFound {
			t.Errorf("read on %s: status %d, want 404", ch, r.StatusCode)
		}
		if r := channelPost(t, srv, "/v1/rooms/room-1/typing", "alice", `{"channelId":"`+ch+`"}`); r.StatusCode != http.StatusNotFound {
			t.Errorf("typing on %s: status %d, want 404", ch, r.StatusCode)
		}
		for _, path := range []string{"/history?channelId=", "/events?channelId="} {
			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1"+path+ch, nil)
			req.Header.Set("X-Bnbloc-User", "alice")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("GET %s%s: status %d, want 404", path, ch, resp.StatusCode)
			}
		}
	}
	// A real channel of the room still works.
	if seq := postChat(t, srv, "alice", "ok", "ch_a"); seq == 0 {
		t.Fatal("chat to a live channel failed")
	}
}

// openStream subscribes user to room-1's SSE stream and returns a channel that
// closes when the server ends the stream.
func openStream(t *testing.T, srv *httptest.Server, hub *realtime.Hub, user string) <-chan struct{} {
	t.Helper()
	before := hub.SubscriberCount("room-1")
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1/events?presence=0", nil)
	req.Header.Set("X-Bnbloc-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("subscribe %s: status %d", user, resp.StatusCode)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	deadline := time.Now().Add(2 * time.Second)
	for hub.SubscriberCount("room-1") == before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	ended := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, bufio.NewReader(resp.Body))
		close(ended)
	}()
	return ended
}

// A removed member's live stream ends as soon as the removal event arrives
// (typed payload in-process, JSON map via the internal ingest), while other
// members keep streaming; a room deletion ends every stream.
func TestRevocationEndsLiveStreams(t *testing.T) {
	hub := realtime.NewHub(16, 16)
	mem := membership.NewStatic()
	mem.Add("room-1", "alice")
	mem.Add("room-1", "bob")
	srv := httptest.NewServer((&Service{Hub: hub, Membership: mem}).Handler())
	defer srv.Close()

	alice := openStream(t, srv, hub, "alice")
	bob := openStream(t, srv, hub, "bob")
	hub.Publish(wire.Event{RoomID: "room-1", Kind: wire.EventMemberRemoved, Payload: wire.MemberRemovedPayload{UserID: "alice"}})
	select {
	case <-alice:
	case <-time.After(2 * time.Second):
		t.Fatal("removed member's stream stayed open")
	}
	select {
	case <-bob:
		t.Fatal("another member's stream was closed by alice's removal")
	case <-time.After(100 * time.Millisecond):
	}

	// Via the internal ingest the payload is a decoded map.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/v1/rooms/room-1/publish",
		strings.NewReader(`{"kind":"`+wire.EventRoomDeleted+`"}`))
	req.Header.Set("X-Bnbloc-User", "service:room")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("internal publish status %d", resp.StatusCode)
	}
	select {
	case <-bob:
	case <-time.After(2 * time.Second):
		t.Fatal("stream stayed open after room deletion")
	}
}

func TestAccessRevokedPayloadShapes(t *testing.T) {
	cases := []struct {
		ev   wire.Event
		want bool
	}{
		{wire.Event{Kind: wire.EventMemberRemoved, Payload: wire.MemberRemovedPayload{UserID: "a"}}, true},
		{wire.Event{Kind: wire.EventMemberRemoved, Payload: &wire.MemberRemovedPayload{UserID: "a"}}, true},
		{wire.Event{Kind: wire.EventMemberRemoved, Payload: map[string]any{"userId": "a"}}, true},
		{wire.Event{Kind: wire.EventMemberRemoved, Payload: map[string]any{"userId": "b"}}, false},
		{wire.Event{Kind: wire.EventRoomDeleted}, true},
		{wire.Event{Kind: "chat_message", Payload: map[string]any{"userId": "a"}}, false},
	}
	for i, c := range cases {
		if got := accessRevoked(c.ev, "a"); got != c.want {
			t.Errorf("case %d: accessRevoked = %v, want %v", i, got, c.want)
		}
	}
}

// Concurrent sends with one clientMsgId produce exactly one stored and one
// delivered message; every caller learns the same sequence.
func TestChatClientMsgIDRaceDeliversOnce(t *testing.T) {
	hub := realtime.NewHub(64, 64)
	mem := membership.NewStatic()
	mem.Add("room-1", "alice")
	srv := httptest.NewServer((&Service{Hub: hub, Membership: mem, Chat: store.NewMemoryChat()}).Handler())
	defer srv.Close()
	sub := hub.SubscribeLive("room-1")
	defer hub.Unsubscribe("room-1", sub)

	const n = 12
	seqs := make([]int64, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/rooms/room-1/chat",
				strings.NewReader(`{"text":"hi","clientMsgId":"cm-1"}`))
			req.Header.Set("X-Bnbloc-User", "alice")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			defer resp.Body.Close()
			var out struct {
				Sequence int64 `json:"sequence"`
			}
			if resp.StatusCode != http.StatusOK {
				t.Errorf("send %d: status %d", i, resp.StatusCode)
			}
			_ = json.NewDecoder(resp.Body).Decode(&out)
			seqs[i] = out.Sequence
		}(i)
	}
	wg.Wait()
	for i := 1; i < n; i++ {
		if seqs[i] != seqs[0] || seqs[0] == 0 {
			t.Fatalf("sequences differ: %v", seqs)
		}
	}
	chats := 0
	for len(sub.Events()) > 0 {
		if (<-sub.Events()).Kind == "chat_message" {
			chats++
		}
	}
	if chats != 1 {
		t.Fatalf("delivered %d chat_message events, want exactly 1", chats)
	}
}

// failingChat rejects every append, simulating a store outage.
type failingChat struct{ *store.MemoryChat }

func (failingChat) AppendRoomMessage(context.Context, store.RoomMessage) error {
	return errors.New("db down")
}

// A message that could not be persisted is not delivered live: the client's
// retry is then the only copy anyone sees.
func TestChatPersistFailureDeliversNothing(t *testing.T) {
	hub := realtime.NewHub(16, 16)
	mem := membership.NewStatic()
	mem.Add("room-1", "alice")
	srv := httptest.NewServer((&Service{Hub: hub, Membership: mem, Chat: failingChat{store.NewMemoryChat()}}).Handler())
	defer srv.Close()
	sub := hub.SubscribeLive("room-1")
	defer hub.Unsubscribe("room-1", sub)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/rooms/room-1/chat", strings.NewReader(`{"text":"hi","clientMsgId":"c1"}`))
	req.Header.Set("X-Bnbloc-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", resp.StatusCode)
	}
	if len(sub.Events()) != 0 {
		t.Fatal("unpersisted message was delivered live")
	}
}

// The internal ingest persists under the tenant the (signed) publisher
// asserted, so a non-default tenant's agent replies reach its history.
func TestInternalPublishPersistsUnderAssertedTenant(t *testing.T) {
	chat := store.NewMemoryChat()
	srv := httptest.NewServer((&Service{Hub: realtime.NewHub(16, 16), Membership: membership.NewStatic(), Chat: chat}).Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/v1/rooms/room-1/publish",
		strings.NewReader(`{"kind":"agent_messages","payload":{"messages":[]}}`))
	req.Header.Set("X-Bnbloc-User", "service:activity")
	req.Header.Set("X-Bnbloc-Tenant", "acme")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	got, err := chat.RoomHistory(tenant.NewContext(context.Background(), "acme"), "room-1", "", 0, 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("acme history = %d rows (err %v), want 1", len(got), err)
	}
	if def, _ := chat.RoomHistory(dctx(), "room-1", "", 0, 10); len(def) != 0 {
		t.Fatalf("message leaked into the default tenant (%d rows)", len(def))
	}
}

// Unauthenticated internal publishes are refused; oversized ones too.
func TestInternalPublishGuards(t *testing.T) {
	srv := httptest.NewServer((&Service{Hub: realtime.NewHub(16, 16), Membership: membership.NewStatic()}).Handler())
	defer srv.Close()
	post := func(body string, user string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/v1/rooms/room-1/publish", strings.NewReader(body))
		if user != "" {
			req.Header.Set("X-Bnbloc-User", user)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := post(`{"kind":"chat_message"}`, ""); code != http.StatusUnauthorized {
		t.Errorf("anonymous publish: %d, want 401", code)
	}
	big := `{"kind":"x","payload":"` + strings.Repeat("a", maxInternalPublishBytes) + `"}`
	if code := post(big, "service:activity"); code != http.StatusBadRequest {
		t.Errorf("oversized publish: %d, want 400", code)
	}
}

// rooms-unread is bounded and de-duplicated.
func TestUnreadRoomListBounded(t *testing.T) {
	srv, _ := newChannelTestServer(t)
	ids := make([]string, 0, maxUnreadRooms+1)
	for i := 0; i <= maxUnreadRooms; i++ {
		ids = append(ids, fmt.Sprintf("room-%d", i))
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms-unread?rooms="+strings.Join(ids, ","), nil)
	req.Header.Set("X-Bnbloc-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("over-limit unread: %d, want 400", resp.StatusCode)
	}
	// Duplicates collapse, so a repeated list within the cap is fine.
	dup := strings.TrimSuffix(strings.Repeat("room-1,", maxUnreadRooms+5), ",")
	if body := channelGet(t, srv, "/v1/rooms-unread?rooms="+dup, "alice"); !strings.Contains(body, "unread") {
		t.Fatalf("dedup unread body = %q", body)
	}
}

// Oversized read/typing bodies are refused.
func TestSmallBodiesBounded(t *testing.T) {
	srv, _ := newChannelTestServer(t)
	big := `{"seq":1,"channelId":"` + strings.Repeat("x", maxSmallBodyBytes) + `"}`
	for _, p := range []string{"/v1/rooms/room-1/read", "/v1/rooms/room-1/typing"} {
		if r := channelPost(t, srv, p, "alice", big); r.StatusCode != http.StatusBadRequest {
			t.Errorf("%s oversized: %d, want 400", p, r.StatusCode)
		}
	}
	// An empty typing body stays valid (legacy clients).
	if r := channelPost(t, srv, "/v1/rooms/room-1/typing", "alice", ""); r.StatusCode != http.StatusOK {
		t.Errorf("empty typing body: %d, want 200", r.StatusCode)
	}
}
