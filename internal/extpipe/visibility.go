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

// GateForContent is a content component's gate. Personal and shared
// content belongs to its owner; org and platform scopes take the rollout
// gate (their rollout and tenant records arrive with org approval and
// platform publishing; until then they distribute to every member).
func GateForContent(c store.ContentComponent) Gate {
	return Gate{Scope: c.Scope, Owner: c.Owner, TenantID: c.TenantID, Status: c.Status}
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
