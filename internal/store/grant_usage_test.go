package store

import (
	"errors"
	"os"
	"strconv"
	"testing"
	"time"
)

// TenantUsage sums runs_used and cost_used across every grant of a tenant,
// spanning rooms, and never leaks another tenant's usage.
func TestTenantUsageAggregatesAndIsolates(t *testing.T) {
	ca, cb := tenantCtx(tenantA), tenantCtx(tenantB)
	g := NewMemoryGrant()

	// Two grants for tenant A across two different rooms.
	if err := g.PutGrant(ca, Grant{GrantID: "A1", RoomID: "r1", TargetID: "shared-agent:*", OwnerUserID: "alice", GranteeUserID: "*", CreatedAtMs: 1}); err != nil {
		t.Fatalf("put A1: %v", err)
	}
	if err := g.PutGrant(ca, Grant{GrantID: "A2", RoomID: "r2", TargetID: "shared-agent:*", OwnerUserID: "alice", GranteeUserID: "*", CreatedAtMs: 1}); err != nil {
		t.Fatalf("put A2: %v", err)
	}
	// One grant for tenant B that must never contribute to A's totals.
	if err := g.PutGrant(cb, Grant{GrantID: "B1", RoomID: "r1", TargetID: "shared-agent:*", OwnerUserID: "bob", GranteeUserID: "*", CreatedAtMs: 1}); err != nil {
		t.Fatalf("put B1: %v", err)
	}

	// A: 2 runs @1.50 in r1, 1 run @0.25 in r2 -> 3 runs, 3.25 cost.
	if _, err := g.ConsumeRun(ca, "A1", 1.50); err != nil {
		t.Fatalf("consume A1 #1: %v", err)
	}
	if _, err := g.ConsumeRun(ca, "A1", 1.50); err != nil {
		t.Fatalf("consume A1 #2: %v", err)
	}
	if _, err := g.ConsumeRun(ca, "A2", 0.25); err != nil {
		t.Fatalf("consume A2: %v", err)
	}
	for range 5 {
		if _, err := g.ConsumeRun(cb, "B1", 2.00); err != nil {
			t.Fatalf("consume B1: %v", err)
		}
	}

	runs, cost, _ := g.TenantUsage(ca)
	if runs != 3 {
		t.Fatalf("tenant A runs = %d, want 3", runs)
	}
	if cost != 3.25 {
		t.Fatalf("tenant A cost = %v, want 3.25", cost)
	}

	bruns, bcost, _ := g.TenantUsage(cb)
	if bruns != 5 {
		t.Fatalf("tenant B runs = %d, want 5", bruns)
	}
	if bcost != 10.0 {
		t.Fatalf("tenant B cost = %v, want 10.0", bcost)
	}

	// No tenant in context is an error, never a cross-tenant leak.
	if runs, cost, err := g.TenantUsage(bg()); err == nil || runs != 0 || cost != 0 {
		t.Fatalf("usage without tenant = (%d,%v), want (0,0) and an error", runs, cost)
	}
}

// consumeCeilingCases exercises ConsumeRun's ceiling enforcement against any
// GrantStore: RunQuota and CostAllowance are enforced atomically by the store
// (they used to be stored but never checked on consume, so a borrow-record path
// that did not pre-check ran past both). An exhausted ceiling consumes nothing.
func consumeCeilingCases(t *testing.T, g GrantStore, prefix string) {
	t.Helper()
	ctx := tenantCtx(tenantA)
	quota := 2
	allowance := 1.0
	runID, costID := prefix+"-runq", prefix+"-cost"
	if err := g.PutGrant(ctx, Grant{GrantID: runID, RoomID: "r1", TargetID: "shared-agent:x", OwnerUserID: "o", GranteeUserID: "*", RunQuota: &quota, MaxConcurrent: 1, CreatedAtMs: 1}); err != nil {
		t.Fatalf("put run grant: %v", err)
	}
	if err := g.PutGrant(ctx, Grant{GrantID: costID, RoomID: "r1", TargetID: "shared-agent:y", OwnerUserID: "o", GranteeUserID: "*", CostAllowance: &allowance, MaxConcurrent: 1, CreatedAtMs: 1}); err != nil {
		t.Fatalf("put cost grant: %v", err)
	}
	for i := 0; i < quota; i++ {
		if _, err := g.ConsumeRun(ctx, runID, 0); err != nil {
			t.Fatalf("consume %d within quota: %v", i, err)
		}
	}
	if _, err := g.ConsumeRun(ctx, runID, 0); !errors.Is(err, ErrRunQuotaExhausted) {
		t.Fatalf("consume past quota err = %v, want ErrRunQuotaExhausted", err)
	}
	if got, _ := g.GetGrant(ctx, runID); got.RunsUsed != quota {
		t.Fatalf("runsUsed = %d, want %d (an exhausted consume must not count)", got.RunsUsed, quota)
	}
	if _, err := g.ConsumeRun(ctx, costID, 0.75); err != nil {
		t.Fatalf("consume within allowance: %v", err)
	}
	if _, err := g.ConsumeRun(ctx, costID, 0.5); !errors.Is(err, ErrCostAllowanceExhausted) {
		t.Fatalf("consume past allowance err = %v, want ErrCostAllowanceExhausted", err)
	}
	if got, _ := g.GetGrant(ctx, costID); got.CostUsed != 0.75 || got.RunsUsed != 1 {
		t.Fatalf("cost grant = runs %d cost %v, want 1 / 0.75", got.RunsUsed, got.CostUsed)
	}
	if _, err := g.ConsumeRun(ctx, prefix+"-missing", 0); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("consume missing err = %v, want ErrGrantNotFound", err)
	}
}

func TestMemoryConsumeRunEnforcesCeilings(t *testing.T) {
	consumeCeilingCases(t, NewMemoryGrant(), "mem")
}

func TestMySQLConsumeRunEnforcesCeilings(t *testing.T) {
	dsn := os.Getenv("BNBLOC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set BNBLOC_TEST_MYSQL_DSN to run the MySQL integration test")
	}
	db, err := OpenMySQL(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	consumeCeilingCases(t, NewMySQLGrant(db), "it"+strconv.FormatInt(time.Now().UnixNano(), 36))
}
