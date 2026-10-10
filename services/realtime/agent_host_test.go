package realtimehttp

import (
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A platform agent's host holds an agent-host membership
// (docs/prd/2026-10-10-zero-install-onboarding.md §2): its bridge streams the
// room and reads history, but every human-only surface refuses it.
func TestAgentHostIsNotAHumanMember(t *testing.T) {
	srv, svc, people := newPeopleChatServer(t)
	mem := svc.Membership.(*membership.Static)
	if err := mem.PutAgentHost(t.Context(), "", "room-1", "root"); err != nil {
		t.Fatal(err)
	}

	// No chat, no typing, no schedules as the host.
	if resp := channelPost(t, srv, "/v1/rooms/room-1/chat", "root", `{"text":"hi"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("chat as agent host = %d, want 403", resp.StatusCode)
	}
	if resp := channelPost(t, srv, "/v1/rooms/room-1/typing", "root", `{}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("typing as agent host = %d, want 403", resp.StatusCode)
	}

	// History of an ordinary channel is readable (the bridge's .room/
	// context); the people chat is not.
	if body := channelGet(t, srv, "/v1/rooms/room-1/history?channelId=ch_a", "root"); !strings.Contains(body, "messages") {
		t.Fatalf("history = %s", body)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1/history?channelId="+people, nil)
	req.Header.Set("X-Bnbloc-User", "root")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("people-chat history as agent host = %d, want 403", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1/events?channelId="+people, nil)
	req.Header.Set("X-Bnbloc-User", "root")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("people-chat stream as agent host = %d, want 403", resp.StatusCode)
	}

	// An all-channels stream that asks for presence, a roster and the
	// people chat gets none of them.
	events := openPeopleTestStream(t, srv, svc, "root", "allChannels=1&includePeople=1&agents=nimbus/codex", 1)
	if got := svc.Presence.Members("room-1"); len(got) != 0 {
		t.Fatalf("presence = %v; an agent host is never present", got)
	}
	if got := svc.Roster.Snapshot("room-1"); len(got) != 0 {
		t.Fatalf("roster = %+v; an agent host advertises nothing", got)
	}
	if resp := channelPost(t, srv, "/v1/rooms/room-1/chat", "alice", `{"text":"people only","channelId":"`+people+`"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("people post = %d", resp.StatusCode)
	}
	if resp := channelPost(t, srv, "/v1/rooms/room-1/chat", "alice", `{"text":"for agents","channelId":"ch_a"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("channel post = %d", resp.StatusCode)
	}
	seen := collectUntil(t, events, func(ev wire.Event) bool { return ev.Kind == "chat_message" && ev.ChannelID == "ch_a" })
	for _, ev := range seen {
		if ev.ChannelID == people {
			t.Fatalf("the agent host's stream carried a people-chat event: %+v", ev)
		}
	}
}
