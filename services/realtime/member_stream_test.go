package realtimehttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

func chatIn(channelID, text string) func(wire.Event) bool {
	return func(ev wire.Event) bool {
		return ev.Kind == wire.KindChatMessage && ev.ChannelID == channelID && strings.Contains(string(mustJSON(ev.Payload)), text)
	}
}

func activityOf(ev wire.Event) wire.ChannelActivityPayload {
	p, _ := wire.DecodePayload[wire.ChannelActivityPayload](ev.Payload)
	return p
}

// openMemberStream opens user's member stream and returns its events and
// id (the stream_opened event is consumed).
func openMemberStream(t *testing.T, srv *httptest.Server, user string) (<-chan wire.Event, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/my/events", nil)
	req.Header.Set("X-Bnbloc-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("member stream = %d", resp.StatusCode)
	}
	events := sseEvents(t, resp.Body)
	first := <-events
	opened, _ := wire.DecodePayload[wire.StreamOpenedPayload](first.Payload)
	if first.Kind != wire.EventStreamOpened || opened.StreamID == "" {
		t.Fatalf("first event = %+v", first)
	}
	return events, opened.StreamID
}

// scopeStream scopes a member stream as user; it returns the status.
func scopeStream(t *testing.T, srv *httptest.Server, user string, body wire.StreamScopeRequest) int {
	t.Helper()
	raw, _ := json.Marshal(body)
	return statusOf(t, http.MethodPost, srv.URL+"/v1/my/events/scope", user, string(raw))
}

func openIn(room string, since int64, channels ...string) *wire.StreamOpen {
	return &wire.StreamOpen{RoomID: room, Channels: channels, Since: since}
}

// The open room's lanes: full events for the open channel, one coalesced
// channel_activity for another (a mention marked), re-scoped to another
// channel without reopening; the stream is the caller's own.
func TestMemberStreamLanes(t *testing.T) {
	srv, _ := newChannelTestServer(t)
	bob, id := openMemberStream(t, srv, "bob")
	if code := scopeStream(t, srv, "bob", wire.StreamScopeRequest{StreamID: id, Open: openIn("room-1", 0, "ch_a")}); code != http.StatusNoContent {
		t.Fatalf("scope = %d", code)
	}
	collectUntil(t, bob, func(ev wire.Event) bool { return ev.Kind == wire.EventPresenceSnapshot })

	postChat(t, srv, "alice", "hello @bob", "ch_b")
	seqB := postChat(t, srv, "alice", "more", "ch_b")
	postChat(t, srv, "alice", "in the open one", "ch_a")
	seen := collectUntil(t, bob, chatIn("ch_a", "in the open one"))
	var firstActivity *wire.ChannelActivityPayload
	for _, ev := range seen {
		if ev.ChannelID == "ch_b" && ev.Kind != wire.EventChannelActivity {
			t.Fatalf("a closed channel's full event reached the stream: %+v", ev)
		}
		if ev.Kind == wire.EventChannelActivity {
			a := activityOf(ev)
			firstActivity = &a
		}
	}
	if firstActivity == nil || !firstActivity.Mention || firstActivity.Events != 1 || firstActivity.Messages != 1 {
		t.Fatalf("the first activity of ch_b = %+v, want one event mentioning bob", firstActivity)
	}
	var coalesced wire.ChannelActivityPayload
	collectUntil(t, bob, func(ev wire.Event) bool {
		coalesced = activityOf(ev)
		return ev.Kind == wire.EventChannelActivity
	})
	if coalesced.ChannelID != "ch_b" || coalesced.LastSeq != seqB || coalesced.Events != 1 || coalesced.Mention {
		t.Fatalf("the coalesced activity = %+v, want ch_b's second message (seq %d)", coalesced, seqB)
	}

	if code := scopeStream(t, srv, "bob", wire.StreamScopeRequest{StreamID: id, Open: openIn("room-1", 0, "ch_b")}); code != http.StatusNoContent {
		t.Fatalf("re-scope = %d", code)
	}
	postChat(t, srv, "alice", "now open", "ch_b")
	postChat(t, srv, "alice", "now closed", "ch_a")
	got := collectUntil(t, bob, func(ev wire.Event) bool {
		return ev.Kind == wire.EventChannelActivity && ev.ChannelID == "ch_a"
	})
	full := false
	for _, ev := range got {
		full = full || chatIn("ch_b", "now open")(ev)
		if ev.Kind == wire.KindChatMessage && ev.ChannelID == "ch_a" {
			t.Fatalf("ch_a's message in full after re-scoping away: %+v", ev)
		}
		if ev.Kind == wire.EventPresenceSnapshot {
			t.Fatal("re-scoping a channel of the same room replayed the room")
		}
	}
	if !full {
		t.Fatalf("ch_b's message did not arrive in full after re-scoping: %+v", got)
	}
	if code := scopeStream(t, srv, "alice", wire.StreamScopeRequest{StreamID: id, Open: openIn("room-1", 0, "ch_a")}); code != http.StatusNotFound {
		t.Fatalf("another member scoping bob's stream = %d, want 404", code)
	}
	if code := scopeStream(t, srv, "bob", wire.StreamScopeRequest{StreamID: id, Open: openIn("room-1", 0, "a", "b", "c", "d", "e")}); code != http.StatusBadRequest {
		t.Fatalf("five open channels = %d, want 400", code)
	}
	if code := scopeStream(t, srv, "bob", wire.StreamScopeRequest{StreamID: id, Open: openIn("room-x", 0, "ch_a")}); code != http.StatusForbidden {
		t.Fatalf("opening a room bob may not read = %d, want 403", code)
	}
}

