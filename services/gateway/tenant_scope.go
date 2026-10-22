package gateway

import (
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// The effective tenant of a request: the ONE rule the reverse proxy and
// every handler the gateway serves itself follow
// (docs/bugs/2026-10-12-acting-tenant-ignored-by-gateway-handlers.md).
//
//  1. The session tenant (the token's claim).
//  2. A platform super admin may act inside any tenant: the
//     X-Bnbloc-Acting-Tenant header (the web's global switch, sent on every
//     request; there is no other input). For anyone else it is ignored:
//     they stay bound to their session tenant. Applied at authentication
//     (authenticate, proxyClaims), so a handler's claims.Tenant already is
//     the acting tenant.
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

// actingTenant applies steps 1 and 2 to verified session claims. A token
// always acts in a tenant: one minted without a tenant claim acts in the
// default tenant (the only place the gateway defaults it).
func actingTenant(r *http.Request, claims auth.Claims) string {
	t := claims.Tenant
	if t == "" {
		t = tenant.Default
	}
	if !rbac.Can(claims.Role, rbac.PermManagePlatform) {
		return t
	}
	if h := strings.TrimSpace(r.Header.Get(actingTenantHeader)); h != "" {
		t = h
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
	if claims.HasRoom(roomID) {
		return true
	}
	_, ok := s.roomMember(claims.Subject, roomID)
	return ok
}

// roomPreviewable reports whether a caller who cannot reach roomID may read
// it anyway as a preview: the room is live and shared, and it is in the
// organization the caller acts in. The backends know the room is shared
// (membership.Previewer) but not the caller's organization (they act in the
// room's tenant on every path that names a room), so the organization
// boundary of a preview is this edge's.
func (s *Service) roomPreviewable(claims auth.Claims, roomID string) bool {
	if s.Membership == nil {
		return false
	}
	m, ok := membership.PreviewOf(s.Membership, roomID, claims.Subject)
	return ok && m.Tenant == claims.Tenant
}

// roomMember is subject's live membership of a room in any tenant (role,
// human or agent host), the membership owner's Member: its IsHuman /
// MayChat are the rules the backends apply. Fails closed without a
// Membership source.
func (s *Service) roomMember(subject, roomID string) (membership.Member, bool) {
	if s.Membership == nil || subject == "" || roomID == "" {
		return membership.Member{}, false
	}
	return s.Membership.Lookup(roomID, subject)
}

// inRoomTenant returns the claims acting in the room's tenant (step 3) for a
// request whose right to name the room its route decides: the proxy, where
// sub-resources need the caller's reach and the room itself, a join, or an
// org admin's share-link revocation do not (the room service decides).
func (s *Service) inRoomTenant(claims auth.Claims, roomID string) auth.Claims {
	rt := s.roomTenantOr(roomID, claims.Tenant)
	if rt != claims.Tenant && !rbac.Can(claims.Role, rbac.PermManagePlatform) {
		// An org role holds in the caller's own organization only: in the
		// room's other organization the caller has none, and the backends
		// authorize org actions on the role the gateway signs (svcauth.Role).
		claims.Role = ""
	}
	claims.Tenant = rt
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
