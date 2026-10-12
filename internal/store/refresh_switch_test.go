package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A tenant switch moves an active family (its hint and scope) without
// rotating it; only the family's own subject switches it, and never a
// revoked one. Both backends follow the one rule (decideRefreshSwitch).
func testRefreshSwitch(t *testing.T, s RefreshTokenStore, id, subject string, now int64) {
	t.Helper()
	ctx := context.Background()
	f := newTestFamily(id, subject, "t1", "member", now)
	f.TenantHint = "t1"
	if err := s.CreateRefreshFamily(ctx, f); err != nil {
		t.Fatal(err)
	}
	sw := RefreshSwitch{FamilyID: id, Subject: subject, TenantHint: "t2", TenantID: "t2", Role: "org_admin", NowMs: now + 1}
	got, err := s.SwitchRefreshFamily(ctx, sw)
	if err != nil || got.TenantHint != "t2" || got.TenantID != "t2" || got.Role != "org_admin" || got.CurrentJTI != f.CurrentJTI {
		t.Fatalf("switch = %+v, %v", got, err)
	}
	stored, _, _ := s.GetRefreshFamily(ctx, id)
	if stored.TenantHint != "t2" || stored.TenantID != "t2" || stored.CurrentJTI != f.CurrentJTI {
		t.Fatalf("stored after switch = %+v", stored)
	}
	other := sw
	other.Subject = subject + "-other"
	if _, err := s.SwitchRefreshFamily(ctx, other); !errors.Is(err, ErrRefreshInvalid) {
		t.Fatalf("another subject's switch = %v, want invalid", err)
	}
	if _, err := s.RevokeRefreshFamily(ctx, subject, id, now+2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SwitchRefreshFamily(ctx, sw); !errors.Is(err, ErrRefreshInvalid) {
		t.Fatalf("revoked family switch = %v, want invalid", err)
	}
	if _, err := s.SwitchRefreshFamily(ctx, RefreshSwitch{FamilyID: id + "-none", Subject: subject, TenantHint: "t2", NowMs: now}); !errors.Is(err, ErrRefreshInvalid) {
		t.Fatalf("unknown family switch = %v, want invalid", err)
	}
}

func TestRefreshFamilySwitch(t *testing.T) {
	testRefreshSwitch(t, NewMemoryRefreshToken(), "f1", "ann", 1000)
}

func TestMySQLRefreshFamilySwitch(t *testing.T) {
	_, refresh := mysqlAuthState(t)
	id := uniqueHex(t, "fam")[:24]
	testRefreshSwitch(t, refresh, id, "subj-"+id[:12], time.Now().UnixMilli())
}
