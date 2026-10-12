package store

import (
	"context"
	"strconv"
	"testing"
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
	n := 0
	run := func(ctx context.Context, grantID string, cost float64) {
		t.Helper()
		n++
		rid := "rsv-" + strconv.Itoa(n)
		if _, err := g.ReserveRun(ctx, RunReservation{ReservationID: rid, GrantID: grantID, RoomID: "r1", OwnerUserID: "o", BorrowerUserID: "b", AtMs: 1}); err != nil {
			t.Fatalf("reserve %s: %v", grantID, err)
		}
		if err := g.SettleRun(ctx, grantID, rid, RunUsage{Cost: &cost}, 2); err != nil {
			t.Fatalf("settle %s: %v", grantID, err)
		}
	}
	run(ca, "A1", 1.50)
	run(ca, "A1", 1.50)
	run(ca, "A2", 0.25)
	for range 5 {
		run(cb, "B1", 2.00)
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
