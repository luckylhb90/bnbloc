package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func newTestFamily(id, subject, tenantID, role string, nowMs int64) RefreshFamily {
	return RefreshFamily{
		FamilyID: id, Subject: subject, TenantID: tenantID, Role: role, Client: "web",
		CurrentJTI: id + "-j1", RotatedAtMs: nowMs, CreatedAtMs: nowMs, LastUsedAtMs: nowMs,
		ExpiresAtMs: nowMs + 1_000_000,
	}
}

// Rotation, the concurrent-tab grace, reuse revocation and listing; a taken
// family id is ErrRefreshFamilyExists.
func TestContractRefreshTokenStoreRotationAndReuse(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s := b.RefreshTokens
		ctx := context.Background()
		id, subject := uniq("fam-"), uniq("ann-")
		f := newTestFamily(id, subject, "t1", "member", 1000)
		f.Email, f.TenantHint, f.UserAgent, f.ParentFamilyID = "ann@example.com", "hint", "ua", "parent-"+id
		if err := s.CreateRefreshFamily(ctx, f); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateRefreshFamily(ctx, f); !errors.Is(err, ErrRefreshFamilyExists) {
			t.Fatalf("duplicate create = %v, want ErrRefreshFamilyExists", err)
		}
		if got, ok := must2(s.GetRefreshFamily(ctx, id))(t); !ok || got != f {
			t.Fatalf("get = %+v %v, want %+v", got, ok, f)
		}
		// Rotating the current jti moves the family forward.
		got, err := s.UseRefreshFamily(ctx, RefreshUse{FamilyID: id, PresentedJTI: id + "-j1", NewJTI: id + "-j2", NowMs: 2000, GraceMs: 60_000, ExpiresAtMs: 5_000_000, TenantID: "t2", Role: "org_admin"})
		if err != nil || got.CurrentJTI != id+"-j2" || got.PreviousJTI != id+"-j1" || got.ExpiresAtMs != 5_000_000 || got.TenantID != "t2" || got.Role != "org_admin" {
			t.Fatalf("rotate = %+v, %v", got, err)
		}
		if stored, _ := must2(s.GetRefreshFamily(ctx, id))(t); stored != got {
			t.Fatalf("stored after rotate = %+v, want %+v", stored, got)
		}
		// Within the grace window the previous jti is a normal use that
		// yields the CURRENT jti (two tabs refreshing at once), without
		// rotating again.
		got, err = s.UseRefreshFamily(ctx, RefreshUse{FamilyID: id, PresentedJTI: id + "-j1", NewJTI: id + "-j3", NowMs: 30_000, GraceMs: 60_000})
		if err != nil || got.CurrentJTI != id+"-j2" {
			t.Fatalf("grace use = %+v, %v; want current %s-j2", got, err, id)
		}
		if fams := must(s.ListRefreshFamilies(ctx, subject, 30_001))(t); len(fams) != 1 || fams[0].FamilyID != id {
			t.Fatalf("list = %+v", fams)
		}
		// After the grace window the previous jti is reuse: the family is revoked.
		if _, err := s.UseRefreshFamily(ctx, RefreshUse{FamilyID: id, PresentedJTI: id + "-j1", NewJTI: id + "-j4", NowMs: 100_000, GraceMs: 60_000}); !errors.Is(err, ErrRefreshReuse) {
			t.Fatalf("stale jti err = %v; want reuse", err)
		}
		// ...and the current jti no longer works either.
		if _, err := s.UseRefreshFamily(ctx, RefreshUse{FamilyID: id, PresentedJTI: id + "-j2", NewJTI: id + "-j5", NowMs: 100_001, GraceMs: 60_000}); !errors.Is(err, ErrRefreshInvalid) {
			t.Fatalf("revoked family err = %v; want invalid", err)
		}
		if fams := must(s.ListRefreshFamilies(ctx, subject, 100_002))(t); fams == nil || len(fams) != 0 {
			t.Fatalf("revoked family still listed: %#v (want [])", fams)
		}
	})
}

// Concurrent rotations of the current token: one rotates, the other lands in
// the grace window; neither is reuse.
func TestContractRefreshTokenStoreConcurrentRotation(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s := b.RefreshTokens
		ctx := context.Background()
		id := uniq("fam-")
		if err := s.CreateRefreshFamily(ctx, newTestFamily(id, uniq("ann-"), "t1", "member", 1000)); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := range errs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, errs[i] = s.UseRefreshFamily(ctx, RefreshUse{FamilyID: id, PresentedJTI: id + "-j1", NewJTI: id + "-n" + string(rune('a'+i)), NowMs: 1010, GraceMs: 60_000, ExpiresAtMs: 2_000_000})
			}(i)
		}
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("concurrent rotations = %v; want both accepted", errs)
		}
		if got, _ := must2(s.GetRefreshFamily(ctx, id))(t); got.PreviousJTI != id+"-j1" || got.ExpiresAtMs != 2_000_000 {
			t.Fatalf("after rotation = %+v", got)
		}
	})
}

