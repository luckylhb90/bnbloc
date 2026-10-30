package room

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A room's rules and onboarding setup (docs/prd/2026-10-28-channels-and-
// threads.md §2.5a, §2.1b):
//
//	GET  /v1/rooms/{roomId}/rules         wire.RoomRulesState (the caller's agreement)
//	POST /v1/rooms/{roomId}/rules/agree   wire.RulesAgreeRequest → wire.RoomRulesState
//
// The rules, the agreement switch, the recommended channels and the channel
// creation policy are set with PATCH /v1/rooms/{roomId} by the room's owner
// or an admin (roomSetupPatch). A change of the rules text bumps their
// version (the store, in the same write), and a room that requires agreeing
// refuses a member's post until they agree to the current version
// (realtime's channel gate).

// roomSetupPatch validates the owner-or-admin part of a room patch into
// patch; it reports whether the body named any of it.
func (s *Service) roomSetupPatch(ctx context.Context, roomID string, body wire.RoomPatchRequest, patch *store.RoomPatch) (bool, error) {
	named := false
	if body.Rules != nil {
		rules, err := channelText(body.Rules, wire.MaxRoomRules, "rules")
		if err != nil {
			return true, err
		}
		patch.Rules, named = &rules, true
	}
	if body.RequireRulesAgreement != nil {
		// decision: channels PRD C16 (off by default; the room's owners may
		// require it).
		patch.RequireRulesAgreement, named = body.RequireRulesAgreement, true
	}
	if body.ChannelCreation != nil {
		policy := strings.TrimSpace(*body.ChannelCreation)
		if !wire.IsChannelCreationPolicy(policy) {
			return true, errBadRequest("channelCreation must be one of anyone, sections or admins")
		}
		patch.ChannelCreation, named = &policy, true
	}
	if body.RecommendedChannels != nil {
		ids, err := s.recommendedChannels(ctx, roomID, *body.RecommendedChannels)
		if err != nil {
			return true, err
		}
		patch.RecommendedChannels, named = &ids, true
	}
	return named, nil
}

// recommendedChannels validates a room's recommended channels: live,
// ordinary, unarchived channels of the room, by id, each once, in the given
// order.
func (s *Service) recommendedChannels(ctx context.Context, roomID string, in []string) ([]string, error) {
	out := []string{}
	for _, id := range in {
		id = strings.TrimSpace(id)
		if id == "" || slices.Contains(out, id) {
			continue
		}
		ch, ok, err := s.Channels.GetChannel(ctx, roomID, id)
		if err != nil {
			return nil, err
		}
		if !ok || ch.IsPeople() || ch.Archived() {
			return nil, errBadRequest("a recommended channel is a live, unarchived channel of the room: " + id)
		}
		out = append(out, id)
	}
	return out, nil
}

// handleGetRules answers the room's rules and the caller's agreement.
func (s *Service) handleGetRules(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	who, ok := s.humanCaller(w, r, roomID)
	if !ok {
		return
	}
	s.writeRulesState(w, r, roomID, who.user)
}

func (s *Service) writeRulesState(w http.ResponseWriter, r *http.Request, roomID, user string) {
	rm, err := s.Store.GetRoom(r.Context(), roomID)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	m, _, err := s.Store.GetMember(r.Context(), roomID, user)
	if err != nil {
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wire.RoomRulesState{Rules: rm.Rules, Version: rm.RulesVersion, Required: rm.RequireRulesAgreement, AgreedVersion: m.RulesAgreedVersion})
}

// handleAgreeRules records the caller's agreement to the version of the
// rules they read: only the room's current version (wire.RulesChanged
// otherwise), so nobody agrees to rules they did not see. decision: channels
// PRD C16 (a member reads the rules and agrees once; a change asks again).
func (s *Service) handleAgreeRules(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	who, ok := s.humanCaller(w, r, roomID)
	if !ok {
		return
	}
	var body wire.RulesAgreeRequest
	if !httpx.DecodeJSON(w, r, maxJSONBodyBytes, &body) {
		return
	}
	if body.Version < 1 {
		httpx.Error(w, "version names the rules agreed to (the room has none at version 0)", http.StatusBadRequest)
		return
	}
	switch err := s.Store.AgreeRoomRules(r.Context(), roomID, who.user, body.Version); {
	case errors.Is(err, store.ErrRulesChanged):
		httpx.WriteJSON(w, http.StatusConflict, httpx.ErrorBody{Error: "the room's rules changed since you read them: read them again", Code: wire.RulesChanged})
		return
	case errors.Is(err, store.ErrRoomNotFound):
		httpx.Error(w, "caller is not a member of room", http.StatusForbidden)
		return
	case err != nil:
		obs.ServerError(w, "agree to rules failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishRoomUpdated(r.Context(), roomID, who.user)
	s.writeRulesState(w, r, roomID, who.user)
}
