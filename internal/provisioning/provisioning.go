// Package provisioning maps a verified IdP identity to its organization
// (tenant) and org role at login, auto-provisioning tenants and memberships on
// first sign-in. It is the SSO org-mapping + RBAC-seed layer: the gateway wires
// Resolver.Resolve as its TenantResolver so every login lands the subject in
// the right tenant with the right role.
package provisioning

import (
	"context"
	"errors"
	"fmt"
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
	// subject id (exactly: subjects are case-sensitive) OR its email (an email
	// address compares case-insensitively), so an operator can configure
	// admins by the human-readable email (the natural choice) or by the opaque
	// OIDC subject — whichever they have.
	SuperAdmins map[string]bool
}

// isSuperAdmin reports whether a verified identity is a configured platform
// super-admin, matching on either the subject id or the email. This is why
// configuring BNBLOC_SUPER_ADMINS with an email works even though the OIDC
// subject is an opaque id.
func (r *Resolver) isSuperAdmin(id auth.Identity) bool {
	if r.SuperAdmins == nil {
		return false
	}
	if id.Subject != "" && r.SuperAdmins[id.Subject] {
		return true
	}
	email := strings.TrimSpace(id.Email)
	if email == "" {
		return false
	}
	for k := range r.SuperAdmins {
		if strings.Contains(k, "@") && strings.EqualFold(k, email) { // identity-fold: an email address, not a subject
			return true
		}
	}
	return false
}

// New constructs a Resolver over a tenant store.
func New(ts store.TenantStore) *Resolver {
	return &Resolver{Tenants: ts, Now: func() int64 { return time.Now().UnixMilli() }, AutoProvision: true}
}

// Resolve maps a verified identity to its effective tenant + org role, matching
// the gateway's TenantResolver signature. A store failure fails the login
// (err != nil): a read that failed is never taken for "not a member", which
// would re-provision the subject and rewrite their org role.
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
func (r *Resolver) Resolve(id auth.Identity, defaultTenant string) (tenantID, role string, err error) {
	ctx := context.Background()
	tenantID, err = r.resolveTenant(ctx, id, defaultTenant)
	if err != nil {
		return "", "", err
	}

	// Ensure the tenant row exists (auto-provision) so membership + issuer
	// mapping have a home.
	issuer := issuerFor(id)
	if _, err := r.Tenants.GetTenant(ctx, tenantID); err != nil {
		if !errors.Is(err, store.ErrTenantNotFound) {
			return "", "", fmt.Errorf("read tenant %s: %w", tenantID, err)
		}
		if r.AutoProvision {
			if err := r.Tenants.PutTenant(ctx, store.Tenant{
				TenantID: tenantID, Name: tenantID, Status: store.TenantActive,
				SSOIssuer: issuer, CreatedAtMs: r.Now(),
			}); err != nil {
				return "", "", fmt.Errorf("provision tenant %s: %w", tenantID, err)
			}
		}
	}

	// Platform super-admin takes precedence over any org role: a configured
	// super-admin is cross-tenant, so they are never bound to one tenant's
	// membership role. They still get a home tenant (resolved above) as the
	// default scope for their session; cross-tenant access is via ?tenantId.
	if r.isSuperAdmin(id) {
		return tenantID, string(store.OrgRoleSuperAdmin), nil
	}
	// Existing member keeps their role.
	m, ok, err := r.Tenants.GetMember(ctx, tenantID, id.Subject)
	if err != nil {
		return "", "", fmt.Errorf("read membership: %w", err)
	}
	if ok {
		return tenantID, string(m.Role), nil
	}
	if !r.AutoProvision {
		// Not a member and provisioning disabled: no role (viewer-equivalent).
		return tenantID, string(store.OrgRoleViewer), nil
	}
	// New member: the first user of a tenant bootstraps as org_admin so an org
	// is never left without an administrator; everyone after is a member.
	newRole := store.OrgRoleMember
	n, err := r.Tenants.MemberCount(ctx, tenantID)
	if err != nil {
		return "", "", fmt.Errorf("count members: %w", err)
	}
	if n == 0 {
		newRole = store.OrgRoleAdmin
	}
	// Insert-only: a membership that appeared since the read above (a racing
	// login, an admin) keeps its role, and that stored role is the answer.
	stored, _, err := r.Tenants.EnsureMember(ctx, store.TenantMember{
		TenantID: tenantID, UserID: id.Subject, Role: newRole, JoinedAtMs: r.Now(),
	})
	if err != nil {
		return "", "", fmt.Errorf("provision membership: %w", err)
	}
	return tenantID, string(stored.Role), nil
}

// resolveTenant applies the mapping precedence to pick the tenant id.
func (r *Resolver) resolveTenant(ctx context.Context, id auth.Identity, defaultTenant string) (string, error) {
	if t := strings.TrimSpace(id.Tenant); t != "" {
		return t, nil
	}
	if issuer := issuerFor(id); issuer != "" {
		t, ok, err := r.Tenants.TenantForIssuer(ctx, issuer)
		if err != nil {
			return "", fmt.Errorf("resolve issuer %s: %w", issuer, err)
		}
		if ok {
			return t, nil
		}
		if r.AutoProvision {
			// A fresh org signing in via SSO for the first time: derive a stable
			// tenant id from its issuer/domain so the whole org converges on one
			// tenant without pre-registration.
			return "org-" + sanitize(issuer), nil
		}
	}
	// No IdP tenant and no issuer mapping: fall back to the user's existing
	// membership (their most-recently-joined tenant). This is what makes an
	// admin-approved join request take effect — after approval the user has a
	// membership row, so their next login lands in that tenant instead of the
	// default. A user with no membership falls through to the default.
	t, ok, err := r.Tenants.PrimaryTenantForUser(ctx, id.Subject)
	if err != nil {
		return "", fmt.Errorf("resolve primary tenant: %w", err)
	}
	if ok {
		return t, nil
	}
	if defaultTenant == "" {
		return tenant.Default, nil
	}
	return defaultTenant, nil
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
