package gateway

import (
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
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
//     (rooms.tenant_id): a member of a room in another organization, or a
//     super admin acting elsewhere, transacts where the room lives. This
//     wins over 2. The named variants below are the only ways to apply it,
//     each with the check its callers need: inRoomTenant (the proxy, whose
//     routes decide reach), roomClaims (the caller's reach: content ?room=,
//     the job grant, site tickets), roomEditorClaims (the room's owner or an
//     admin: room content uses) and roomInTenant (a share that must stay in
//     the caller's organization). Backends refuse a request signed for any
//     other tenant than the room's (membership.RoomScope).

// actingTenantHeader is the client-supplied global tenant switch for platform
// super-admins. It is advisory (honored only for super_admins) and is always
// stripped before proxying so a backend only ever sees the signed tenant.
const actingTenantHeader = "X-Bnbloc-Acting-Tenant"

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

// roomTenantOr is the room's own tenant, else fallback when it cannot be
// resolved (the backend then refuses the room, membership.RoomScope). Only
// the variants below call it.
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

// inRoomTenant returns the claims acting in the room's tenant (step 3) for a
// request whose right to name the room its route decides: the proxy, where
// sub-resources need the caller's reach and the room itself, a join, or an
// org admin's share-link revocation do not (the room service decides).
func (s *Service) inRoomTenant(claims auth.Claims, roomID string) auth.Claims {
	claims.Tenant = s.roomTenantOr(roomID, claims.Tenant)
	return claims
}

// roomClaims checks the caller may reach roomID and returns their claims
// acting in the room's tenant (step 3). false when the room is out of reach
// (an invalid room id included).
func (s *Service) roomClaims(claims auth.Claims, roomID string) (auth.Claims, bool) {
	if roomctx.ValidateID(roomID) != nil || !s.roomReachable(claims, roomID) {
		return claims, false
	}
	return s.inRoomTenant(claims, roomID), true
}

// roomEditorClaims checks the caller may change the room's settings (its
// owner or an admin, a person) and returns their claims acting in the room's
// tenant. An editor is a member: the room is in reach.
func (s *Service) roomEditorClaims(claims auth.Claims, roomID string) (auth.Claims, bool) {
	m, ok := s.roomMember(claims.Subject, roomID)
	if !ok || !m.IsHuman() || (m.Role != store.RoleOwner && m.Role != store.RoleAdmin) {
		return claims, false
	}
	return s.inRoomTenant(claims, roomID), true
}

// roomInTenant reports whether the room lives in the tenant the caller acts
// in (a share reaches only the caller's own organization). A room whose
// tenant cannot be resolved is not (fail closed); without a resolver the
// deployment is single-tenant.
func (s *Service) roomInTenant(claims auth.Claims, roomID string) bool {
	if s.RoomTenant == nil {
		return true
	}
	return s.RoomTenant(roomID) == claims.Tenant
}
