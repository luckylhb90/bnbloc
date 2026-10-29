package room

import (
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A forum channel's switches (§2.1c, C20): its owners define the posting
// style, tags, statuses and a post template (validated by the one settings
// owner); any member who may post marks a thread of it (tags and a status
// of the channel's own vocabulary, an answer), never a visitor; marks are
// a forum's only; a closed thread is read-only.
func TestForumSettingsAndThreadMarks(t *testing.T) {
	e := newSetupEnv(t)
	bugs := e.create("bob", wire.ChannelCreateRequest{Name: "bugs"}, http.StatusOK)
	th := seedThread(t, e, bugs.ChannelID, 4, "login fails", "carol")
	mark := func(user string, body any, want int) wire.RoomChannel {
		var out wire.RoomChannel
		e.call(http.MethodPatch, "/v1/rooms/room-1/channel-threads/"+th, user, body, want, &out)
		return out
	}
	// Not a forum yet: no marks.
	mark("carol", map[string]any{"status": "open"}, http.StatusConflict)

	for _, bad := range []map[string]any{
		{"postingStyle": "board"},
		{"tags": []map[string]string{{"id": "a b", "name": "A"}}},
		{"tags": []map[string]string{{"id": "a", "name": "A"}, {"id": "a", "name": "B"}}},
		{"statuses": []map[string]string{{"id": "s", "name": ""}}},
		{"postTemplate": strings.Repeat("x", wire.MaxPostTemplate+1)},
	} {
		e.patch("bob", bugs.ChannelID, map[string]any{"settings": bad}, http.StatusBadRequest)
	}
	settings := map[string]any{
		"postingStyle": "threads",
		"tags":         []map[string]string{{"id": "ios", "name": " iOS "}, {"id": "web", "name": "Web", "color": "#3b82f6"}},
		"statuses":     []map[string]string{{"id": "open", "name": "Open"}, {"id": "done", "name": "Done"}},
		"postTemplate": "## Steps\n\n## Expected\n",
	}
	e.patch("carol", bugs.ChannelID, map[string]any{"settings": settings}, http.StatusForbidden)
	got := e.patch("bob", bugs.ChannelID, map[string]any{"settings": settings}, http.StatusOK)
	if !got.Settings.Forum() || len(got.Settings.Tags) != 2 || got.Settings.Tags[0].Name != "iOS" || len(got.Settings.Statuses) != 2 || got.Settings.PostTemplate == "" {
		t.Fatalf("forum settings = %+v", got.Settings)
	}

	// Any member who may post marks it, with the channel's vocabulary only.
	mark("carol", map[string]any{"tags": []string{"android"}}, http.StatusBadRequest)
	mark("carol", map[string]any{"status": "wip"}, http.StatusBadRequest)
	mark("vic", map[string]any{"status": "open"}, http.StatusForbidden)
	out := mark("carol", map[string]any{"tags": []string{"ios", "web", "ios"}, "status": "open"}, http.StatusOK)
	if out.Thread == nil || len(out.Thread.Tags) != 2 || out.Thread.Status != "open" {
		t.Fatalf("marked thread = %+v", out.Thread)
	}
	out = mark("bob", map[string]any{"status": "done", "answeredSeq": 9}, http.StatusOK)
	if out.Thread.Status != "done" || out.Thread.AnsweredSeq != 9 || len(out.Thread.Tags) != 2 {
		t.Fatalf("a second mark = %+v", out.Thread)
	}
	if e.pub.count(wire.EventThreadUpdated) == 0 {
		t.Fatal("a mark does not tell the parent's stream")
	}
	if l := e.threads("alice", "?parent="+bugs.ChannelID); len(l) != 1 || l[0].Thread.Status != "done" {
		t.Fatalf("the listing's marks = %+v", l)
	}
	// A closed thread is read-only.
	e.call(http.MethodPost, "/v1/rooms/room-1/channels/"+th+"/archive", "carol", nil, http.StatusOK, nil)
	mark("carol", map[string]any{"status": "open"}, http.StatusConflict)
}

// A thread of a members-only channel is not found for a non-member.
func TestThreadMarksOfMembersOnlyChannels(t *testing.T) {
	m := newMembersOnlyEnv(t)
	m.patch("bob", m.plans.ChannelID, map[string]any{"settings": map[string]any{"postingStyle": "threads", "statuses": []map[string]string{{"id": "open", "name": "Open"}}}}, http.StatusOK)
	th := seedThread(t, m.setupEnv, m.plans.ChannelID, 3, "secret plan", "bob")
	m.call(http.MethodPatch, "/v1/rooms/room-1/channel-threads/"+th, "carol", map[string]any{"status": "open"}, http.StatusNotFound, nil)
	m.call(http.MethodPatch, "/v1/rooms/room-1/channel-threads/"+th, "bob", map[string]any{"status": "open"}, http.StatusOK, nil)
}
