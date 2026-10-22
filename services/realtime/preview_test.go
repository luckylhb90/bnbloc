package realtimehttp

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// previewGet is a GET as user, answering the status.
func previewGet(t *testing.T, srv *httptest.Server, path, user string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	req.Header.Set("X-Bnbloc-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// A non-member of a shared room reads its history and live stream as a
// preview (docs/prd/2026-10-22-shared-room-preview.md): never the people
// chat, never present, never in the roster, and no write; a room that is not
// shared stays closed.
func TestPreviewReadsASharedRoomLikeAnAgentHostDoes(t *testing.T) {
	srv, svc, people := newPeopleChatServer(t)
	mem := svc.Membership.(*membership.Static)

	if got := previewGet(t, srv, "/v1/rooms/room-1/history?channelId=ch_a", "jack"); got != http.StatusForbidden {
		t.Fatalf("history of a room that is not shared = %d, want 403", got)
	}
	if got := previewGet(t, srv, "/v1/rooms/room-1/events?channelId=ch_a", "jack"); got != http.StatusForbidden {
		t.Fatalf("stream of a room that is not shared = %d, want 403", got)
	}

	mem.SetPreviewable("room-1", true)
	if got := previewGet(t, srv, "/v1/rooms/room-1/history?channelId=ch_a", "jack"); got != http.StatusOK {
		t.Fatalf("history of a shared room by a non-member = %d, want 200", got)
	}
	for _, path := range []string{
		"/v1/rooms/room-1/history?channelId=" + people,
		"/v1/rooms/room-1/events?channelId=" + people,
	} {
		if got := previewGet(t, srv, path, "jack"); got != http.StatusForbidden {
			t.Errorf("GET %s by a preview = %d, want 403 (the people chat is for people)", path, got)
		}
	}
	// Reading is all a preview does: every other route still wants a member.
	for _, c := range []struct{ path, body string }{
		{"/v1/rooms/room-1/chat", `{"text":"hi","channelId":"ch_a"}`},
		{"/v1/rooms/room-1/typing", `{}`},
		{"/v1/rooms/room-1/read", `{}`},
	} {
		if resp := channelPost(t, srv, c.path, "jack", c.body); resp.StatusCode != http.StatusForbidden {
			t.Errorf("POST %s by a preview = %d, want 403", c.path, resp.StatusCode)
		}
	}
	for _, path := range []string{"/v1/rooms/room-1/agents", "/v1/rooms/room-1/tasks"} {
		if got := previewGet(t, srv, path, "jack"); got != http.StatusForbidden {
			t.Errorf("GET %s by a preview = %d, want 403 (members only)", path, got)
		}
	}

	// A stream that asks for presence, a roster and the people chat gets
	// none of them, and carries the channels' events.
	events := openPeopleTestStream(t, srv, svc, "jack", "allChannels=1&includePeople=1&agents=nimbus/codex", 1)
	if got := svc.Presence.Members("room-1"); len(got) != 0 {
		t.Fatalf("presence = %v; a preview is never present", got)
	}
	if got := svc.Roster.Snapshot("room-1"); len(got) != 0 {
		t.Fatalf("roster = %+v; a preview advertises nothing", got)
	}
	postChat(t, srv, "alice", "people only", people)
	marker := postChat(t, srv, "alice", "for everyone", "ch_a")
	for _, ev := range collectUntil(t, events, func(ev wire.Event) bool { return ev.Kind == "chat_message" && ev.Sequence == marker }) {
		if ev.ChannelID == people {
			t.Fatalf("a preview's stream carried a people-chat event: %+v", ev)
		}
	}
}

// A room made private ends its previews: the stream re-checks with Read at
// every heartbeat.
func TestPreviewStreamEndsWhenTheRoomStopsBeingShared(t *testing.T) {
	srv, svc, _ := newPeopleChatServer(t)
	mem := svc.Membership.(*membership.Static)
	mem.SetPreviewable("room-1", true)
	svc.HeartbeatEvery = 20 * time.Millisecond

	events := openPeopleTestStream(t, srv, svc, "jack", "channelId=ch_a", 1)
	// Many heartbeats pass while the room is shared: the stream stays open.
	settle := time.After(300 * time.Millisecond)
hold:
	for {
		select {
		case _, open := <-events:
			if !open {
				t.Fatal("the preview's stream ended while the room was still shared")
			}
		case <-settle:
			break hold
		}
	}
	mem.SetPreviewable("room-1", false)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, open := <-events:
			if !open {
				return
			}
		case <-deadline:
			t.Fatal("the preview's stream outlived the room's sharing")
		}
	}
}

// A preview's stream carries the feed and the deliverables, not the room's
// agent runs, tasks, live views, rosters or app changes: those are members'
// (wire.PreviewedKind), as the REST routes are.
func TestPreviewStreamCarriesOnlyThePreviewedKinds(t *testing.T) {
	srv, svc, _ := newPeopleChatServer(t)
	mem := svc.Membership.(*membership.Static)
	mem.SetPreviewable("room-1", true)
	events := openPeopleTestStream(t, srv, svc, "jack", "allChannels=1&replay=0", 1)

	for _, kind := range []string{
		wire.EventAgentJobCreated, wire.EventAgentJobUpdated, wire.EventAgentJobProgress, wire.EventJobLiveFrame,
		wire.EventAgentSessionState, wire.EventAgentRoster, wire.EventTaskUpdated, wire.EventContentSaved, wire.EventAppChanged,
	} {
		svc.Hub.Publish(wire.Event{RoomID: "room-1", Kind: kind, ChannelID: "ch_a", Payload: map[string]any{"secret": kind}})
	}
	marker := postChat(t, srv, "alice", "for everyone", "ch_a")
	for _, ev := range collectUntil(t, events, func(ev wire.Event) bool { return ev.Kind == "chat_message" && ev.Sequence == marker }) {
		if !wire.PreviewedKind(ev.Kind) {
			t.Errorf("a preview's stream carried %s: %+v", ev.Kind, ev)
		}
	}
}

// A member removed from a shared room has no access to it as a member: the
// stream they opened as one ends at the next heartbeat, though the room
// itself stays readable as a preview.
func TestMemberStreamEndsWhenRemovedFromASharedRoom(t *testing.T) {
	srv, svc, _ := newPeopleChatServer(t)
	mem := svc.Membership.(*membership.Static)
	mem.SetPreviewable("room-1", true)
	svc.HeartbeatEvery = 20 * time.Millisecond

	events := openPeopleTestStream(t, srv, svc, "bob", "channelId=ch_a", 1)
	mem.Remove("room-1", "bob")
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, open := <-events:
			if !open {
				return
			}
		case <-deadline:
			t.Fatal("a removed member's stream outlived the membership")
		}
	}
}
