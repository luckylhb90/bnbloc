package store

import (
	"context"
	"testing"
)

// PrimaryTenantForUser drives login routing. A real org membership must outrank
// the "default" catch-all even when the default row is newer — otherwise an
// auto-provisioned default membership (created on a user's first login) would
// shadow the org they were later approved into, and approval would silently not
// take effect at login.
func TestPrimaryTenantPrefersNonDefaultOverNewerDefault(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryTenant()
	// Real org membership joined earlier.
	if err := s.PutMember(ctx, TenantMember{TenantID: "acme", UserID: "dave", Role: OrgRoleMember, JoinedAtMs: 100}); err != nil {
		t.Fatal(err)
	}
	// A NEWER default membership (auto-provisioned later).
	if err := s.PutMember(ctx, TenantMember{TenantID: "default", UserID: "dave", Role: OrgRoleMember, JoinedAtMs: 999}); err != nil {
		t.Fatal(err)
	}
	got, ok := must2(s.PrimaryTenantForUser(ctx, "dave"))(t)
	if !ok || got != "acme" {
		t.Fatalf("PrimaryTenantForUser = %q,%v; want acme (non-default must win over newer default)", got, ok)
	}
}

// With only a default membership, that is the primary tenant.
func TestPrimaryTenantDefaultOnly(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryTenant()
	_ = s.PutMember(ctx, TenantMember{TenantID: "default", UserID: "solo", Role: OrgRoleMember, JoinedAtMs: 1})
	got, ok := must2(s.PrimaryTenantForUser(ctx, "solo"))(t)
	if !ok || got != "default" {
		t.Fatalf("PrimaryTenantForUser = %q,%v; want default", got, ok)
	}
}

// Among multiple real org memberships, the most-recently-joined wins.
func TestPrimaryTenantMostRecentAmongOrgs(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryTenant()
	_ = s.PutMember(ctx, TenantMember{TenantID: "acme", UserID: "multi", Role: OrgRoleMember, JoinedAtMs: 100})
	_ = s.PutMember(ctx, TenantMember{TenantID: "globex", UserID: "multi", Role: OrgRoleMember, JoinedAtMs: 200})
	got, _ := must2(s.PrimaryTenantForUser(ctx, "multi"))(t)
	if got != "globex" {
		t.Fatalf("PrimaryTenantForUser = %q; want globex (most recent org)", got)
	}
}

// A user with no membership has no primary tenant.
func TestPrimaryTenantNone(t *testing.T) {
	got, ok := must2(NewMemoryTenant().PrimaryTenantForUser(context.Background(), "ghost"))(t)
	if ok || got != "" {
		t.Fatalf("PrimaryTenantForUser = %q,%v; want empty,false", got, ok)
	}
}
