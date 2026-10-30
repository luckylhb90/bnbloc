package realtimehttp

import (
	"net/http"
	"strings"
	"testing"

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

// A lanes stream (channels §2.4): full events for the open channel, one
// coalesced channel_activity for another (a mention marked), re-scoped by
// a scope request without reopening; the scope is the caller's own.
func TestLanesStream(t *testing.T) {
	srv, svc := newChannelTestServer(t)
	bob := openPeopleTestStream(t, srv, svc, "bob", "lanes=1&open=ch_a", 1)
	first := <-bob
	opened, _ := wire.DecodePayload[wire.StreamOpenedPayload](first.Payload)
	if first.Kind != wire.EventStreamOpened || opened.StreamID == "" || len(opened.Open) != 1 || opened.Open[0] != "ch_a" {
		t.Fatalf("first event = %+v", first)
	}

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
	if firstActivity == nil || !firstActivity.Mention || firstActivity.Events != 1 {
		t.Fatalf("the first activity of ch_b = %+v, want one event mentioning bob", firstActivity)
	}
	// The second message of the window comes coalesced, a second later.
	var coalesced wire.ChannelActivityPayload
	collectUntil(t, bob, func(ev wire.Event) bool {
		coalesced = activityOf(ev)
		return ev.Kind == wire.EventChannelActivity
	})
	if coalesced.ChannelID != "ch_b" || coalesced.LastSeq != seqB || coalesced.Events != 1 || coalesced.Mention {
		t.Fatalf("the coalesced activity = %+v, want ch_b's second message (seq %d)", coalesced, seqB)
	}

	// Re-scope to ch_b: its messages arrive in full, ch_a's as activity.
	scope := func(user, body string) int {
		return statusOf(t, http.MethodPost, srv.URL+"/v1/rooms/room-1/events/scope", user, body)
	}
	if code := scope("bob", `{"streamId":"`+opened.StreamID+`","open":["ch_b"]}`); code != http.StatusNoContent {
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
	}
	if !full {
		t.Fatalf("ch_b's message did not arrive in full after re-scoping: %+v", got)
	}
	if code := scope("alice", `{"streamId":"`+opened.StreamID+`","open":["ch_a"]}`); code != http.StatusNotFound {
		t.Fatalf("another member re-scoping bob's stream = %d, want 404", code)
	}
	if code := scope("bob", `{"streamId":"`+opened.StreamID+`","open":["a","b","c","d","e"]}`); code != http.StatusBadRequest {
		t.Fatalf("five open channels = %d, want 400", code)
	}
}

// A members-only channel's activity never reaches a non-member's lanes
// stream (store.MayReadChannel on every lane).
func TestLanesStreamReadersOnly(t *testing.T) {
	srv, svc, _ := newMembersOnlyServer(t)
	bob := openPeopleTestStream(t, srv, svc, "bob", "lanes=1&open=ch_general", 1)
	postChat(t, srv, "alice", "the secret", "ch_b")
	postChat(t, srv, "alice", "open channel", "ch_a")
	postChat(t, srv, "alice", "general", "ch_general")
	sawOpen := false
	for _, ev := range collectUntil(t, bob, chatIn("ch_general", "general")) {
		if ev.ChannelID == "ch_b" {
			t.Fatalf("a members-only channel reached a non-member's lanes stream: %+v", ev)
		}
		sawOpen = sawOpen || (ev.Kind == wire.EventChannelActivity && ev.ChannelID == "ch_a")
	}
	if !sawOpen {
		t.Fatal("an open channel's activity did not reach the stream")
	}
}
