package room

import (
	"context"
	"errors"
	"net/http"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// A shared room is readable by a non-member (docs/prd/2026-10-22-shared-
// room-preview.md): anyone could join it and read it one click later. This
// file is the room service's one answer to "is this room readable by a
// non-member" (sharedRoom) and its one read gate (mayRead, requireRead).
// Which organization may read it is the gateway's call: it acts in the
// room's tenant on every path that names a room, so the request's tenant
// here is the room's.

// sharedRoom reports whether roomID is live and shared in the request's
// tenant.
func (s *Service) sharedRoom(ctx context.Context, roomID string) (bool, error) {
	rm, err := s.Store.GetRoom(ctx, roomID)
	if errors.Is(err, store.ErrRoomNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return rm.Visibility == store.VisibilityShared, nil
}

// sharedRoomAnyTenant is sharedRoom for the tenant-agnostic membership probe:
// the room's own tenant is resolved first, then its visibility read in it.
// tenantID is "" when the room is not live.
func (s *Service) sharedRoomAnyTenant(ctx context.Context, roomID string) (shared bool, tenantID string, err error) {
	tenantID, ok, err := s.Store.RoomTenant(ctx, roomID)
	if err != nil || !ok {
		return false, "", err
	}
	shared, err = s.sharedRoom(tenant.NewContext(ctx, tenantID), roomID)
	return shared, tenantID, err
}

// mayRead reports whether user may read roomID's public surface (its
// channels, members and deliverables): a member, or anyone when the room is
// shared. Writes and everything else stay members' (Store.IsMember).
func (s *Service) mayRead(ctx context.Context, roomID, user string) (bool, error) {
	isMember, err := s.Store.IsMember(ctx, roomID, user)
	if err != nil || isMember {
		return isMember, err
	}
	return s.sharedRoom(ctx, roomID)
}

// requireRead answers the refusal of a read route itself: 500 when the
// answer cannot be read, 403 for a caller who may not read the room. ok
// reports the caller may.
func (s *Service) requireRead(w http.ResponseWriter, r *http.Request, roomID string) bool {
	ok, err := s.mayRead(r.Context(), roomID, caller(r))
	if err != nil {
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return false
	}
	if !ok {
		httpx.Error(w, "caller is not a member of room", http.StatusForbidden)
		return false
	}
	return true
}

// requireMember is requireRead for a write: members only (403 otherwise).
func (s *Service) requireMember(w http.ResponseWriter, r *http.Request, roomID string) bool {
	isMember, err := s.Store.IsMember(r.Context(), roomID, caller(r))
	if err != nil {
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return false
	}
	if !isMember {
		httpx.Error(w, "caller is not a member of room", http.StatusForbidden)
		return false
	}
	return true
}
