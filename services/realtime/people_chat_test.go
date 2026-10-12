package realtimehttp

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// bridgeStreamQuery is the exact query the bridge subscribes with
// (services/bridge consumeRoom): it never opts into the people chat.
const bridgeStreamQuery = "presence=0&allChannels=1&replay=0"

// newPeopleChatServer is the channel test server plus room-1's people chat.
func newPeopleChatServer(t *testing.T) (*httptest.Server, *Service, string) {
	t.Helper()
	srv, svc := newChannelTestServer(t)
	pc, err := svc.Channels.(*store.MemoryChannel).EnsurePeopleChannel(dctx(), "room-1", "alice", 10)
	if err != nil {
		t.Fatal(err)
	}
	return srv, svc, pc.ChannelID
}

// openStream subscribes as user with query and returns its decoded events.
func openPeopleTestStream(t *testing.T, srv *httptest.Server, svc *Service, user, query string, wantSubs int) <-chan wire.Event {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1/events?"+query, nil)
	req.Header.Set("X-Bnbloc-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("subscribe status = %d", resp.StatusCode)
	}
	if !waitFor(func() bool { return svc.Hub.SubscriberCount("room-1") == wantSubs }) {
		t.Fatal("timed out waiting for the subscriber to register")
	}
	return sseEvents(t, resp.Body)
}

// collectUntil reads events until one matches stop, returning everything
// seen before it.
func collectUntil(t *testing.T, events <-chan wire.Event, stop func(wire.Event) bool) []wire.Event {
	t.Helper()
	var seen []wire.Event
	deadline := time.After(4 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("stream closed early")
			}
			if stop(ev) {
				return seen
			}
			seen = append(seen, ev)
		case <-deadline:
			t.Fatalf("timed out; saw %+v", seen)
		}
	}
}

// The people chat is human-only: an agent recipient there is a 400 and
// creates no job, while a people message posts normally.
func TestPeopleChatRejectsAgentRecipient(t *testing.T) {
	srv, svc, people := newPeopleChatServer(t)
	resp := channelPost(t, srv, "/v1/rooms/room-1/chat", "alice",
		fmt.Sprintf(`{"text":"@bob/codex hi","channelId":%q,"recipient":{"ownerId":"bob","provider":"codex"}}`, people))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("agent recipient in people chat = %d, want 400", resp.StatusCode)
	}
	postChat(t, srv, "alice", "just us", people)
	if got := historyTexts(t, srv, "bob", "?channelId="+people); len(got) != 1 || got[0] != "just us" {
		t.Fatalf("people history = %v, want [just us]", got)
	}
	// Schedules cannot target an agent from the people chat either.
	sc := wire.RoomSchedule{
		RoomID: "room-1", ChannelID: people, Text: "standup", Timezone: "UTC",
		Recipient:  wire.ScheduleRecipient{Kind: wire.ScheduleRecipientAgent, OwnerID: "bob", Provider: "codex"},
		Recurrence: wire.ScheduleRecurrence{Kind: "daily", Time: "09:00"},
	}
	if code, err := svc.checkScheduleSpec(dctx(), &sc, time.Now()); code != http.StatusBadRequest || !errors.Is(err, errAgentInPeopleChat) {
		t.Fatalf("agent schedule in people chat = %d %v, want 400 errAgentInPeopleChat", code, err)
	}
}