// An unknown family and an expired one are invalid; a family is live
// strictly before its expiry (valid at expires-1, invalid at expires), and
// creating a family prunes the ones expired at its creation.
func TestContractRefreshTokenStoreExpiry(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s := b.RefreshTokens
		ctx := context.Background()
		if _, err := s.UseRefreshFamily(ctx, RefreshUse{FamilyID: uniq("nope-"), PresentedJTI: "x", NowMs: 1}); !errors.Is(err, ErrRefreshInvalid) {
			t.Fatalf("unknown err = %v", err)
		}
		subject := uniq("ann-")
		edge := newTestFamily(uniq("fam-"), subject, "t1", "", 1000)
		edge.ExpiresAtMs = 2000
		if err := s.CreateRefreshFamily(ctx, edge); err != nil {
			t.Fatal(err)
		}
		if fams := must(s.ListRefreshFamilies(ctx, subject, 1999))(t); len(fams) != 1 {
			t.Fatalf("listed at expiry-1 = %+v", fams)
		}
		if fams := must(s.ListRefreshFamilies(ctx, subject, 2000))(t); len(fams) != 0 {
			t.Fatalf("listed at expiry = %+v", fams)
		}
		if _, err := s.UseRefreshFamily(ctx, RefreshUse{FamilyID: edge.FamilyID, PresentedJTI: edge.CurrentJTI, NowMs: 2000}); !errors.Is(err, ErrRefreshInvalid) {
			t.Fatalf("use at expiry = %v, want invalid", err)
		}
		if _, err := s.UseRefreshFamily(ctx, RefreshUse{FamilyID: edge.FamilyID, PresentedJTI: edge.CurrentJTI, NowMs: 1999}); err != nil {
			t.Fatalf("use just before expiry = %v", err)
		}
		// A family created at 2000 prunes the one expired then.
		if err := s.CreateRefreshFamily(ctx, newTestFamily(uniq("fam-"), subject, "t1", "", 2000)); err != nil {
			t.Fatal(err)
		}
		if _, ok := must2(s.GetRefreshFamily(ctx, edge.FamilyID))(t); ok {
			t.Fatal("expired family not pruned")
		}
	})
}

// Single revoke is subject-scoped and idempotent; bulk revoke selects by
// subject and/or tenant and spares an excepted role; an empty filter and a
// revoke without its time are refused.
func TestContractRefreshTokenStoreRevocation(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s := b.RefreshTokens
		ctx := context.Background()
		ann, bob, root := uniq("ann-"), uniq("bob-"), uniq("root-")
		t1, t2 := uniq("t1-"), uniq("t2-")
		a1, a2, b1, r1 := uniq("a1-"), uniq("a2-"), uniq("b1-"), uniq("r1-")
		for _, f := range []RefreshFamily{
			newTestFamily(a1, ann, t1, "member", 1000),
			newTestFamily(a2, ann, t2, "member", 1000),
			newTestFamily(b1, bob, t1, "org_admin", 1000),
			newTestFamily(r1, root, t1, "super_admin", 1000),
		} {
			if err := s.CreateRefreshFamily(ctx, f); err != nil {
				t.Fatal(err)
			}
		}
		if ok, err := s.RevokeRefreshFamily(ctx, bob, a1, 2000); err != nil || ok {
			t.Fatalf("bob revoking ann's family = %v, %v", ok, err)
		}
		if _, err := s.RevokeRefreshFamily(ctx, ann, a1, 0); !errors.Is(err, ErrRevokeTimeRequired) {
			t.Fatalf("revoke at 0 = %v, want ErrRevokeTimeRequired", err)
		}
		if ok, err := s.RevokeRefreshFamily(ctx, ann, a1, 2000); err != nil || !ok {
			t.Fatalf("ann revoking her family = %v, %v", ok, err)
		}
		if got, _ := must2(s.GetRefreshFamily(ctx, a1))(t); got.RevokedAtMs != 2000 {
			t.Fatalf("revoked at = %d", got.RevokedAtMs)
		}
		if ok, err := s.RevokeRefreshFamily(ctx, ann, a1, 2001); err != nil || ok {
			t.Fatalf("second revoke = %v, %v; want false", ok, err)
		}
		// Member removal: subject in one tenant only.
		if n, err := s.RevokeRefreshFamilies(ctx, RefreshRevokeFilter{Subject: ann, TenantID: t2}, 3000); err != nil || n != 1 {
			t.Fatalf("subject+tenant revoked %d, %v; want 1", n, err)
		}
		if _, err := s.RevokeRefreshFamilies(ctx, RefreshRevokeFilter{TenantID: t1}, 0); !errors.Is(err, ErrRevokeTimeRequired) {
			t.Fatalf("bulk revoke at 0 = %v, want ErrRevokeTimeRequired", err)
		}
		// Tenant suspension spares super-admins.
		if n, err := s.RevokeRefreshFamilies(ctx, RefreshRevokeFilter{TenantID: t1, ExceptRole: "super_admin"}, 4000); err != nil || n != 1 {
			t.Fatalf("tenant revoke = %d, %v; want 1 (bob)", n, err)
		}
		if fams := must(s.ListRefreshFamilies(ctx, root, 4001))(t); len(fams) != 1 {
			t.Fatalf("super-admin family revoked by suspension: %+v", fams)
		}
		if _, err := s.RevokeRefreshFamilies(ctx, RefreshRevokeFilter{}, 5000); err == nil {
			t.Fatal("empty filter must be refused")
		}
	})
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
