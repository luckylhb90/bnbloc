package gateway

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// handleRoomMemory answers GET /v1/rooms/{roomId}/memory?job=: what an agent
// is handed of the room's notes (docs/prd/2026-10-16-room-memory-and-
// evolution.md), read by the bridge for .room/memory/. The caller must reach
// the room, and the read is for a job only: the bridge running the job reads as
// the job's requester (jobGrant, the content routes' rule), who must be a
// member (the room service decides, as for the app and library reads). Members
// read and edit notes through /v1/rooms/{roomId}/notes instead.
func (s *Service) handleRoomMemory(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	w.Header().Set("Cache-Control", "no-store")
	roomID := r.PathValue("roomId")
	job := strings.TrimSpace(r.URL.Query().Get("job"))
	if job == "" {
		httpx.Error(w, "the room's memory is read for a job (?job=); members use the room's notes", http.StatusBadRequest)
		return
	}
	claims, ok := s.roomClaims(caller, roomID)
	if !ok {
		httpx.Error(w, "room scope not authorized", http.StatusForbidden)
		return
	}
	actor, err := s.jobGrant(r.Context(), caller, claims, roomID, job)
	if err != nil {
		writeGrantErr(w, err)
		return
	}
	var mem wire.RoomMemory
	path := "/internal/v1/rooms/" + url.PathEscape(roomID) + "/memory?as=" + url.QueryEscape(actor.Claims.Subject)
	if err := s.roomBackendDo(r.Context(), backendHTTP, http.MethodGet, path, "", nil, gatewayServiceSubject, actor.Claims.Tenant, &mem); err != nil {
		var rbe *roomBackendError
		if errors.As(err, &rbe) && rbe.Status < http.StatusInternalServerError {
			httpx.WriteStatus(w, rbe.Status, rbe.Body, err)
			return
		}
		obs.ServerError(w, "read the room's memory failed", err)
		httpx.Error(w, "the room's memory could not be read", http.StatusBadGateway)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, mem)
}
