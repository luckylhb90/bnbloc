package gateway

import (
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// The effective tenant of a request: the ONE rule the reverse proxy and
// every handler the gateway serves itself follow
// (docs/bugs/2026-10-12-acting-tenant-ignored-by-gateway-handlers.md).
//
//  1. The session tenant (the token's claim).
//  2. A platform super admin may act inside any tenant: the
//     X-Bnbloc-Acting-Tenant header (the web's global switch, sent on every
//     request), then ?tenantId= (a per-call override, which wins). For
//     anyone else both are ignored: they stay bound to their session
//     tenant. Applied at authentication (authenticate, proxyClaims), so a
//     handler's claims.Tenant already is the acting tenant.
//  3. A request that names a room acts in the room's own tenant
//     (rooms.tenant_id), once the caller's reach of the room is checked
//     (roomClaims): a member of a room in another organization, or a super
//     admin acting elsewhere, transacts where the room lives, exactly as a
//     job grant does (contentActing). This wins over 2. Whether a route
//     names a room is the handler's decision: the proxy's room-scoped paths,
//     content routes with ?room= (contentCaller), room content uses, site
//     tickets.

// actingTenant applies steps 1 and 2 to verified session claims.
func actingTenant(r *http.Request, claims auth.Claims) string {
	t := claims.Tenant
	if !rbac.Can(claims.Role, rbac.PermManagePlatform) {
		return t
	}
	if h := strings.TrimSpace(r.Header.Get(actingTenantHeader)); h != "" {
		t = h
	}
	if q := strings.TrimSpace(r.URL.Query().Get("tenantId")); q != "" {
		t = q
	}
	return t
}

// roomTenantOr applies step 3: the room's own tenant, else fallback (the
// tenant the request acts in) when the room's tenant cannot be resolved.
// The caller checks the request is entitled to name the room (roomClaims,
// or a job lease that pins the job to the room).
func (s *Service) roomTenantOr(roomID, fallback string) string {
	if roomID == "" || s.RoomTenant == nil {
		return fallback
	}
	if rt := s.RoomTenant(roomID); rt != "" {
		return rt
	}
	return fallback
}

// roomReachable reports whether the caller may reach a room: the token's
// baked room list, else a live membership lookup (a room created or joined
// after sign-in).
func (s *Service) roomReachable(claims auth.Claims, roomID string) bool {
	return claims.HasRoom(roomID) || s.subjectInRoom(claims.Subject, roomID, claims.Tenant)
}

// roomClaims checks the caller may reach roomID and returns their claims
// acting in the room's tenant (step 3). false when the room is out of reach.
func (s *Service) roomClaims(claims auth.Claims, roomID string) (auth.Claims, bool) {
	if !s.roomReachable(claims, roomID) {
		return claims, false
	}
	return s.inRoomTenant(claims, roomID), true
}

// inRoomTenant returns the claims acting in the room's tenant (step 3) for a
// request whose right to name the room its route decides: the proxy, where
// sub-resources need the caller's reach and the room itself, a join, or an
// org admin's share-link revocation do not (the room service decides).
func (s *Service) inRoomTenant(claims auth.Claims, roomID string) auth.Claims {
	claims.Tenant = s.roomTenantOr(roomID, claims.Tenant)
	return claims
}
