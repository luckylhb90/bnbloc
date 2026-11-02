package room

import (
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A draft (C6) is the caller's own: saved and read back by them only,
// replaced by their next save, removed by DELETE or an empty save; a
// visitor (who cannot post) keeps none, a non-member and a member who may
// not read the channel are told it does not exist, and a text over the chat
// cap is refused.
func TestChannelDraftsAreTheMembersOwn(t *testing.T) {
	m := newMembersOnlyEnv(t)
	e := m.setupEnv
	general := "/v1/rooms/room-1/channels/" + e.def + "/draft"

	var empty wire.ChannelDraft
	e.call(http.MethodGet, general, "carol", nil, http.StatusOK, &empty)
	if empty.Text != "" || empty.UpdatedAtMs != 0 {
		t.Fatalf("no draft yet = %+v", empty)
	}
	var saved wire.ChannelDraft
	e.call(http.MethodPut, general, "carol", map[string]any{"text": "half a thought"}, http.StatusOK, &saved)
	if saved.Text != "half a thought" || saved.UpdatedAtMs == 0 {
		t.Fatalf("saved = %+v", saved)
	}
	e.call(http.MethodPut, general, "carol", map[string]any{"text": "a whole thought"}, http.StatusOK, nil)
	var got wire.ChannelDraft
	e.call(http.MethodGet, general, "carol", nil, http.StatusOK, &got)
	if got.Text != "a whole thought" {
		t.Fatalf("carol's draft = %+v (the latest save)", got)
	}
	var bobs wire.ChannelDraft
	e.call(http.MethodGet, general, "bob", nil, http.StatusOK, &bobs)
	if bobs.Text != "" {
		t.Fatalf("bob reads carol's draft: %+v", bobs)
	}

	e.call(http.MethodPut, general, "carol", map[string]any{"text": strings.Repeat("x", wire.MaxChatTextBytes+1)}, http.StatusRequestEntityTooLarge, nil)
	e.call(http.MethodPut, general, "vic", map[string]any{"text": "a visitor's"}, http.StatusForbidden, nil)
	e.call(http.MethodPut, general, "mallory", map[string]any{"text": "outsider"}, http.StatusForbidden, nil)

	// A members-only channel does not exist for a member outside its section.
	plans := "/v1/rooms/room-1/channels/" + m.plans.ChannelID + "/draft"
	e.call(http.MethodPut, plans, "carol", map[string]any{"text": "peek"}, http.StatusNotFound, nil)
	e.call(http.MethodGet, plans, "carol", nil, http.StatusNotFound, nil)
	e.call(http.MethodPut, plans, "bob", map[string]any{"text": "the plan"}, http.StatusOK, nil)

	// An empty save and a DELETE both remove it.
	e.call(http.MethodPut, plans, "bob", map[string]any{"text": "  "}, http.StatusOK, nil)
	var cleared wire.ChannelDraft
	e.call(http.MethodGet, plans, "bob", nil, http.StatusOK, &cleared)
	if cleared.Text != "" {
		t.Fatalf("an empty save kept %+v", cleared)
	}
	e.call(http.MethodDelete, general, "carol", nil, http.StatusNoContent, nil)
	e.call(http.MethodGet, general, "carol", nil, http.StatusOK, &got)
	if got.Text != "" {
		t.Fatalf("after delete = %+v", got)
	}
}
