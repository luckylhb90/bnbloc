package realtimehttp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/search"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// openThread opens the thread of rootSeq in parent as user (status and the
// thread).
func openThread(t *testing.T, base, user, parent string, rootSeq int64, title string) (int, wire.RoomChannel) {
	t.Helper()
	buf, _ := json.Marshal(wire.ThreadOpenRequest{ParentID: parent, RootSeq: rootSeq, Title: title})
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/rooms/room-1/threads", strings.NewReader(string(buf)))
	req.Header.Set("X-Bnbloc-User", user)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var th wire.RoomChannel
	_ = json.NewDecoder(resp.Body).Decode(&th)
	return resp.StatusCode, th
}

// A thread (channels PRD §2.2, C1, C2) opens from a message of a channel
// the opener may post in, once per root (opening again returns it); its
// own history, unread, typing and search work per channel unchanged; a
// reply counts on the root and tells the parent's stream; a reply may name
// the message it answers (replyTo), quoted as it read.
func TestThreadsOpenReplyAndCount(t *testing.T) {
	srv, svc := newChannelTestServer(t)
	svc.Membership.(*membership.Static).Add("room-1", "carl")
	root := postChat(t, srv, "alice", "the login bug is back", "ch_a")
	parentStream := openPeopleTestStream(t, srv, svc, "carl", "channelId=ch_a", 1)

	code, th := openThread(t, srv.URL, "bob", "ch_a", root, "")
	if code != http.StatusOK || th.Kind != wire.ChannelKindThread || th.Thread == nil || th.Thread.ParentID != "ch_a" ||
		th.Thread.RootSeq != root || th.Name != "the login bug is back" || !th.Joined {
		t.Fatalf("open = %d %+v", code, th)
	}
	if code, again := openThread(t, srv.URL, "carl", "ch_a", root, "another title"); code != http.StatusOK || again.ChannelID != th.ChannelID {
		t.Fatalf("opening again = %d %+v, want the same thread", code, again)
	}
	// Refusals: no such message in that channel, a thread of a thread, a visitor.
	if code, _ := openThread(t, srv.URL, "bob", "ch_b", root, ""); code != http.StatusNotFound {
		t.Fatalf("a root of another channel = %d, want 404", code)
	}
	if code, _ := openThread(t, srv.URL, "bob", "ch_a", 999, ""); code != http.StatusNotFound {
		t.Fatalf("no such root = %d, want 404", code)
	}
	reply := postChat(t, srv, "bob", "I can reproduce it", th.ChannelID)
	if code, _ := openThread(t, srv.URL, "bob", th.ChannelID, reply, ""); code != http.StatusBadRequest {
		t.Fatalf("a thread of a thread = %d, want 400", code)
	}
	svc.Membership.(*membership.Static).AddRole("room-1", "vic", store.RoleVisitor)
	if code, _ := openThread(t, srv.URL, "vic", "ch_a", root, ""); code != http.StatusForbidden {
		t.Fatalf("a visitor opening a thread = %d, want 403", code)
	}

	// The thread's own history holds its replies only; the parent's does not.
	if got := historyTexts(t, srv, "alice", "?channelId="+th.ChannelID); len(got) != 1 || got[0] != "I can reproduce it" {
		t.Fatalf("thread history = %v", got)
	}
	if got := historyTexts(t, srv, "alice", "?channelId=ch_a"); len(got) != 1 {
		t.Fatalf("parent history = %v", got)
	}
	// Its unread is its own channel's.
	found := false
	for _, c := range fetchUnread(t, srv, "alice").Channels["room-1"] {
		found = found || (c.ChannelID == th.ChannelID && c.Count == 1)
	}
	if !found {
		t.Fatalf("alice's unread misses the thread: %+v", fetchUnread(t, srv, "alice"))
	}
	// Typing in a thread is scoped to it.
	if code := statusOf(t, http.MethodPost, srv.URL+"/v1/rooms/room-1/typing", "bob", `{"channelId":"`+th.ChannelID+`"}`); code != http.StatusOK {
		t.Fatalf("typing in a thread = %d", code)
	}

	// The reply counted on the root, told to the parent's stream.
	var last wire.ThreadUpdatedPayload
	deadline := time.After(4 * time.Second)
	for last.Thread.Replies < 1 {
		select {
		case ev, ok := <-parentStream:
			if !ok {
				t.Fatal("stream closed")
			}
			if ev.Kind != wire.EventThreadUpdated {
				continue
			}
			if ev.ChannelID != "ch_a" {
				t.Fatalf("thread_updated scoped to %q, want the parent", ev.ChannelID)
			}
			last, _ = wire.DecodePayload[wire.ThreadUpdatedPayload](ev.Payload)
		case <-deadline:
			t.Fatalf("the parent's stream saw no reply count; last %+v", last)
		}
	}
	if last.ThreadID != th.ChannelID || last.Thread.Replies != 1 || len(last.Thread.Participants) != 1 || last.Thread.Participants[0] != "bob" {
		t.Fatalf("the parent's stream saw %+v", last)
	}
	got, _, _ := svc.Channels.(*store.MemoryChannel).GetChannel(dctx(), "room-1", th.ChannelID)
	if got.Thread.Replies != 1 {
		t.Fatalf("stored stats = %+v", got.Thread)
	}

	// replyTo: a message of the same channel, or the thread's root; never another channel's.
	resp := channelPost(t, srv, "/v1/rooms/room-1/chat", "carl", `{"text":"which build?","channelId":"`+th.ChannelID+`","replyTo":`+itoaTest(reply)+`}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a reply to a thread message = %d", resp.StatusCode)
	}
	resp = channelPost(t, srv, "/v1/rooms/room-1/chat", "carl", `{"text":"about the root","channelId":"`+th.ChannelID+`","replyTo":`+itoaTest(root)+`}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a reply to the thread's root = %d", resp.StatusCode)
	}
	other := postChat(t, srv, "alice", "elsewhere", "ch_b")
	resp = channelPost(t, srv, "/v1/rooms/room-1/chat", "carl", `{"text":"x","channelId":"ch_a","replyTo":`+itoaTest(other)+`}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a reply to another channel's message = %d, want 400", resp.StatusCode)
	}
	body := channelGet(t, srv, "/v1/rooms/room-1/history?channelId="+th.ChannelID, "alice")
	if !strings.Contains(body, `"replyTo":{"seq":`+itoaTest(root)+`,"author":"alice","excerpt":"the login bug is back"}`) {
		t.Fatalf("the reply's quote is not stored as it read: %s", body)
	}

	// Search finds a thread's message like any channel's.
	svc.Search = search.NewStore(svc.Chat.(*store.MemoryChat))
	svc.Rooms = svc.Membership.(*membership.Static)
	if body := channelGet(t, srv, "/v1/search?q=reproduce", "alice"); !strings.Contains(body, th.ChannelID) {
		t.Fatalf("search misses the thread's message: %s", body)
	}
}

// A thread of a members-only channel is its parent's: a non-member of the
// section neither opens nor reads it, nor sees its root's line (C11, C14 by
// the one rule: the thread carries its parent's section).
func TestThreadOfMembersOnlyChannel(t *testing.T) {
	srv, svc, _ := newMembersOnlyServer(t)
	root := postChat(t, srv, "alice", "the secret plan", "ch_b")
	if code, _ := openThread(t, srv.URL, "bob", "ch_b", root, ""); code != http.StatusNotFound {
		t.Fatalf("a non-member opening a thread there = %d, want 404", code)
	}
	code, th := openThread(t, srv.URL, "carl", "ch_b", root, "")
	if code != http.StatusOK || th.SectionID != "sec_x" {
		t.Fatalf("a member opens it = %d %+v", code, th)
	}
	postChat(t, srv, "carl", "step one", th.ChannelID)
	base := srv.URL + "/v1/rooms/room-1"
	if code := statusOf(t, http.MethodGet, base+"/history?channelId="+th.ChannelID, "bob", ""); code != http.StatusNotFound {
		t.Fatalf("a non-member's history of the thread = %d, want 404", code)
	}
	if code := statusOf(t, http.MethodPost, base+"/chat", "bob", `{"text":"x","channelId":"`+th.ChannelID+`"}`); code != http.StatusNotFound {
		t.Fatalf("a non-member's post in the thread = %d, want 404", code)
	}
	for _, c := range fetchUnread(t, srv, "bob").Channels["room-1"] {
		if c.ChannelID == th.ChannelID {
			t.Fatal("a non-member counts the thread as unread")
		}
	}
	bob := openPeopleTestStream(t, srv, svc, "bob", "allChannels=1&includePeople=1", 1)
	postChat(t, srv, "carl", "step two", th.ChannelID)
	postChat(t, srv, "alice", "public marker", "ch_a")
	for _, ev := range collectUntil(t, bob, func(ev wire.Event) bool {
		return ev.Kind == wire.KindChatMessage && strings.Contains(string(mustJSON(ev.Payload)), "public marker")
	}) {
		if ev.ChannelID == th.ChannelID || ev.Kind == wire.EventThreadUpdated {
			t.Fatalf("a non-member's stream carried the thread: %+v", ev)
		}
	}
	_ = realtime.Durable{}
}

// A thread's unread is its followers' (C4): who wrote in it, was mentioned
// in it, or chose to follow it; anyone else sees its root's line only.
func TestThreadUnreadIsItsFollowers(t *testing.T) {
	srv, svc := newChannelTestServer(t)
	svc.Membership.(*membership.Static).Add("room-1", "carl")
	root := postChat(t, srv, "alice", "plan for friday", "ch_a")
	_, th := openThread(t, srv.URL, "bob", "ch_a", root, "")
	counts := func(user string) map[string]int {
		out := map[string]int{}
		for _, c := range fetchUnread(t, srv, user).Channels["room-1"] {
			out[c.ChannelID] = c.Count
		}
		return out
	}
	postChat(t, srv, "bob", "I take the slides", th.ChannelID)
	// alice wrote the root: she follows the thread from its opening.
	if counts("alice")[th.ChannelID] != 1 {
		t.Fatalf("the root's author's unread = %v", counts("alice"))
	}
	if _, ok := counts("carl")[th.ChannelID]; ok {
		t.Fatalf("a member who does not follow the thread counts it: %v", counts("carl"))
	}
	// A mention in the thread makes carl follow it.
	postChat(t, srv, "bob", "@carl can you review?", th.ChannelID)
	if counts("carl")[th.ChannelID] != 2 {
		t.Fatalf("after a mention carl's unread = %v", counts("carl"))
	}
}
