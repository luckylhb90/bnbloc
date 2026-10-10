package shareagenthttp

import (
	"context"
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// tctx scopes a request/seed to a specific tenant, matching what
// svcauth.EnsureTenant injects for real HTTP requests.
func tctx(id string) context.Context {
	return tenant.NewContext(context.Background(), id)
}

// seedAuthorizedBorrow puts a LIVE room authorization for owner "alice" in the
func seedAuthorizedBorrow(t *testing.T, gs *store.MemoryGrant, tenantID string) {
	t.Helper()
	future := int64(1_000_000_000_000)
	if err := gs.PutGrant(tctx(tenantID), store.Grant{
		GrantID:       roomAuthGrantID(tctx(tenantID), "room-1", "alice", "codex"),
		RoomID:        "room-1",
		TargetID:      "shared-agent:codex",
		OwnerUserID:   "alice",
		GranteeUserID: "*",
		AllowedModels: []store.AllowedModel{{Model: "gpt-5"}},
		Capabilities:  store.Capabilities{Consult: true},
		MaxConcurrent: 4,
		ExpiresAtMs:   &future,
	}); err != nil {
		t.Fatalf("seed authorization: %v", err)
	}
}

// recordBorrow is one whole borrowed run of alice's codex by bob in
// tenantID (reserve, then settle with cost), returning the refusal or the
// settle's answer.
func recordBorrow(t *testing.T, svc *Service, tenantID string, cost float64, channelID string) (int, map[string]any) {
	t.Helper()
	return borrowRun(t, svc, tenantID, "alice", "codex", "bob", channelID, map[string]any{"model": "gpt-5", "cost": cost})
}

// A tenant's PERSISTED cost allowance is enforced: once aggregate cost reaches
// the ceiling, further borrows are denied with org_quota_exhausted. The old
// code enforced only a run ceiling and only from a process-global field, so a
// cost-only ceiling would never trigger — this fails against that bug.
func TestRecordBorrowEnforcesTenantCostCeiling(t *testing.T) {
	svc, gs := newService(t, "")
	tenants := store.NewMemoryTenant()
	_ = tenants.PutTenant(tctx(tenant.Default), store.Tenant{
		TenantID: tenant.Default, Name: "Default", Status: store.TenantActive,
	})
	// Cost ceiling 1.0, no run ceiling.
	if err := tenants.SetQuota(tctx(tenant.Default), tenant.Default, 0, 1.0); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	svc.Tenants = tenants
	seedAuthorizedBorrow(t, gs, tenant.Default)

	// First borrow (cost 0.8) is under the ceiling and is recorded.
	if code, body := recordBorrow(t, svc, tenant.Default, 0.8, ""); code != http.StatusOK || body["recorded"] != true {
		t.Fatalf("first borrow: code=%d body=%v (want 200 recorded)", code, body)
	}
	// Aggregate cost (0.8) now equals-or-exceeds nothing yet; drive it over 1.0.
	if code, body := recordBorrow(t, svc, tenant.Default, 0.8, ""); code != http.StatusOK || body["recorded"] != true {
		t.Fatalf("second borrow: code=%d body=%v (want 200 recorded)", code, body)
	}
	// Aggregate cost is now 1.6 >= 1.0: the next borrow is denied on cost.
	code, body := recordBorrow(t, svc, tenant.Default, 0.1, "")
	if code != http.StatusForbidden || body["denyReason"] != "org_quota_exhausted" {
		t.Fatalf("over-ceiling borrow: code=%d body=%v (want 403 org_quota_exhausted)", code, body)
	}
}

// A tenant's ceiling applies only to that tenant. The old code read a
// process-global TenantRunQuota field, so setting one tenant's quota would
// wrongly gate every other tenant. Here tenant "acme" has a run ceiling of 1
// and is exhausted, while tenant "globex" has no ceiling and must still borrow.
func TestRecordBorrowQuotaIsPerTenant(t *testing.T) {
	svc, gs := newService(t, "")
	tenants := store.NewMemoryTenant()
	for _, id := range []string{"acme", "globex"} {
		_ = tenants.PutTenant(tctx(id), store.Tenant{TenantID: id, Name: id, Status: store.TenantActive})
	}
	// acme: run ceiling 1. globex: unlimited.
	if err := tenants.SetQuota(tctx("acme"), "acme", 1, 0); err != nil {
		t.Fatalf("set acme quota: %v", err)
	}
	svc.Tenants = tenants
	seedAuthorizedBorrow(t, gs, "acme")
	seedAuthorizedBorrow(t, gs, "globex")

	// acme: first borrow recorded, second denied (run ceiling 1 reached).
	if code, _ := recordBorrow(t, svc, "acme", 0, ""); code != http.StatusOK {
		t.Fatalf("acme first borrow code=%d (want 200)", code)
	}
	if code, body := recordBorrow(t, svc, "acme", 0, ""); code != http.StatusForbidden || body["denyReason"] != "org_quota_exhausted" {
		t.Fatalf("acme second borrow code=%d body=%v (want 403 exhausted)", code, body)
	}
	// globex must be unaffected by acme's exhausted ceiling: it borrows freely.
	if code, body := recordBorrow(t, svc, "globex", 0, ""); code != http.StatusOK || body["recorded"] != true {
		t.Fatalf("globex borrow code=%d body=%v (want 200 recorded — quota leaked across tenants)", code, body)
	}
	if code, body := recordBorrow(t, svc, "globex", 0, ""); code != http.StatusOK || body["recorded"] != true {
		t.Fatalf("globex second borrow code=%d body=%v (want 200 recorded)", code, body)
	}
}
