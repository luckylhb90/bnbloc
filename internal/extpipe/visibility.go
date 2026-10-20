package extpipe

import "github.com/luckylhb90/bnbloc/internal/store"

// One visibility function for both component registries
// (docs/prd/2026-10-12-experts-and-teams.md §1): "may member M use component
// C" is answered the same way for an executable record (platform-installed:
// rollout + tenant scope + opt-in) and a content component (scope, owner,
// status, plus the same rollout gate at platform scope). Every member
// surface — GET /v1/components, the core and artifact gates, the content
// component routes and the bridge's resolution — decides through it.

// Viewer is the member a decision is made for, in the tenant they act in.
type Viewer struct {
	Subject string
	Tenant  string
}

// Visibility is what the member may do with one component: see it (Visible:
// listed) and use it (Installed: an auto component, an opted-in one, or
// their own content); Managed marks an auto (admin-managed) install.
type Visibility struct {
	Visible       bool
	Installed     bool
	Managed       bool
	InstalledAtMs int64
}

// Gate is one component's visibility inputs, from either registry.
type Gate struct {
	// Scope is where the component is published; executable records are
	// platform scope.
	Scope store.ContentScope
	// Owner is the author of a personal component (or the holder of a
	// shared copy).
	Owner string
	// TenantID is the tenant a member-authored component lives in.
	TenantID string
	// Tenants restricts a platform-scope component (nil = every tenant).
	Tenants []string
	// Rollout is the platform/org distribution policy (auto, optin, off).
	Rollout string
	// Status is the publication state ("" = active).
	Status store.ContentStatus
}

// GateForRecord is an installed executable component's gate.
func GateForRecord(rec Record) Gate {
	return Gate{Scope: store.ContentScopePlatform, Tenants: rec.EnabledTenants, Rollout: rec.Rollout}
}

// GateForContent is a content component's gate. Personal content and a
// shared copy belong to their owner (the copy's recipient); org content
// reaches its organization and platform content every organization, both
// with rollout auto: an org admin's approval or a super admin's signed
// install is the publication, unpublishing retires it, and members opt in
// per use (an expert per message, a pack turned on), so content has no
// second opt-in switch (docs/prd/2026-10-12-experts-and-teams.md, step 4).
// Platform content takes the component tenant scope of executable records
// (EnabledTenants; "Gap closure" 3).
func GateForContent(c store.ContentComponent) Gate {
	return Gate{Scope: c.Scope, Owner: c.Owner, TenantID: c.TenantID, Tenants: c.EnabledTenants, Status: c.Status}
}

// MemberVisibility is the one gate: owner scopes admit their owner in the
// component's tenant; org scope admits its tenant; platform scope admits the
// allowed tenants; then retired content is hidden, pending content is the
// owner's alone (listed, not usable), and the rollout decides — off hides,
// optin lists and admits once the member installed it, auto admits all.
func MemberVisibility(v Viewer, g Gate, installedAtMs int64) Visibility {
	switch g.Status {
	case store.ContentStatusRetired:
		return Visibility{}
	case store.ContentStatusPending:
		if v.Subject != "" && v.Subject == g.Owner {
			return Visibility{Visible: true}
		}
		return Visibility{}
	}
	switch g.Scope {
	case store.ContentScopePersonal, store.ContentScopeShared:
		if v.Subject == "" || v.Subject != g.Owner || v.Tenant != g.TenantID {
			return Visibility{}
		}
		return Visibility{Visible: true, Installed: true}
	case store.ContentScopeOrg:
		if v.Tenant != g.TenantID {
			return Visibility{}
		}
	default:
		if !(Record{EnabledTenants: g.Tenants}).AllowsTenant(v.Tenant) {
			return Visibility{}
		}
	}
	switch NormalizeRollout(g.Rollout) {
	case RolloutOff:
		return Visibility{}
	case RolloutOptIn:
		return Visibility{Visible: true, Installed: installedAtMs > 0, InstalledAtMs: installedAtMs}
	default:
		return Visibility{Visible: true, Installed: true, Managed: true}
	}
}

// MemberVisibilityWith is MemberVisibility reading the member's opt-in
// install only when the rollout needs it (an opt-in component the member
// may see): installedAtMs returns the install time, 0 when there is none.
// Surfaces that look installs up per component (a plugin stream, a
// notifier, a notifier reply) use it instead of re-deriving the opt-in rule.
func MemberVisibilityWith(v Viewer, g Gate, installedAtMs func() int64) Visibility {
	vis := MemberVisibility(v, g, 0)
	if vis.Visible && !vis.Installed && NormalizeRollout(g.Rollout) == RolloutOptIn {
		return MemberVisibility(v, g, installedAtMs())
	}
	return vis
}

// Distribution is how an installed record reaches a tenant before any member
// is known (an event's tenant, a notifier's target list): Distributed — the
// rollout is not off and the tenant is in scope; OptIn — a member uses it
// only once they installed it.
type Distribution struct {
	Distributed bool
	OptIn       bool
}

// TenantDistribution is the tenant-only half of MemberVisibility for an
// installed record.
func TenantDistribution(rec Record, tenantID string) Distribution {
	vis := MemberVisibility(Viewer{Tenant: tenantID}, GateForRecord(rec), 0)
	return Distribution{Distributed: vis.Visible, OptIn: vis.Visible && !vis.Managed}
}
