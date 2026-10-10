package room

import (
	"errors"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
)

// Agent-host memberships (docs/prd/2026-10-10-zero-install-onboarding.md §2):
// a platform agent's host holds a hidden membership in every room the agent
// was added to, so the host's bridge reads the room and posts the agent's
// replies. Only shareagent — the platform-agent authority, which checks the
// adding member and the published policy — adds or removes one, over these
// internal routes (svcauth-signed as membership.AgentHostSubject). The
// request's tenant (signed) is the room's.
func (s *Service) registerAgentHostRoutes(mux *http.ServeMux) {
	internal := func(h http.HandlerFunc) http.Handler { return svcauth.RequireSigned(svcauth.KeyFromEnv(), h) }
	mux.Handle("PUT /internal/v1/rooms/{roomId}/agent-hosts/{userId}", internal(s.handlePutAgentHost))
	mux.Handle("DELETE /internal/v1/rooms/{roomId}/agent-hosts/{userId}", internal(s.handleDeleteAgentHost))
}

// agentHostTarget checks the internal caller and returns the path's room and
// user; ok=false when it wrote an error.
func agentHostTarget(w http.ResponseWriter, r *http.Request) (roomID, userID string, ok bool) {
	if caller(r) != membership.AgentHostSubject {
		writeErr(w, http.StatusForbidden, "agent-host memberships are managed by shareagent only")
		return "", "", false
	}
	roomID, userID = r.PathValue("roomId"), strings.TrimSpace(r.PathValue("userId"))
	if roomID == "" || userID == "" {
		writeErr(w, http.StatusBadRequest, "room and user are required")
		return "", "", false
	}
	return roomID, userID, true
}

// handlePutAgentHost adds the agent-host membership (idempotent; a human
// member stays a human member). 204, or 404 when the room is gone.
func (s *Service) handlePutAgentHost(w http.ResponseWriter, r *http.Request) {
	roomID, userID, ok := agentHostTarget(w, r)
	if !ok {
		return
	}
	if err := s.Store.AddAgentHost(r.Context(), roomID, userID, s.Now()); err != nil {
		if errors.Is(err, store.ErrRoomNotFound) {
			writeErr(w, http.StatusNotFound, "room not found")
			return
		}
		obs.ServerError(w, "add agent host failed", err)
		writeErr(w, http.StatusInternalServerError, "add agent host failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteAgentHost removes the agent-host membership (never a human
// one; absent is fine) and closes the host's live streams of the room.
func (s *Service) handleDeleteAgentHost(w http.ResponseWriter, r *http.Request) {
	roomID, userID, ok := agentHostTarget(w, r)
	if !ok {
		return
	}
	m, member := s.Store.GetMember(r.Context(), roomID, userID)
	if !member || !m.AgentHost {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := s.Store.RemoveAgentHost(r.Context(), roomID, userID); err != nil {
		obs.ServerError(w, "remove agent host failed", err)
		writeErr(w, http.StatusInternalServerError, "remove agent host failed")
		return
	}
	s.publishMemberRemoved(r, roomID, userID)
	w.WriteHeader(http.StatusNoContent)
}
