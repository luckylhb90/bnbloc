package room

import (
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A member's groups of their rail (C24) are theirs alone: made, renamed and
// deleted by them, listed only to them; a channel goes only into one of
// their own groups; deleting a group returns its channels to their section,
// and leaving a channel takes it out of its group. A visitor keeps their own
// groups, as they keep stars; a non-member has none.
func TestChannelGroupsAreTheMembersOwn(t *testing.T) {
	e := newSetupEnv(t)
	design := e.create("bob", wire.ChannelCreateRequest{Name: "design"}, http.StatusOK)
	var mine wire.ChannelGroup
	e.call(http.MethodPost, "/v1/rooms/room-1/channel-groups", "carol", map[string]any{"name": "  Focus  "}, http.StatusOK, &mine)
	if mine.GroupID == "" || mine.Name != "Focus" {
		t.Fatalf("created group = %+v", mine)
	}
	e.call(http.MethodPost, "/v1/rooms/room-1/channel-groups", "carol", map[string]any{"name": " "}, http.StatusBadRequest, nil)
	e.call(http.MethodPost, "/v1/rooms/room-1/channel-groups", "carol", map[string]any{"name": strings.Repeat("g", wire.MaxChannelGroupName+1)}, http.StatusBadRequest, nil)
	e.call(http.MethodPost, "/v1/rooms/room-1/channel-groups", "vic", map[string]any{"name": "Reading"}, http.StatusOK, nil)
	e.call(http.MethodPost, "/v1/rooms/room-1/channel-groups", "mallory", map[string]any{"name": "Nope"}, http.StatusForbidden, nil)

	follow := "/v1/rooms/room-1/channels/" + design.ChannelID + "/follow"
	var got wire.RoomChannel
	e.call(http.MethodPut, follow, "carol", map[string]any{"groupId": mine.GroupID}, http.StatusOK, &got)
	if !got.Joined || got.GroupID != mine.GroupID || got.SectionID != design.SectionID {
		t.Fatalf("after moving into the group = %+v", got)
	}
	// Another member's group is not the caller's to use, and never shown.
	e.call(http.MethodPut, follow, "bob", map[string]any{"groupId": mine.GroupID}, http.StatusBadRequest, nil)
	if l := e.list("bob", ""); len(l.Groups) != 0 || byChannel(l.Channels)[design.ChannelID].GroupID != "" {
		t.Fatalf("bob sees carol's groups: %+v", l.Groups)
	}
	if l := e.list("carol", ""); len(l.Groups) != 1 || l.Groups[0].GroupID != mine.GroupID || byChannel(l.Channels)[design.ChannelID].GroupID != mine.GroupID {
		t.Fatalf("carol's list = %+v", l.Groups)
	}
	// Starring keeps the group; an empty group id takes it out.
	e.call(http.MethodPut, follow, "carol", map[string]any{"starred": true}, http.StatusOK, &got)
	if got.GroupID != mine.GroupID || !got.Starred {
		t.Fatalf("a star moved the channel out of its group: %+v", got)
	}
	e.call(http.MethodPut, follow, "carol", map[string]any{"groupId": ""}, http.StatusOK, &got)
	if got.GroupID != "" {
		t.Fatalf("an empty group id kept the group: %+v", got)
	}

	// Rename: only one's own.
	var renamed wire.ChannelGroup
	e.call(http.MethodPatch, "/v1/rooms/room-1/channel-groups/"+mine.GroupID, "carol", map[string]any{"name": "Now", "position": 2}, http.StatusOK, &renamed)
	if renamed.Name != "Now" || renamed.Position != 2 {
		t.Fatalf("renamed = %+v", renamed)
	}
	e.call(http.MethodPatch, "/v1/rooms/room-1/channel-groups/"+mine.GroupID, "bob", map[string]any{"name": "Mine"}, http.StatusNotFound, nil)

	// Delete returns the channel to its section; another member's delete
	// leaves carol's group alone.
	e.call(http.MethodPut, follow, "carol", map[string]any{"groupId": mine.GroupID}, http.StatusOK, nil)
	e.call(http.MethodDelete, "/v1/rooms/room-1/channel-groups/"+mine.GroupID, "bob", nil, http.StatusNoContent, nil)
	if l := e.list("carol", ""); len(l.Groups) != 1 {
		t.Fatalf("bob's delete removed carol's group: %+v", l.Groups)
	}
	e.call(http.MethodDelete, "/v1/rooms/room-1/channel-groups/"+mine.GroupID, "carol", nil, http.StatusNoContent, nil)
	l := e.list("carol", "")
	if c := byChannel(l.Channels)[design.ChannelID]; len(l.Groups) != 0 || !c.Joined || c.GroupID != "" {
		t.Fatalf("after the group's delete: groups %+v, channel %+v", l.Groups, c)
	}

	// Leaving a channel takes it out of its group; joining again starts
	// outside any group.
	var again wire.ChannelGroup
	e.call(http.MethodPost, "/v1/rooms/room-1/channel-groups", "carol", map[string]any{"name": "Again"}, http.StatusOK, &again)
	e.call(http.MethodPut, follow, "carol", map[string]any{"groupId": again.GroupID}, http.StatusOK, nil)
	e.call(http.MethodDelete, follow, "carol", nil, http.StatusOK, nil)
	e.call(http.MethodPut, follow, "carol", nil, http.StatusOK, &got)
	if got.GroupID != "" {
		t.Fatalf("a channel joined again is still in its old group: %+v", got)
	}
}