// A members-only channel never reaches a non-member's open room.
func TestMemberStreamReadersOnly(t *testing.T) {
	srv, _, _ := newMembersOnlyServer(t)
	bob, id := openMemberStream(t, srv, "bob")
	scopeStream(t, srv, "bob", wire.StreamScopeRequest{StreamID: id, Open: openIn("room-1", 0, "ch_general")})
	postChat(t, srv, "alice", "the secret", "ch_b")
	postChat(t, srv, "alice", "open channel", "ch_a")
	postChat(t, srv, "alice", "general", "ch_general")
	sawOpen := false
	for _, ev := range collectUntil(t, bob, chatIn("ch_general", "general")) {
		if ev.ChannelID == "ch_b" {
			t.Fatalf("a members-only channel reached a non-member's stream: %+v", ev)
		}
		sawOpen = sawOpen || (ev.Kind == wire.EventChannelActivity && ev.ChannelID == "ch_a")
	}
	if !sawOpen {
		t.Fatal("an open channel's activity did not reach the stream")
	}
}

// Opening a room with the last sequence the client holds sends only what is
// newer; a room whose buffer does not reach back says so (stream_gap).
func TestMemberStreamOpensSinceItsSequence(t *testing.T) {
	srv, svc := newChannelTestServer(t)
	openPeopleTestStream(t, srv, svc, "alice", "allChannels=1", 1) // keeps the room's buffer
	s1 := postChat(t, srv, "alice", "one", "ch_a")
	postChat(t, srv, "alice", "two", "ch_a")
	bob, id := openMemberStream(t, srv, "bob")
	scopeStream(t, srv, "bob", wire.StreamScopeRequest{StreamID: id, Open: openIn("room-1", s1, "ch_a")})
	for _, ev := range collectUntil(t, bob, chatIn("ch_a", "two")) {
		if ev.Kind == wire.KindChatMessage || ev.Kind == wire.EventStreamGap {
			t.Fatalf("opening after seq %d sent %s %d", s1, ev.Kind, ev.Sequence)
		}
	}
	srv2, _ := newChannelTestServer(t)
	carl, id2 := openMemberStream(t, srv2, "bob")
	scopeStream(t, srv2, "bob", wire.StreamScopeRequest{StreamID: id2, Open: openIn("room-1", 40, "ch_a")})
	collectUntil(t, carl, func(ev wire.Event) bool { return ev.Kind == wire.EventStreamGap && ev.RoomID == "room-1" })
}

