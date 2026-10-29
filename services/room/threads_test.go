package room

import (
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// seedThread puts a thread of message rootSeq of parent straight in the
// store (realtime opens threads; this service lists, renames and archives
// them), followed by its opener.
func seedThread(t *testing.T, e *setupEnv, parent string, rootSeq int64, title, opener string) string {
	t.Helper()
	p, ok, err := e.svc.Channels.GetChannel(dctx(), "room-1", parent)
	if err != nil || !ok {
		t.Fatalf("parent %s: %v %v", parent, ok, err)
	}
	th := store.ThreadOf(p, "th_"+strings.ReplaceAll(title, " ", "_"), rootSeq, title, opener, 100)
	if err := e.svc.Channels.PutChannel(dctx(), th); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Channels.FollowChannel(dctx(), "room-1", th.ChannelID, opener, 100, store.FollowMarks{}); err != nil {
		t.Fatal(err)
	}
	return th.ChannelID
}

func (e *setupEnv) threads(user, query string) []wire.RoomChannel {
	e.t.Helper()
	var out wire.ThreadList
	e.call(http.MethodGet, "/v1/rooms/room-1/channel-threads"+query, user, nil, http.StatusOK, &out)
	return out.Threads
}

// Threads are never channels of the room's list (rail, directory, the
// agents' listing); they are listed per parent and per follower, renamed by
// anyone who may post (only their title), archived by a person (C3), and
// tell their parent's stream (thread_updated, scoped to the parent).
func TestThreadsListedApartRenamedArchived(t *testing.T) {
	e := newSetupEnv(t)
	design := e.create("bob", wire.ChannelCreateRequest{Name: "design"}, http.StatusOK)
	th := seedThread(t, e, design.ChannelID, 7, "login bug", "carol")
	for _, q := range []string{"", "?agents=1", "?archived=1"} {
		for _, c := range e.list("alice", q).Channels {
			if c.ChannelID == th {
				t.Fatalf("the channel list%s holds a thread", q)
			}
		}
	}
	var dir wire.ChannelDirectory
	e.call(http.MethodGet, "/v1/rooms/room-1/channel-directory", "alice", nil, http.StatusOK, &dir)
	for _, c := range dir.Channels {
		if c.ChannelID == th {
			t.Fatal("the directory lists a thread")
		}
	}
	got := e.threads("alice", "?parent="+design.ChannelID)
	if len(got) != 1 || got[0].ChannelID != th || got[0].Thread == nil || got[0].Thread.RootSeq != 7 || got[0].Joined {
		t.Fatalf("the parent's threads for alice = %+v", got)
	}
	if got := e.threads("carol", "?followed=1"); len(got) != 1 || !got[0].Joined {
		t.Fatalf("carol's followed threads = %+v", got)
	}
	if got := e.threads("alice", "?followed=1"); len(got) != 0 {
		t.Fatalf("alice follows no thread: %+v", got)
	}
	// Follow: the channel route.
	e.call(http.MethodPut, "/v1/rooms/room-1/channels/"+th+"/follow", "alice", nil, http.StatusOK, nil)
	if got := e.threads("alice", "?followed=1"); len(got) != 1 {
		t.Fatalf("after following = %+v", got)
	}
	// Rename: anyone who may post, only the title.
	before := e.pub.count(wire.EventThreadUpdated)
	var renamed wire.RoomChannel
	e.call(http.MethodPatch, "/v1/rooms/room-1/channels/"+th, "bob", map[string]any{"name": "  login   bug on iOS "}, http.StatusOK, &renamed)
	if renamed.Name != "login bug on iOS" || renamed.Thread == nil {
		t.Fatalf("renamed = %+v", renamed)
	}
	if e.pub.count(wire.EventThreadUpdated) != before+1 {
		t.Fatal("a rename did not tell the parent's stream")
	}
	e.call(http.MethodPatch, "/v1/rooms/room-1/channels/"+th, "bob", map[string]any{"purpose": "x"}, http.StatusBadRequest, nil)
	e.call(http.MethodPatch, "/v1/rooms/room-1/channels/"+th, "vic", map[string]any{"name": "x"}, http.StatusForbidden, nil)
	// Archive: a member who is neither its owner nor an admin (C3).
	var archived wire.RoomChannel
	e.call(http.MethodPost, "/v1/rooms/room-1/channels/"+th+"/archive", "bob", nil, http.StatusOK, &archived)
	if archived.ArchivedAtMs == 0 {
		t.Fatalf("archived = %+v", archived)
	}
	e.call(http.MethodPost, "/v1/rooms/room-1/channels/"+th+"/archive", "vic", nil, http.StatusForbidden, nil)
	if got := e.threads("alice", "?parent="+design.ChannelID); len(got) != 1 || got[0].ArchivedAtMs == 0 {
		t.Fatalf("an archived thread stays listed, marked: %+v", got)
	}
	// The parent moves into another section: its thread follows (store.ThreadOf).
	var sec wire.RoomSection
	e.call(http.MethodPost, "/v1/rooms/room-1/sections", "alice", map[string]any{"name": "Product"}, http.StatusOK, &sec)
	e.patch("alice", design.ChannelID, map[string]any{"sectionId": sec.SectionID}, http.StatusOK)
	if got := e.threads("alice", "?parent="+design.ChannelID); got[0].SectionID != sec.SectionID {
		t.Fatalf("a thread after its parent moved = %+v", got[0])
	}
}

// A thread of a members-only channel exists only for the channel's readers:
// a non-member of the section finds it in no list and reaches none of its
// routes (C11, by the one rule: the thread carries its parent's section).
func TestThreadsOfMembersOnlyChannels(t *testing.T) {
	m := newMembersOnlyEnv(t)
	th := seedThread(t, m.setupEnv, m.plans.ChannelID, 3, "secret thread", "bob")
	if got := m.threads("carol", ""); len(got) != 0 {
		t.Fatalf("a non-member lists the section's thread: %+v", got)
	}
	if got := m.threads("alice", ""); len(got) != 0 {
		t.Fatalf("an owner who has not joined lists the section's thread: %+v", got)
	}
	if got := m.threads("bob", ""); len(got) != 1 || got[0].ChannelID != th {
		t.Fatalf("a member's threads = %+v", got)
	}
	m.call(http.MethodPatch, "/v1/rooms/room-1/channels/"+th, "carol", map[string]any{"name": "x"}, http.StatusNotFound, nil)
	m.call(http.MethodPost, "/v1/rooms/room-1/channels/"+th+"/archive", "carol", nil, http.StatusNotFound, nil)
	m.call(http.MethodPut, "/v1/rooms/room-1/channels/"+th+"/follow", "carol", nil, http.StatusNotFound, nil)
}

// The agents' listing names a thread only when the read asks for it
// (?thread=, the turn's own conversation) and the reader may read it: an
// agent answering in a thread reads it as a channel; a thread of a channel
// that bars agents, or of a members-only channel the reader is not in, is
// never named.
func TestAgentsListingNamesTheTurnsThread(t *testing.T) {
	m := newMembersOnlyEnv(t)
	design := m.create("bob", wire.ChannelCreateRequest{Name: "design"}, http.StatusOK)
	open := seedThread(t, m.setupEnv, design.ChannelID, 4, "open thread", "carol")
	secret := seedThread(t, m.setupEnv, m.plans.ChannelID, 3, "secret thread", "bob")
	has := func(user, thread string) bool {
		for _, c := range m.list(user, "?agents=1&thread="+thread).Channels {
			if c.ChannelID == thread {
				return c.Thread != nil
			}
		}
		return false
	}
	if !has("carol", open) {
		t.Fatal("the turn's thread is missing from the agents' listing")
	}
	for _, c := range m.list("carol", "?agents=1").Channels {
		if c.ChannelID == open {
			t.Fatal("a thread is listed for agents without being asked for")
		}
	}
	if has("carol", secret) {
		t.Fatal("a thread of a members-only channel is listed for a non-member's agent")
	}
	if !has("bob", secret) {
		t.Fatal("a member's agent does not get its own thread")
	}
	m.patch("alice", design.ChannelID, map[string]any{"settings": map[string]any{"noAgents": true}}, http.StatusOK)
	if has("carol", open) {
		t.Fatal("a thread of a channel that bars agents is listed for agents")
	}
}
