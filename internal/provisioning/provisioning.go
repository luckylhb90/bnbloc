// Package provisioning maps a verified IdP identity to its organization
// (tenant) and org role at login, auto-provisioning tenants and memberships on
// first sign-in. It is the SSO org-mapping + RBAC-seed layer: the gateway wires
// Resolver.Resolve as its TenantResolver so every login lands the subject in
// the right tenant with the right role.
package provisioning

import (
	"context"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Resolver maps identities to tenants and provisions membership. It is safe for
// concurrent use (the store is).
type Resolver struct {
	Tenants store.TenantStore
	Now     func() int64
	// AutoProvision controls whether an unknown identity creates a tenant +
	// membership on first login. Enterprise SSO deployments set true; a locked
	// deployment sets false to require pre-provisioned tenants.
	AutoProvision bool
	// SuperAdmins is the set of PLATFORM super-admins (cross-tenant). Membership
	// here is configuration-driven (not a tenant membership row) and takes
	// precedence over any org role. A key matches either the identity's verified
	// subject id OR its email (case-insensitively), so an operator can configure
	// admins by the human-readable email (the natural choice) or by the opaque
	// OIDC subject — whichever they have.
	SuperAdmins map[string]bool
}

// isSuperAdmin reports whether a verified identity is a configured platform
// super-admin, matching on either the subject id or the email (emails are
// compared case-insensitively). This is why configuring BNBLOC_SUPER_ADMINS
// with an email works even though the OIDC subject is an opaque id.
func (r *Resolver) isSuperAdmin(id auth.Identity) bool {
	if r.SuperAdmins == nil {
		return false
	}
	if id.Subject != "" && r.SuperAdmins[id.Subject] {
		return true
	}
	if e := strings.ToLower(strings.TrimSpace(id.Email)); e != "" && r.SuperAdmins[e] {
		return true
	}
	return false
}

// New constructs a Resolver over a tenant store.
func New(ts store.TenantStore) *Resolver {
	return &Resolver{Tenants: ts, Now: func() int64 { return time.Now().UnixMilli() }, AutoProvision: true}
}

// Resolve maps a verified identity to its effective tenant + org role, matching
// the gateway's TenantResolver signature. It never fails login: on any error it
// falls back to the provided defaultTenant with an empty role.
//
// Mapping precedence:
//  1. identity.Tenant, when the IdP/verifier already resolved one (SAML issuer
//     mapping, OIDC org claim);
//  2. the tenant registered for the identity's SSO issuer / email domain;
//  3. defaultTenant.
//
// After resolving the tenant, the subject's membership is ensured: an existing
// member keeps its role; a new member is provisioned (the FIRST member of a
// tenant becomes org_admin, everyone after is a member). The returned role is
// the member's org role, stamped into the session token for coarse RBAC.
func (r *Resolver) Resolve(id auth.Identity, defaultTenant string) (tenantID, role string) {
	ctx := context.Background()
	tenantID = r.resolveTenant(ctx, id, defaultTenant)

	// Ensure the tenant row exists (auto-provision) so membership + issuer
	// mapping have a home. Best-effort; never blocks login.
	issuer := issuerFor(id)
	if _, err := r.Tenants.GetTenant(ctx, tenantID); err != nil && r.AutoProvision {
		_ = r.Tenants.PutTenant(ctx, store.Tenant{
			TenantID: tenantID, Name: tenantID, Status: store.TenantActive,
			SSOIssuer: issuer, CreatedAtMs: r.Now(),
		})
	}

	// Platform super-admin takes precedence over any org role: a configured
	// super-admin is cross-tenant, so they are never bound to one tenant's
	// membership role. They still get a home tenant (resolved above) as the
	// default scope for their session; cross-tenant access is via ?tenantId.
	if r.isSuperAdmin(id) {
		return tenantID, string(store.OrgRoleSuperAdmin)
	}
	// Existing member keeps their role.
	if m, ok := r.Tenants.GetMember(ctx, tenantID, id.Subject); ok {
		return tenantID, string(m.Role)
	}
	if !r.AutoProvision {
		// Not a member and provisioning disabled: no role (viewer-equivalent).
		return tenantID, string(store.OrgRoleViewer)
	}
	// New member: the first user of a tenant bootstraps as org_admin so an org
	// is never left without an administrator; everyone after is a member.
	newRole := store.OrgRoleMember
	if r.Tenants.MemberCount(ctx, tenantID) == 0 {
		newRole = store.OrgRoleAdmin
	}
	_ = r.Tenants.PutMember(ctx, store.TenantMember{
		TenantID: tenantID, UserID: id.Subject, Role: newRole, JoinedAtMs: r.Now(),
	})
	return tenantID, string(newRole)
}

// resolveTenant applies the mapping precedence to pick the tenant id.
func (r *Resolver) resolveTenant(ctx context.Context, id auth.Identity, defaultTenant string) string {
	if t := strings.TrimSpace(id.Tenant); t != "" {
		return t
	}
	if issuer := issuerFor(id); issuer != "" {
		if t, ok := r.Tenants.TenantForIssuer(ctx, issuer); ok {
			return t
		}
		if r.AutoProvision {
			// A fresh org signing in via SSO for the first time: derive a stable
			// tenant id from its issuer/domain so the whole org converges on one
			// tenant without pre-registration.
			return "org-" + sanitize(issuer)
		}
	}
	// No IdP tenant and no issuer mapping: fall back to the user's existing
	// membership (their most-recently-joined tenant). This is what makes an
	// admin-approved join request take effect — after approval the user has a
	// membership row, so their next login lands in that tenant instead of the
	// default. A user with no membership falls through to the default.
	if t, ok := r.Tenants.PrimaryTenantForUser(ctx, id.Subject); ok {
		return t
	}
	if defaultTenant == "" {
		return tenant.Default
	}
	return defaultTenant
}

// issuerFor derives the org-identifying issuer for an identity: the explicit
// tenant if any, else the email domain (the natural org boundary for SSO).
func issuerFor(id auth.Identity) string {
	if t := strings.TrimSpace(id.Tenant); t != "" {
		return t
	}
	if at := strings.LastIndexByte(id.Email, '@'); at >= 0 && at+1 < len(id.Email) {
		return strings.ToLower(strings.TrimSpace(id.Email[at+1:]))
	}
	return ""
}

// sanitize makes an issuer/domain safe as a tenant id path segment.
func sanitize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	return b.String()
}
