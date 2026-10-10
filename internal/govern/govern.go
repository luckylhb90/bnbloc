// Package govern holds org-level (tenant) agent governance: an allowlist of
// borrowable providers and models, layered above the per-user disabledProviders
// preference. Where a per-user preference is a personal opt-out, a govern.Policy
// is an organization mandate: it is resolved once per tenant and consulted at
// the point a borrow is authorized, so an org can forbid a provider or model
// for every member regardless of individual grants.
//
// The policy is tenant-scoped: PolicyStore.TenantPolicy reads the tenant from
// the request context (internal/tenant) so two organizations never share a
// governance decision.
package govern

import (
	"context"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Deny reason codes returned by Policy.Can (stable wire strings).
const (
	ReasonProviderNotAllowed = "provider_not_allowed"
	ReasonModelNotAllowed    = "model_not_allowed"
)

// Policy is an organization's agent governance: which providers and models may
// be borrowed. An empty allowlist means "no restriction" (all allowed), so a
// zero Policy allows everything and is the safe back-compat default.
type Policy struct {
	// AllowedProviders is the set of borrowable providers. Empty = all allowed.
	AllowedProviders map[string]bool
	// AllowedModels is the set of borrowable models. Empty = all allowed.
	AllowedModels map[string]bool
}

// Can reports whether borrowing provider/model is permitted by this policy. It
// returns ok=true and an empty reason when allowed; otherwise ok=false with a
// stable reason code. An empty model skips the model check (provider-only), and
// an empty provider skips the provider check (model-only), so a caller can vet
// each dimension independently.
func (p Policy) Can(provider, model string) (ok bool, reason string) {
	if provider != "" && len(p.AllowedProviders) > 0 && !p.AllowedProviders[provider] {
		return false, ReasonProviderNotAllowed
	}
	if model != "" && len(p.AllowedModels) > 0 && !p.AllowedModels[model] {
		return false, ReasonModelNotAllowed
	}
	return true, ""
}

// PolicyStore resolves the governance policy for the tenant carried by ctx. A
// tenant with no configured policy yields the zero Policy (allow all).
type PolicyStore interface {
	TenantPolicy(ctx context.Context) Policy
}

// MemoryStore is an in-memory PolicyStore keyed by tenant id. It is safe for
// concurrent reads after construction; policies are set at wiring time.
type MemoryStore struct {
	byTenant map[string]Policy
}

// NewMemoryStore returns an empty in-memory policy store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byTenant: make(map[string]Policy)}
}

// Set installs the policy for a tenant, replacing any prior one.
func (m *MemoryStore) Set(tenantID string, p Policy) {
	m.byTenant[tenantID] = p
}

// TenantPolicy returns the policy for the tenant in ctx, or the zero Policy
// (allow all) when the tenant is unset or has no configured policy.
func (m *MemoryStore) TenantPolicy(ctx context.Context) Policy {
	tid := tenant.FromContext(ctx)
	if tid == "" {
		return Policy{}
	}
	return m.byTenant[tid]
}
