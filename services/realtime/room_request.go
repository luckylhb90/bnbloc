package realtimehttp

import (
	"net/http"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
)

// The one preamble of a room route: the {roomId} path value (normalized;
// 400), the verified caller (401) and, for room, the caller's membership
// with the ability the route needs (membership.RoomScope in the request's
// tenant; 403). Handlers never re-derive any of the three.

// room resolves the room, the caller and the caller's membership with the
// ability need. It answers the error itself.
func (s *Service) room(w http.ResponseWriter, r *http.Request, need membership.Need) (roomID, userID string, m membership.Member, ok bool) {
	if roomID, userID, ok = s.roomCaller(w, r); !ok {
		return "", "", m, false
	}
	if m, ok = s.member(w, r, roomID, userID, need); !ok {
		return "", "", m, false
	}
	return roomID, userID, m, true
}

// streamRoom is room for a stream open: the room service's current answer,
// never a cached one that could still admit a member removed seconds ago
// (membership.FreshRoomScope); the stream then re-checks on its own.
func (s *Service) streamRoom(w http.ResponseWriter, r *http.Request, need membership.Need) (roomID, userID string, m membership.Member, ok bool) {
	if roomID, userID, ok = s.roomCaller(w, r); !ok {
		return "", "", m, false
	}
	m, err := membership.FreshRoomScope(r.Context(), s.Membership, roomID, userID, need)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusForbidden)
		return "", "", m, false
	}
	return roomID, userID, m, true
}

// roomCaller resolves the room and the caller of a route whose membership
// check lives in the operation it runs (PostChat, Dispatch).
func (s *Service) roomCaller(w http.ResponseWriter, r *http.Request) (roomID, userID string, ok bool) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return "", "", false
	}
	if userID, ok = requireSubject(w, r); !ok {
		return "", "", false
	}
	return roomID, userID, true
}

// requireSubject returns the verified caller, answering 401 when absent.
func requireSubject(w http.ResponseWriter, r *http.Request) (string, bool) {
	userID := svcauth.Subject(r)
	if userID == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return "", false
	}
	return userID, true
}

// member is realtime's one membership check of a request
// (membership.RoomScope): userID's membership of roomID in the request's
// tenant, with the ability need. It answers the 403 itself.
func (s *Service) member(w http.ResponseWriter, r *http.Request, roomID, userID string, need membership.Need) (membership.Member, bool) {
	m, err := membership.RoomScope(r.Context(), s.Membership, roomID, userID, need)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusForbidden)
		return m, false
	}
	return m, true
}