// Switching rooms leaves the first (presence), shows the second, and the
// first's messages count as unread only.
func TestMemberStreamSwitchesRooms(t *testing.T) {
	srv, svc := newChannelTestServer(t)
	bob, id := openMemberStream(t, srv, "bob")
	scopeStream(t, srv, "bob", wire.StreamScopeRequest{StreamID: id, Open: openIn("room-1", 0, "ch_a")})
	if !waitFor(func() bool { return slices.Contains(svc.Presence.Members("room-1"), "bob") }) {
		t.Fatal("bob is not here in the room he opened")
	}
	scopeStream(t, srv, "bob", wire.StreamScopeRequest{StreamID: id, Open: &wire.StreamOpen{}})
	// A departure is announced after the presence grace window.
	if !waitForUntil(func() bool { return !slices.Contains(svc.Presence.Members("room-1"), "bob") }, 8*time.Second) {
		t.Fatal("bob is still counted here after closing the room")
	}
	seq := postChat(t, srv, "alice", "while away", "ch_a")
	got := collectUntil(t, bob, func(ev wire.Event) bool {
		d, _ := wire.DecodePayload[wire.UnreadDeltaPayload](ev.Payload)
		return ev.Kind == wire.UnreadDeltaKind && slices.Contains(d.Rooms["room-1"].Seqs, seq)
	})
	for _, ev := range got {
		if ev.Kind == wire.KindChatMessage {
			t.Fatal("a room no longer open sent a message in full")
		}
	}
}

// Removed from the open room: the stream tells it and goes on (another
// room can be opened on it).
func TestMemberStreamOpenRoomRevoked(t *testing.T) {
	srv, _ := newChannelTestServer(t)
	bob, id := openMemberStream(t, srv, "bob")
	scopeStream(t, srv, "bob", wire.StreamScopeRequest{StreamID: id, Open: openIn("room-1", 0, "ch_a")})
	collectUntil(t, bob, func(ev wire.Event) bool { return ev.Kind == wire.EventPresenceSnapshot })
	code := statusOf(t, http.MethodPost, srv.URL+"/internal/v1/rooms/room-1/publish", "service:room",
		`{"kind":"member_removed","payload":{"userId":"bob"}}`)
	if code/100 != 2 {
		t.Fatalf("publish = %d", code)
	}
	collectUntil(t, bob, func(ev wire.Event) bool { return ev.Kind == wire.EventMemberRemoved })
	postChat(t, srv, "alice", "after", "ch_a")
	if code := scopeStream(t, srv, "bob", wire.StreamScopeRequest{StreamID: id, Open: &wire.StreamOpen{}}); code != http.StatusNoContent {
		t.Fatalf("the stream did not go on after the removal: %d", code)
	}
}

func memberPayload[T any](t *testing.T, f sseFrame) T {
	t.Helper()
	var ev struct {
		Payload T `json:"payload"`
	}
	if err := json.Unmarshal([]byte(f.data), &ev); err != nil {
		t.Fatalf("decode %s: %v", f.data, err)
	}
	return ev.Payload
}

// The stream's unread, across replicas: the state of the member's own
// rooms, read by the stream (none of the others), then what changes: a
// message posted through another replica, a mark of read on any device, a
// message deleted. A scope reaching the replica that does not hold the
// stream is handed on.
func TestMemberStreamUnreadAcrossReplicas(t *testing.T) {
	c := newCluster(t, 2)
	a, b := c.reps[0], c.reps[1]
	c.members.Add("room-1", "maya")
	c.members.Add("room-1", "jack")
	c.members.Add("room-2", "maya")
	c.members.Add("room-3", "jack") // not maya's
	c.chatVia(b, "room-1", "jack", "before", "m-0")

	s := c.open(a, "/v1/my/events", "maya")
	id := memberPayload[wire.StreamOpenedPayload](t, s.next(t, wire.EventStreamOpened)).StreamID
	st := memberPayload[wire.UnreadReply](t, s.next(t, wire.UnreadStateKind))
	if st.Unread["room-1"] != 1 || st.Unread["room-2"] != 0 || st.Unread["room-3"] != 0 {
		t.Fatalf("state %+v, want room-1: 1 and nothing of room-3", st.Unread)
	}
	resp, err := http.DefaultClient.Do(c.request(b, http.MethodPost, "/v1/my/events/scope", "maya", wire.StreamScopeRequest{StreamID: id, State: true}))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("scoping the stream through a replica that does not hold it = %d, want 204 (handed on)", resp.StatusCode)
	}
	if again := memberPayload[wire.UnreadReply](t, s.next(t, wire.UnreadStateKind)); again.Unread["room-1"] != 1 {
		t.Fatalf("the state asked again = %+v", again.Unread)
	}

	seq := c.chatVia(b, "room-1", "jack", "hello", "m-1")
	c.chatVia(b, "room-3", "jack", "elsewhere", "m-3")
	d := memberPayload[wire.UnreadDeltaPayload](t, s.next(t, wire.UnreadDeltaKind))
	if got := d.Rooms["room-1"].Seqs; !slices.Equal(got, []int64{seq}) {
		t.Fatalf("delta %+v, want room-1 seq %d", d.Rooms, seq)
	}
	if _, leaked := d.Rooms["room-3"]; leaked {
		t.Fatal("a room the member is not in reached their stream")
	}

	resp, _ = http.DefaultClient.Do(c.request(b, http.MethodPost, "/v1/rooms/room-1/read", "maya", map[string]any{"seq": seq}))
	resp.Body.Close()
	if d := memberPayload[wire.UnreadDeltaPayload](t, s.next(t, wire.UnreadDeltaKind)); !d.Rooms["room-1"].Read {
		t.Fatalf("after a mark of read through another replica: %+v, want room-1 read", d.Rooms)
	}
	resp, _ = http.DefaultClient.Do(c.request(a, http.MethodDelete, "/v1/rooms/room-1/messages/1", "jack", nil))
	resp.Body.Close()
	if d := memberPayload[wire.UnreadDeltaPayload](t, s.next(t, wire.UnreadDeltaKind)); !slices.Equal(d.Rooms["room-1"].Deleted, []int64{1}) {
		t.Fatalf("after a delete: %+v, want room-1 deleted 1", d.Rooms)
	}
}

