package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func newTestFamily(id, subject, tenantID, role string, nowMs int64) RefreshFamily {
	return RefreshFamily{
		FamilyID: id, Subject: subject, TenantID: tenantID, Role: role, Client: "web",
		CurrentJTI: id + "-j1", RotatedAtMs: nowMs, CreatedAtMs: nowMs, LastUsedAtMs: nowMs,
		ExpiresAtMs: nowMs + 1_000_000,
	}
}

func TestRefreshFamilyRotationAndReuse(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryRefreshToken()
	if err := s.CreateRefreshFamily(ctx, newTestFamily("f1", "ann", "t1", "member", 1000)); err != nil {
		t.Fatal(err)
	}
	// Rotating the current jti moves the family forward.
	f, err := s.UseRefreshFamily(ctx, RefreshUse{FamilyID: "f1", PresentedJTI: "f1-j1", NewJTI: "f1-j2", NowMs: 2000, GraceMs: 60_000, ExpiresAtMs: 5_000_000})
	if err != nil || f.CurrentJTI != "f1-j2" || f.PreviousJTI != "f1-j1" || f.ExpiresAtMs != 5_000_000 {
		t.Fatalf("rotate = %+v, %v", f, err)
	}
	// Within the grace window the previous jti is a normal use that yields
	// the CURRENT jti (two tabs refreshing at once), without rotating again.
	f, err = s.UseRefreshFamily(ctx, RefreshUse{FamilyID: "f1", PresentedJTI: "f1-j1", NewJTI: "f1-j3", NowMs: 30_000, GraceMs: 60_000})
	if err != nil || f.CurrentJTI != "f1-j2" {
		t.Fatalf("grace use = %+v, %v; want current f1-j2", f, err)
	}
	// After the grace window the previous jti is reuse: the family is revoked.
	_, err = s.UseRefreshFamily(ctx, RefreshUse{FamilyID: "f1", PresentedJTI: "f1-j1", NewJTI: "f1-j4", NowMs: 100_000, GraceMs: 60_000})
	if !errors.Is(err, ErrRefreshReuse) {
		t.Fatalf("stale jti err = %v; want reuse", err)
	}
	// ...and the current jti no longer works either.
	if _, err := s.UseRefreshFamily(ctx, RefreshUse{FamilyID: "f1", PresentedJTI: "f1-j2", NewJTI: "f1-j5", NowMs: 100_001, GraceMs: 60_000}); !errors.Is(err, ErrRefreshInvalid) {
		t.Fatalf("revoked family err = %v; want invalid", err)
	}
	if fams, _ := s.ListRefreshFamilies(ctx, "ann", 100_002); len(fams) != 0 {
		t.Fatalf("revoked family still listed: %+v", fams)
	}
}

func TestRefreshFamilyUnknownAndExpired(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryRefreshToken()
	if _, err := s.UseRefreshFamily(ctx, RefreshUse{FamilyID: "nope", PresentedJTI: "x", NowMs: 1}); !errors.Is(err, ErrRefreshInvalid) {
		t.Fatalf("unknown err = %v", err)
	}
	f := newTestFamily("f1", "ann", "t1", "", 1000)
	f.ExpiresAtMs = 2000
	_ = s.CreateRefreshFamily(ctx, f)
	if _, err := s.UseRefreshFamily(ctx, RefreshUse{FamilyID: "f1", PresentedJTI: "f1-j1", NowMs: 3000}); !errors.Is(err, ErrRefreshInvalid) {
		t.Fatalf("expired err = %v", err)
	}
	// Creating a later family prunes the expired one.
	_ = s.CreateRefreshFamily(ctx, newTestFamily("f2", "ann", "t1", "", 5000))
	if _, ok, _ := s.GetRefreshFamily(ctx, "f1"); ok {
		t.Fatal("expired family not pruned")
	}
}

