package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

// mysqlAuthState opens the real MySQL auth-state stores (DSN-gated like the
// other MySQL integration tests).
func mysqlAuthState(t *testing.T) (*MySQLSSOHandoff, *MySQLRefreshToken) {
	t.Helper()
	dsn := os.Getenv("BNBLOC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set BNBLOC_TEST_MYSQL_DSN to run the MySQL auth-state tests")
	}
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	t.Cleanup(func() { b.DB.Close() })
	return b.SSOHandoffs.(*MySQLSSOHandoff), b.RefreshTokens.(*MySQLRefreshToken)
}

func uniqueHex(t *testing.T, label string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(label + t.Name() + time.Now().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:])
}

// The conditional UPDATE is the cross-replica single-use gate: of many
// concurrent takers exactly one wins.
func TestMySQLSSOHandoffSingleUseUnderConcurrency(t *testing.T) {
	handoffs, _ := mysqlAuthState(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	key := uniqueHex(t, "key")
	h := SSOHandoff{KeyHash: key, NonceHash: uniqueHex(t, "nonce"), Subject: "ann", Email: "ann@corp", TenantHint: "t1", ExpiresAtMs: now + 60_000}
	if err := handoffs.PutSSOHandoff(ctx, h, now); err != nil {
		t.Fatal(err)
	}
	if err := handoffs.PutSSOHandoff(ctx, h, now); !errors.Is(err, ErrSSOHandoffPending) {
		t.Fatalf("second put err = %v; want pending", err)
	}
	var wins int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, ok, err := handoffs.TakeSSOHandoff(ctx, key, now+1)
			if err != nil {
				t.Errorf("take: %v", err)
				return
			}
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
				if got.Subject != "ann" || got.Email != "ann@corp" || got.TenantHint != "t1" || got.NonceHash != h.NonceHash {
					t.Errorf("taken = %+v", got)
				}
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("winners = %d; want exactly 1", wins)
	}
	// Expired hand-offs are never redeemed and are pruned on the next put.
	old := SSOHandoff{KeyHash: uniqueHex(t, "old"), NonceHash: "n", Subject: "ann", ExpiresAtMs: now - 1}
	if err := handoffs.PutSSOHandoff(ctx, old, now-10); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := handoffs.TakeSSOHandoff(ctx, old.KeyHash, now); ok {
		t.Fatal("expired hand-off redeemed")
	}
	if err := handoffs.PutSSOHandoff(ctx, SSOHandoff{KeyHash: uniqueHex(t, "new"), NonceHash: "n", Subject: "x", ExpiresAtMs: now + 1000}, now); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := handoffs.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sso_handoff_codes WHERE key_hash = ?`, old.KeyHash).Scan(&n); err != nil || n != 0 {
		t.Fatalf("expired row not pruned: n=%d err=%v", n, err)
	}
}

// Rotation, the concurrent-tab grace, reuse revocation, listing, and bulk
// revocation on the MySQL backend.
func TestMySQLRefreshFamilyLifecycle(t *testing.T) {
	_, refresh := mysqlAuthState(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	id := uniqueHex(t, "fam")[:32]
	subject := "subj-" + id[:12]
	f := RefreshFamily{
		FamilyID: id, Subject: subject, Email: "s@corp", TenantHint: "hint", TenantID: "t-" + id[:8], Role: "member",
		Client: "web", UserAgent: "test", CurrentJTI: "j1", RotatedAtMs: now, CreatedAtMs: now, LastUsedAtMs: now,
		ExpiresAtMs: now + 1_000_000, ParentFamilyID: "parent-" + id[:8],
	}
	if err := refresh.CreateRefreshFamily(ctx, f); err != nil {
		t.Fatal(err)
	}
	got, ok, err := refresh.GetRefreshFamily(ctx, id)
	if err != nil || !ok || got.Email != "s@corp" || got.TenantHint != "hint" || got.RevokedAtMs != 0 || got.ParentFamilyID != f.ParentFamilyID {
		t.Fatalf("get = %+v %v %v", got, ok, err)
	}
	// Concurrent rotations of the current token: one rotates, the other lands
	// in the grace window; neither is reuse.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = refresh.UseRefreshFamily(ctx, RefreshUse{FamilyID: id, PresentedJTI: "j1", NewJTI: "j2-" + string(rune('a'+i)), NowMs: now + 10, GraceMs: 60_000, ExpiresAtMs: now + 2_000_000, TenantID: f.TenantID, Role: "member"})
		}(i)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("concurrent rotations = %v; want both accepted", errs)
	}
	got, _, _ = refresh.GetRefreshFamily(ctx, id)
	if got.PreviousJTI != "j1" || got.ExpiresAtMs != now+2_000_000 {
		t.Fatalf("after rotation = %+v", got)
	}
	if fams, _ := refresh.ListRefreshFamilies(ctx, subject, now+20); len(fams) != 1 {
		t.Fatalf("list = %+v", fams)
	}
	// After the grace window the old token is reuse: family revoked.
	if _, err := refresh.UseRefreshFamily(ctx, RefreshUse{FamilyID: id, PresentedJTI: "j1", NewJTI: "j3", NowMs: now + 120_000, GraceMs: 60_000}); !errors.Is(err, ErrRefreshReuse) {
		t.Fatalf("reuse err = %v", err)
	}
	if _, err := refresh.UseRefreshFamily(ctx, RefreshUse{FamilyID: id, PresentedJTI: got.CurrentJTI, NewJTI: "j4", NowMs: now + 120_001, GraceMs: 60_000}); !errors.Is(err, ErrRefreshInvalid) {
		t.Fatalf("revoked family err = %v", err)
	}
	if fams, _ := refresh.ListRefreshFamilies(ctx, subject, now+120_002); len(fams) != 0 {
		t.Fatalf("revoked family listed: %+v", fams)
	}

	// Bulk + single revocation.
	id2, id3 := uniqueHex(t, "f2")[:32], uniqueHex(t, "f3")[:32]
	for _, x := range []RefreshFamily{
		{FamilyID: id2, Subject: subject, TenantID: f.TenantID, Role: "member", CurrentJTI: "a", ExpiresAtMs: now + 1_000_000, CreatedAtMs: now},
		{FamilyID: id3, Subject: subject + "-sa", TenantID: f.TenantID, Role: "super_admin", CurrentJTI: "b", ExpiresAtMs: now + 1_000_000, CreatedAtMs: now},
	} {
		if err := refresh.CreateRefreshFamily(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := refresh.RevokeRefreshFamilies(ctx, RefreshRevokeFilter{TenantID: f.TenantID, ExceptRole: "super_admin"}, now+5); err != nil || n != 1 {
		t.Fatalf("tenant revoke = %d %v; want 1", n, err)
	}
	if ok, err := refresh.RevokeRefreshFamily(ctx, subject+"-sa", id3, now+6); err != nil || !ok {
		t.Fatalf("single revoke = %v %v", ok, err)
	}
	if ok, _ := refresh.RevokeRefreshFamily(ctx, subject+"-sa", id3, now+7); ok {
		t.Fatal("second revoke reported a change")
	}
	if _, err := refresh.RevokeRefreshFamilies(ctx, RefreshRevokeFilter{}, now); err == nil {
		t.Fatal("empty filter must be refused")
	}
}
