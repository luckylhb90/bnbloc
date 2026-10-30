package realtimehttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/search"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// newMembersOnlyServer is the channel test server with ch_b in a members-only
// section whose members are alice and carl: bob is a member of the room, not
// of the section (docs/prd/2026-10-28-channels-and-threads.md C11).
func newMembersOnlyServer(t *testing.T) (*httptest.Server, *Service, *store.MemoryChannel) {
	t.Helper()
	srv, svc := newChannelTestServer(t)
	svc.Membership.(*membership.Static).Add("room-1", "carl")
	svc.Tasks = store.NewMemoryMessageTasks()
	ch := svc.Channels.(*store.MemoryChannel)
	if err := ch.PutSection(dctx(), store.RoomSection{SectionID: "sec_x", RoomID: "room-1", Name: "Secret",
		Visibility: wire.SectionMembers, JoinPolicy: store.SectionJoinInvite, Owners: []string{"alice"}, CreatedBy: "alice", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	sec := "sec_x"
	if err := ch.UpdateChannel(dctx(), "room-1", "ch_b", store.ChannelPatch{SectionID: &sec}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"alice", "carl"} {
		if err := ch.AddSectionMember(dctx(), store.SectionMember{RoomID: "room-1", SectionID: sec, UserID: u, AddedBy: "alice", JoinedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
	}
	return srv, svc, ch
}

func statusOf(t *testing.T, method, url, user, body string) int {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("X-Bnbloc-User", user)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// History, posting, editing, tasks, unread and revisions: a non-member of
// the section reads, posts and counts nothing of its channel; a member does.
func TestMembersOnlyChannelRESTPaths(t *testing.T) {
	srv, _, _ := newMembersOnlyServer(t)
	seq := postChat(t, srv, "alice", "the secret plan", "ch_b")
	if got := historyTexts(t, srv, "carl", "?channelId=ch_b"); len(got) != 1 {
		t.Fatalf("a member's history = %v", got)
	}
	base := srv.URL + "/v1/rooms/room-1"
	if code := statusOf(t, http.MethodGet, base+"/history?channelId=ch_b", "bob", ""); code != http.StatusNotFound {
		t.Fatalf("a non-member's history of the section's channel = %d, want 404", code)
	}
	if code := statusOf(t, http.MethodPost, base+"/chat", "bob", `{"text":"let me in","channelId":"ch_b"}`); code != http.StatusNotFound {
		t.Fatalf("a non-member's post into the section's channel = %d, want 404", code)
	}
	if code := statusOf(t, http.MethodPatch, base+"/messages/"+itoaTest(seq), "bob", `{"text":"x"}`); code != http.StatusNotFound {
		t.Fatalf("a non-member editing the section's message = %d, want 404", code)
	}
	// Tasks: a non-member can neither mark nor list the section's message.
	if code := statusOf(t, http.MethodPut, base+"/tasks/"+itoaTest(seq), "bob", `{"status":"open"}`); code != http.StatusNotFound {
		t.Fatalf("a non-member marking the section's message = %d, want 404", code)
	}
	if code := statusOf(t, http.MethodPut, base+"/tasks/"+itoaTest(seq), "carl", `{"status":"open"}`); code != http.StatusOK {
		t.Fatalf("a member marking a task = %d", code)
	}
	if body := channelGet(t, srv, "/v1/rooms/room-1/tasks", "bob"); strings.Contains(body, "secret") {
		t.Fatalf("a non-member's task list holds the section's task: %s", body)
	}
	if body := channelGet(t, srv, "/v1/rooms/room-1/tasks", "carl"); !strings.Contains(body, "secret") {
		t.Fatalf("a member's task list misses the task: %s", body)
	}
	for _, c := range fetchUnread(t, srv, "bob").Channels["room-1"] {
		if c.ChannelID == "ch_b" {
			t.Fatal("a non-member counts the section's channel as unread")
		}
	}
	found := false
	for _, c := range fetchUnread(t, srv, "carl").Channels["room-1"] {
		found = found || c.ChannelID == "ch_b"
	}
	if !found {
		t.Fatal("a member's unread misses the section's channel")
	}
	if code := statusOf(t, http.MethodDelete, base+"/messages/"+itoaTest(seq), "alice", ""); code != http.StatusOK {
		t.Fatalf("the author's delete = %d", code)
	}
	if body := channelGet(t, srv, "/v1/rooms/room-1/history/revisions?after=0", "bob"); strings.Contains(body, "ch_b") {
		t.Fatalf("a non-member's revisions name the section's channel: %s", body)
	}
	if body := channelGet(t, srv, "/v1/rooms/room-1/history/revisions?after=0", "carl"); !strings.Contains(body, "ch_b") {
		t.Fatalf("a member's revisions miss the section's channel: %s", body)
	}
}

// The room stream: a non-member's all-channels stream never carries the
// section's events; a member's does; a member who leaves stops receiving
// them at once (the membership event re-scopes the open stream); a
// one-channel stream of the section is refused to a non-member.
func TestMembersOnlyChannelStream(t *testing.T) {
	srv, svc, ch := newMembersOnlyServer(t)
	bob := openPeopleTestStream(t, srv, svc, "bob", "allChannels=1&includePeople=1", 1)
	carl := openPeopleTestStream(t, srv, svc, "carl", "allChannels=1&includePeople=1", 2)
	postChat(t, srv, "alice", "secret", "ch_b")
	postChat(t, srv, "alice", "open", "ch_a")
	isOpen := func(text string) func(wire.Event) bool {
		return func(ev wire.Event) bool {
			return ev.Kind == wire.KindChatMessage && ev.ChannelID == "ch_a" && strings.Contains(string(mustJSON(ev.Payload)), text)
		}
	}
	for _, ev := range collectUntil(t, bob, isOpen("open")) {
		if ev.ChannelID == "ch_b" {
			t.Fatalf("a non-member's stream carried the section's event: %+v", ev)
		}
	}
	saw := false
	for _, ev := range collectUntil(t, carl, isOpen("open")) {
		saw = saw || ev.ChannelID == "ch_b"
	}
	if !saw {
		t.Fatal("a member's stream misses the section's event")
	}
	if err := ch.RemoveSectionMember(dctx(), "room-1", "sec_x", "carl"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.publish(dctx(), wire.Event{RoomID: "room-1", Kind: wire.EventSectionMembership,
		Payload: wire.SectionMembershipPayload{SectionID: "sec_x", UserID: "carl", Action: "left", By: "carl"}, AtUnixMs: time.Now().UnixMilli()}, realtime.Durable{}); err != nil {
		t.Fatal(err)
	}
	postChat(t, srv, "alice", "after carl left", "ch_b")
	postChat(t, srv, "alice", "open again", "ch_a")
	for _, ev := range collectUntil(t, carl, isOpen("open again")) {
		if ev.Kind == wire.KindChatMessage && ev.ChannelID == "ch_b" {
			t.Fatalf("a member who left still receives the section's events: %+v", ev)
		}
	}
	// The room state forgets carl's sections on the event: he posts there no
	// more (room_state.go).
	if code := statusOf(t, http.MethodPost, srv.URL+"/v1/rooms/room-1/chat", "carl", `{"text":"still here?","channelId":"ch_b"}`); code != http.StatusNotFound {
		t.Fatalf("a member who left posting in the section's channel = %d, want 404", code)
	}
	if code := statusOf(t, http.MethodGet, srv.URL+"/v1/rooms/room-1/events?channelId=ch_b", "bob", ""); code != http.StatusNotFound {
		t.Fatalf("a non-member's stream of the section's channel = %d, want 404", code)
	}
}

// A mention notifies, and makes follow, only a reader of the channel: bob,
// named in the section's channel, is neither told nor joined to it.
func TestMembersOnlyMentionReachesReadersOnly(t *testing.T) {
	srv, svc, ch := newMembersOnlyServer(t)
	postChat(t, srv, "alice", "@bob and @carl look", "ch_b")
	follows := func(user string) bool {
		fs, err := ch.ChannelFollows(dctx(), "room-1", user)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fs {
			if f.ChannelID == "ch_b" {
				return true
			}
		}
		return false
	}
	if follows("bob") || !follows("carl") {
		t.Fatalf("a mention joins a reader only: bob %v carl %v", follows("bob"), follows("carl"))
	}
	if svc.memberMayReadChannel(dctx(), "room-1", "ch_b", "bob") || !svc.memberMayReadChannel(dctx(), "room-1", "ch_b", "carl") {
		t.Fatal("the notification gate (Observer.MayRead) reads the rule")
	}
}

func mustJSON(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}

// Global search: a non-member of the section finds nothing of its channel;
// its member does (store.MayReadChannel).
func TestMembersOnlySearch(t *testing.T) {
	srv, svc, _ := newMembersOnlyServer(t)
	svc.Search = search.NewStore(svc.Chat.(*store.MemoryChat))
	svc.Rooms = svc.Membership.(*membership.Static)
	postChat(t, srv, "alice", "launch codename lantern", "ch_b")
	postChat(t, srv, "alice", "lantern is public here", "ch_a")
	hits := func(user string) string {
		body := channelGet(t, srv, "/v1/search?q=lantern", user)
		var out wire.SearchResponse
		_ = json.Unmarshal([]byte(body), &out)
		var chans []string
		for _, r := range out.Results {
			chans = append(chans, r.ChannelID)
		}
		return strings.Join(chans, ",")
	}
	if got := hits("bob"); strings.Contains(got, "ch_b") || !strings.Contains(got, "ch_a") {
		t.Fatalf("a non-member's search = %s", got)
	}
	if got := hits("carl"); !strings.Contains(got, "ch_b") {
		t.Fatalf("a member's search = %s", got)
	}
}