func TestRefreshFamilyRevocation(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryRefreshToken()
	for _, f := range []RefreshFamily{
		newTestFamily("a1", "ann", "t1", "member", 1000),
		newTestFamily("a2", "ann", "t2", "member", 1000),
		newTestFamily("b1", "bob", "t1", "org_admin", 1000),
		newTestFamily("r1", "root", "t1", "super_admin", 1000),
	} {
		if err := s.CreateRefreshFamily(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	// Single revoke is subject-scoped and idempotent.
	if ok, _ := s.RevokeRefreshFamily(ctx, "bob", "a1", 2000); ok {
		t.Fatal("bob revoked ann's family")
	}
	if ok, _ := s.RevokeRefreshFamily(ctx, "ann", "a1", 2000); !ok {
		t.Fatal("ann could not revoke her family")
	}
	if ok, _ := s.RevokeRefreshFamily(ctx, "ann", "a1", 2001); ok {
		t.Fatal("second revoke reported a change")
	}
	// Member removal: subject in one tenant only.
	if n, _ := s.RevokeRefreshFamilies(ctx, RefreshRevokeFilter{Subject: "ann", TenantID: "t2"}, 3000); n != 1 {
		t.Fatalf("subject+tenant revoked %d; want 1", n)
	}
	// Tenant suspension spares super-admins.
	if n, _ := s.RevokeRefreshFamilies(ctx, RefreshRevokeFilter{TenantID: "t1", ExceptRole: "super_admin"}, 4000); n != 1 {
		t.Fatalf("tenant revoke = %d; want 1 (bob)", n)
	}
	if fams, _ := s.ListRefreshFamilies(ctx, "root", 4001); len(fams) != 1 {
		t.Fatalf("super-admin family revoked by suspension: %+v", fams)
	}
	if _, err := s.RevokeRefreshFamilies(ctx, RefreshRevokeFilter{}, 5000); err == nil {
		t.Fatal("empty filter must be refused")
	}
}

func TestSchemaTableDDLExtractsNewTables(t *testing.T) {
	for _, table := range []string{"sso_handoff_codes", "refresh_token_families"} {
		ddl, err := schemaTableDDL(table)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(ddl, "CREATE TABLE IF NOT EXISTS "+table+" (") || !strings.HasSuffix(ddl, "utf8mb4") {
			t.Fatalf("ddl for %s = %q", table, ddl)
		}
	}
	if _, err := schemaTableDDL("no_such_table"); err == nil {
		t.Fatal("missing table must error")
	}
}

func TestMemorySSOHandoffSingleUse(t *testing.T) {
	ctx := context.Background()
	s := NewMemorySSOHandoff()
	h := SSOHandoff{KeyHash: "k1", NonceHash: "n1", Subject: "ann", ExpiresAtMs: 2000}
	if err := s.PutSSOHandoff(ctx, h, 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSSOHandoff(ctx, h, 1000); !errors.Is(err, ErrSSOHandoffPending) {
		t.Fatalf("second put err = %v; want pending", err)
	}
	got, ok, err := s.TakeSSOHandoff(ctx, "k1", 1500)
	if err != nil || !ok || got.Subject != "ann" {
		t.Fatalf("take = %+v %v %v", got, ok, err)
	}
	if _, ok, _ := s.TakeSSOHandoff(ctx, "k1", 1500); ok {
		t.Fatal("hand-off redeemed twice")
	}
	_ = s.PutSSOHandoff(ctx, SSOHandoff{KeyHash: "k2", ExpiresAtMs: 2000}, 1000)
	if _, ok, _ := s.TakeSSOHandoff(ctx, "k2", 2001); ok {
		t.Fatal("expired hand-off redeemed")
	}
	// An expired entry under a key is replaced, not "pending".
	_ = s.PutSSOHandoff(ctx, SSOHandoff{KeyHash: "k3", ExpiresAtMs: 2000}, 1000)
	if err := s.PutSSOHandoff(ctx, SSOHandoff{KeyHash: "k3", ExpiresAtMs: 9000}, 3000); err != nil {
		t.Fatalf("put over expired = %v", err)
	}
}