// The stream follows the member's rooms: a join through another replica
// (bnbloc-room's member_joined) adds the room, tells the client and sends
// the state with it; a join announced for a room of another tenant, or
// one the directory does not confirm, adds nothing; a removal drops the
// room.
func TestMemberStreamFollowsTheMembersRooms(t *testing.T) {
	c := newCluster(t, 2)
	a, b := c.reps[0], c.reps[1]
	c.members.Add("room-1", "maya")
	s := c.open(a, "/v1/my/events", "maya")
	s.next(t, wire.EventStreamOpened)
	s.next(t, wire.UnreadStateKind)

	publish := func(room, body string) {
		t.Helper()
		resp, err := http.DefaultClient.Do(c.request(b, http.MethodPost, "/internal/v1/rooms/"+room+"/publish", "service:room", json.RawMessage(body)))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			t.Fatalf("publish to %s = %d", room, resp.StatusCode)
		}
	}
	// Announced but not a membership the directory knows: nothing.
	publish("room-x", `{"kind":"member_joined","payload":{"userId":"maya"}}`)
	c.members.Add("room-2", "maya")
	c.members.Add("room-2", "jack")
	c.chatVia(b, "room-2", "jack", "waiting", "m-1")
	publish("room-2", `{"kind":"member_joined","payload":{"userId":"maya"}}`)
	got := memberPayload[wire.MemberRoomsPayload](t, s.next(t, wire.EventMemberRooms))
	if !slices.Equal(got.Joined, []string{"room-2"}) || len(got.Left) != 0 {
		t.Fatalf("member_rooms = %+v, want room-2 joined (and not room-x)", got)
	}
	if st := memberPayload[wire.UnreadReply](t, s.next(t, wire.UnreadStateKind)); st.Unread["room-2"] != 1 {
		t.Fatalf("state after the join = %+v, want room-2's message", st.Unread)
	}
	seq := c.chatVia(b, "room-2", "jack", "after the join", "m-2")
	if d := memberPayload[wire.UnreadDeltaPayload](t, s.next(t, wire.UnreadDeltaKind)); !slices.Equal(d.Rooms["room-2"].Seqs, []int64{seq}) {
		t.Fatalf("delta %+v, want room-2 seq %d", d.Rooms, seq)
	}

	c.members.Remove("room-2", "maya")
	publish("room-2", `{"kind":"member_removed","payload":{"userId":"maya"}}`)
	if got := memberPayload[wire.MemberRoomsPayload](t, s.next(t, wire.EventMemberRooms)); !slices.Equal(got.Left, []string{"room-2"}) {
		t.Fatalf("member_rooms = %+v, want room-2 left", got)
	}
	if st := memberPayload[wire.UnreadReply](t, s.next(t, wire.UnreadStateKind)); st.Unread["room-2"] != 0 {
		t.Fatalf("state after leaving = %+v, want no room-2", st.Unread)
	}
}
