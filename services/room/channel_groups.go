package room

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A member's own groups of their channel rail
// (docs/prd/2026-10-28-channels-and-threads.md §2.1b, C24): folders only
// they see, holding any channel they joined; a channel's section is
// unchanged for everyone else. They are the member's own marks, like stars,
// so any human member keeps them, a visitor included (the same rule as
// following; decision: pending owner, as for following).
//
//	POST   /v1/rooms/{roomId}/channel-groups             wire.ChannelGroupCreateRequest → wire.ChannelGroup
//	PATCH  /v1/rooms/{roomId}/channel-groups/{groupId}   wire.ChannelGroupPatchRequest → wire.ChannelGroup
//	DELETE /v1/rooms/{roomId}/channel-groups/{groupId}   its channels return to their sections
//
// The caller's groups are listed with the channels (GET .../channels), and a
// channel is put in one through its follow (PUT .../follow {groupId}). No
// event is published: nobody else's view changes.

// groupView is the one projection of a member's group.
func groupView(g store.ChannelGroup) wire.ChannelGroup {
	return wire.ChannelGroup{GroupID: g.GroupID, Name: g.Name, Position: g.Position, CreatedAtMs: g.CreatedAtMs}
}

// groupsOf reads user's groups in the room ("" user: none).
func (s *Service) groupsOf(ctx context.Context, roomID, user string) ([]wire.ChannelGroup, error) {
	out := []wire.ChannelGroup{}
	if user == "" {
		return out, nil
	}
	list, err := s.Channels.ChannelGroups(ctx, roomID, user)
	if err != nil {
		return nil, err
	}
	for _, g := range list {
		out = append(out, groupView(g))
	}
	return out, nil
}

// ownGroup reports whether groupID is one of user's groups in the room.
func (s *Service) ownGroup(ctx context.Context, roomID, user, groupID string) (bool, error) {
	_, found, err := s.findGroup(ctx, roomID, user, groupID)
	return found, err
}

func (s *Service) findGroup(ctx context.Context, roomID, user, groupID string) (store.ChannelGroup, bool, error) {
	list, err := s.Channels.ChannelGroups(ctx, roomID, user)
	if err != nil {
		return store.ChannelGroup{}, false, err
	}
	for _, g := range list {
		if g.GroupID == groupID {
			return g, true, nil
		}
	}
	return store.ChannelGroup{}, false, nil
}

// groupName is a group's name, trimmed and bounded.
func groupName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	switch {
	case name == "":
		return "", errBadRequest("name is required")
	case utf8.RuneCountInString(name) > wire.MaxChannelGroupName:
		return "", errBadRequest("a group's name is too long")
	}
	return name, nil
}

func (s *Service) handleCreateChannelGroup(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	who, ok := s.humanCaller(w, r, roomID)
	if !ok {
		return
	}
	var body wire.ChannelGroupCreateRequest
	if !httpx.DecodeJSON(w, r, maxJSONBodyBytes, &body) {
		return
	}
	name, err := groupName(body.Name)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	have, err := s.Channels.ChannelGroups(r.Context(), roomID, who.user)
	if err != nil {
		obs.ServerError(w, "read channel groups failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(have) >= wire.MaxChannelGroups {
		httpx.Error(w, "you have as many groups as a room allows", http.StatusConflict)
		return
	}
	g := store.ChannelGroup{
		TenantID: tenant.FromContext(r.Context()), RoomID: roomID, UserID: who.user,
		GroupID: ids.New("grp_"), Name: name, Position: body.Position, CreatedAtMs: s.Now(),
	}
	if err := s.Channels.PutChannelGroup(r.Context(), g); err != nil {
		obs.ServerError(w, "create channel group failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, groupView(g))
}

func (s *Service) handlePatchChannelGroup(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	who, ok := s.humanCaller(w, r, roomID)
	if !ok {
		return
	}
	var body wire.ChannelGroupPatchRequest
	if !httpx.DecodeJSON(w, r, maxJSONBodyBytes, &body) {
		return
	}
	patch := store.ChannelGroupPatch{Position: body.Position}
	if body.Name != nil {
		name, err := groupName(*body.Name)
		if err != nil {
			httpx.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		patch.Name = &name
	}
	groupID := r.PathValue("groupId")
	if err := s.Channels.UpdateChannelGroup(r.Context(), roomID, who.user, groupID, patch); err != nil {
		if errors.Is(err, store.ErrChannelGroupNotFound) {
			httpx.Error(w, "group not found", http.StatusNotFound)
			return
		}
		obs.ServerError(w, "update channel group failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	g, found, err := s.findGroup(r.Context(), roomID, who.user, groupID)
	if err != nil || !found {
		if err == nil {
			err = store.ErrChannelGroupNotFound
		}
		obs.ServerError(w, "read channel group failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, groupView(g))
}

// handleDeleteChannelGroup deletes one of the caller's groups; its channels
// return to their sections on the caller's rail (nothing else changes).
func (s *Service) handleDeleteChannelGroup(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	who, ok := s.humanCaller(w, r, roomID)
	if !ok {
		return
	}
	if err := s.Channels.DeleteChannelGroup(r.Context(), roomID, who.user, r.PathValue("groupId")); err != nil {
		obs.ServerError(w, "delete channel group failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
