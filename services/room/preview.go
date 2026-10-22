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

// A previewable room is readable by a non-member (docs/prd/2026-10-22-shared-
// room-preview.md): its owner lets anyone in the organization read it before
// joining (a shared room is previewable by default; a private one can be too,
// and is still joined with an invite code). This file is the room service's
// one answer to "is this room readable by a non-member" (previewableRoom) and
// its one read gate (readAccess, requireRead).
// Which organization may read it is the gateway's call: it acts in the
// room's tenant on every path that names a room, so the request's tenant
// here is the room's.

// previewableRoom reports whether roomID is live and previewable in the
// request's tenant.
func (s *Service) previewableRoom(ctx context.Context, roomID string) (bool, error) {
	rm, err := s.Store.GetRoom(ctx, roomID)
	if errors.Is(err, store.ErrRoomNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return rm.Previewable, nil
}

// previewableRoomAnyTenant is previewableRoom for the tenant-agnostic
// membership probe: the room's own tenant is resolved first, then the room
// read in it. tenantID is "" when the room is not live.
func (s *Service) previewableRoomAnyTenant(ctx context.Context, roomID string) (previewable bool, tenantID string, err error) {
	tenantID, ok, err := s.Store.RoomTenant(ctx, roomID)
	if err != nil || !ok {
		return false, "", err
	}
	previewable, err = s.previewableRoom(tenant.NewContext(ctx, tenantID), roomID)
	return previewable, tenantID, err
}

// readAccess reports how user may read roomID's public surface (its
// channels, members and deliverables): as a member, or, when the room is
// previewable, as anyone (member=false, ok=true). Writes and everything else stay
// members' (requireMember, Store.IsMember).
func (s *Service) readAccess(ctx context.Context, roomID, user string) (member, ok bool, err error) {
	if member, err = s.Store.IsMember(ctx, roomID, user); err != nil || member {
		return member, member, err
	}
	ok, err = s.previewableRoom(ctx, roomID)
	return false, ok, err
}

// requireReadAs answers the refusal of a read route itself: 500 when the
// answer cannot be read, 403 for a caller who may not read the room. ok
// reports the caller may; member whether they are a member.
func (s *Service) requireReadAs(w http.ResponseWriter, r *http.Request, roomID string) (member, ok bool) {
	member, ok, err := s.readAccess(r.Context(), roomID, caller(r))
	if err != nil {
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return false, false
	}
	if !ok {
		httpx.Error(w, "caller is not a member of room", http.StatusForbidden)
		return false, false
	}
	return member, true
}

// requireRead is requireReadAs for a route that serves a member and a
// non-member alike.
func (s *Service) requireRead(w http.ResponseWriter, r *http.Request, roomID string) bool {
	_, ok := s.requireReadAs(w, r, roomID)
	return ok
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