// An all-channels stream without includePeople=1 (the bridge's) never sees
// a people-chat event — chat, edit, delete, task — while the web's
// includePeople=1 stream and an explicit people-chat stream do.
func TestPeopleChatHiddenFromAgentStreams(t *testing.T) {
	srv, svc, people := newPeopleChatServer(t)
	bridge := openPeopleTestStream(t, srv, svc, "bob", bridgeStreamQuery, 1)
	web := openPeopleTestStream(t, srv, svc, "bob", "allChannels=1&includePeople=1&replay=0", 2)
	panel := openPeopleTestStream(t, srv, svc, "bob", "channelId="+people+"&replay=0", 3)

	secret := postChat(t, srv, "alice", "private note", people)
	req, _ := http.NewRequest(http.MethodPatch, fmt.Sprintf("%s/v1/rooms/room-1/messages/%d", srv.URL, secret), strings.NewReader(`{"text":"edited secret"}`))
	req.Header.Set("X-Bnbloc-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("edit people message = %d", resp.StatusCode)
	}
	typing := channelPost(t, srv, "/v1/rooms/room-1/typing", "alice", fmt.Sprintf(`{"channelId":%q}`, people))
	if typing.StatusCode != http.StatusOK {
		t.Fatalf("typing = %d", typing.StatusCode)
	}
	marker := postChat(t, srv, "alice", "public marker", "ch_a")

	isMarker := func(ev wire.Event) bool { return ev.Kind == "chat_message" && ev.Sequence == marker }
	for _, ev := range collectUntil(t, bridge, isMarker) {
		if ev.ChannelID == people {
			t.Fatalf("bridge stream received a people-chat event: %+v", ev)
		}
		if ev.Kind == "message_edited" || ev.Kind == "presence_typing" {
			t.Fatalf("bridge stream received %s for the people chat: %+v", ev.Kind, ev)
		}
	}
	kinds := map[string]bool{}
	for _, ev := range collectUntil(t, web, isMarker) {
		if ev.ChannelID == people {
			kinds[ev.Kind] = true
		}
	}
	if !kinds["chat_message"] || !kinds["message_edited"] {
		t.Fatalf("web stream people-chat kinds = %v, want chat_message and message_edited", kinds)
	}
	var panelSaw bool
	deadline := time.After(4 * time.Second)
	for !panelSaw {
		select {
		case ev := <-panel:
			if ev.ChannelID != "" && ev.ChannelID != people {
				t.Fatalf("people panel stream received channel %q", ev.ChannelID)
			}
			panelSaw = ev.Kind == "chat_message" && ev.Sequence == secret
		case <-deadline:
			t.Fatal("people panel stream never received the people message")
		}
	}
}

// chat_message.v1 plugin hooks fire for ordinary channels, never for the
// people chat.
func TestPeopleChatSkipsPluginHooks(t *testing.T) {
	srv, svc, people := newPeopleChatServer(t)
	f := newPluginFixture(t, nil, "acme-hook")
	svc.PluginHooks = f.dispatcher
	st := f.subscribe(t, "bob", tenant.Default)

	postChat(t, srv, "alice", "private", people)
	expectNoDelivery(t, st)
	postChat(t, srv, "alice", "public", "ch_a")
	if env := decodeEnvelope(t, expectDelivery(t, st)); env.RoomID != "room-1" {
		t.Fatalf("delivery = %+v", env)
	}
	expectNoDelivery(t, st)
}

// The people chat counts in unread like any channel, under its own id, so
// the room header can badge it.
func TestPeopleChatUnread(t *testing.T) {
	srv, _, people := newPeopleChatServer(t)
	postChat(t, srv, "alice", "p1", people)
	postChat(t, srv, "alice", "p2", people)
	res := fetchUnread(t, srv, "bob")
	var got int
	for _, c := range res.Channels["room-1"] {
		if c.ChannelID == people {
			got = c.Count
		}
	}
	if got != 2 || res.Unread["room-1"] != 2 {
		t.Fatalf("people unread = %d (room %d), want 2", got, res.Unread["room-1"])
	}
}

// A handoff import never writes an agent reply into the people chat (the
// people-chat rule, store.ChannelAdmitsAgents); people chat is imported.
func TestImportRefusesAgentRepliesInPeopleChat(t *testing.T) {
	srv, _, people := newPeopleChatServer(t)
	batch := func(kind, payload string) string {
		return fmt.Sprintf(`{"fromRoomId":"src","messages":[{"sequence":1,"kind":%q,"channelId":%q,"payload":%s}]}`, kind, people, payload)
	}
	if r := channelPost(t, srv, "/internal/v1/rooms/room-1/import", "alice", batch(wire.KindAgentMessages, `{"ownerUserId":"bob","provider":"codex","messages":[]}`)); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("agent reply into the people chat = %d, want 400", r.StatusCode)
	}
	if r := channelPost(t, srv, "/internal/v1/rooms/room-1/import", "alice", batch(wire.KindChatMessage, `{"userId":"bob","text":"hi"}`)); r.StatusCode != http.StatusOK {
		t.Fatalf("people chat message = %d, want 200", r.StatusCode)
	}
}
