package govern

import (
	"context"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

func TestPolicyCan_EmptyAllowsAll(t *testing.T) {
	var p Policy // zero value: empty allowlists
	if ok, reason := p.Can("openai", "gpt-5"); !ok || reason != "" {
		t.Fatalf("empty policy must allow all: ok=%v reason=%q", ok, reason)
	}
	if ok, reason := p.Can("anthropic", "claude-x"); !ok || reason != "" {
		t.Fatalf("empty policy must allow all providers/models: ok=%v reason=%q", ok, reason)
	}
}

func TestPolicyCan_ProviderAllowlist(t *testing.T) {
	p := Policy{AllowedProviders: map[string]bool{"openai": true}}
	if ok, _ := p.Can("openai", ""); !ok {
		t.Fatalf("listed provider must be allowed")
	}
	ok, reason := p.Can("anthropic", "")
	if ok || reason != ReasonProviderNotAllowed {
		t.Fatalf("non-listed provider must be denied provider_not_allowed: ok=%v reason=%q", ok, reason)
	}
}

func TestPolicyCan_ModelAllowlist(t *testing.T) {
	p := Policy{AllowedModels: map[string]bool{"gpt-5": true}}
	if ok, _ := p.Can("", "gpt-5"); !ok {
		t.Fatalf("listed model must be allowed")
	}
	ok, reason := p.Can("", "gpt-4o")
	if ok || reason != ReasonModelNotAllowed {
		t.Fatalf("non-listed model must be denied model_not_allowed: ok=%v reason=%q", ok, reason)
	}
}

func TestPolicyCan_ProviderCheckedBeforeModel(t *testing.T) {
	p := Policy{
		AllowedProviders: map[string]bool{"openai": true},
		AllowedModels:    map[string]bool{"gpt-5": true},
	}
	// Both dimensions violated: provider reason wins (checked first).
	ok, reason := p.Can("anthropic", "claude-x")
	if ok || reason != ReasonProviderNotAllowed {
		t.Fatalf("provider denial must take precedence: ok=%v reason=%q", ok, reason)
	}
	// Provider allowed but model not.
	ok, reason = p.Can("openai", "gpt-4o")
	if ok || reason != ReasonModelNotAllowed {
		t.Fatalf("allowed provider + bad model must deny model_not_allowed: ok=%v reason=%q", ok, reason)
	}
}

func TestPolicyCan_EmptyProviderOrModelSkipsThatCheck(t *testing.T) {
	p := Policy{
		AllowedProviders: map[string]bool{"openai": true},
		AllowedModels:    map[string]bool{"gpt-5": true},
	}
	// Empty model: only the provider dimension is vetted.
	if ok, reason := p.Can("openai", ""); !ok || reason != "" {
		t.Fatalf("empty model must skip model check: ok=%v reason=%q", ok, reason)
	}
	// Empty provider: only the model dimension is vetted.
	if ok, reason := p.Can("", "gpt-5"); !ok || reason != "" {
		t.Fatalf("empty provider must skip provider check: ok=%v reason=%q", ok, reason)
	}
}

func TestMemoryStore_TenantsAreIndependent(t *testing.T) {
	store := NewMemoryStore()
	store.Set("t-alpha", Policy{AllowedProviders: map[string]bool{"openai": true}})
	store.Set("t-beta", Policy{AllowedProviders: map[string]bool{"anthropic": true}})

	alpha := store.TenantPolicy(tenant.NewContext(context.Background(), "t-alpha"))
	if ok, _ := alpha.Can("openai", ""); !ok {
		t.Fatalf("alpha must allow openai")
	}
	if ok, reason := alpha.Can("anthropic", ""); ok || reason != ReasonProviderNotAllowed {
		t.Fatalf("alpha must deny anthropic: ok=%v reason=%q", ok, reason)
	}

	beta := store.TenantPolicy(tenant.NewContext(context.Background(), "t-beta"))
	if ok, _ := beta.Can("anthropic", ""); !ok {
		t.Fatalf("beta must allow anthropic")
	}
	if ok, reason := beta.Can("openai", ""); ok || reason != ReasonProviderNotAllowed {
		t.Fatalf("beta must deny openai: ok=%v reason=%q", ok, reason)
	}
}

func TestMemoryStore_UnknownOrEmptyTenantAllowsAll(t *testing.T) {
	store := NewMemoryStore()
	store.Set("t-alpha", Policy{AllowedProviders: map[string]bool{"openai": true}})

	// Tenant with no configured policy => zero Policy (allow all).
	unknown := store.TenantPolicy(tenant.NewContext(context.Background(), "t-unknown"))
	if ok, reason := unknown.Can("anthropic", "claude-x"); !ok || reason != "" {
		t.Fatalf("unconfigured tenant must allow all: ok=%v reason=%q", ok, reason)
	}
	// No tenant in ctx => allow all.
	none := store.TenantPolicy(context.Background())
	if ok, reason := none.Can("anthropic", "claude-x"); !ok || reason != "" {
		t.Fatalf("missing tenant must allow all: ok=%v reason=%q", ok, reason)
	}
}
