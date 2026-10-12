package store

import (
	"context"
	"testing"
)

// TenantUsage sums runs_used and cost_used across every owner-issued grant
// of a tenant, spanning rooms, and never leaks another tenant's usage.
func TestContractGrantStoreTenantUsage(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		g := b.Grant
		ca, cb := tenantCtx(uniq("t-usage-a-")), tenantCtx(uniq("t-usage-b-"))
		put := func(ctx context.Context, id, room string) {
			t.Helper()
			if err := g.PutGrant(ctx, Grant{GrantID: id, RoomID: room, TargetID: "shared-agent:*", OwnerUserID: "maya", GranteeUserID: "*", CreatedAtMs: 1}); err != nil {
				t.Fatalf("put %s: %v", id, err)
			}
		}
		a1, a2, b1 := uniq("g-a1-"), uniq("g-a2-"), uniq("g-b1-")
		put(ca, a1, "r1")
		put(ca, a2, "r2")
		put(cb, b1, "r1")
		run := func(ctx context.Context, grant string, cost float64) {
			t.Helper()
			rid := uniq("rsv-")
			if _, err := g.ReserveRun(ctx, RunReservation{ReservationID: rid, GrantID: grant, RoomID: "r1", OwnerUserID: "maya", BorrowerUserID: "jack", AtMs: 10}); err != nil {
				t.Fatalf("reserve on %s: %v", grant, err)
			}
			if err := g.SettleRun(ctx, grant, rid, RunUsage{Cost: &cost}, 20); err != nil {
				t.Fatalf("settle on %s: %v", grant, err)
			}
		}
		// A: 2 runs @1.50 in r1, 1 run @0.25 in r2 -> 3 runs, 3.25 cost.
		run(ca, a1, 1.5)
		run(ca, a1, 1.5)
		run(ca, a2, 0.25)
		for range 5 {
			run(cb, b1, 2)
		}
		if runs, cost, err := g.TenantUsage(ca); err != nil || runs != 3 || cost != 3.25 {
			t.Fatalf("tenant A usage = %d, %v, %v; want 3, 3.25", runs, cost, err)
		}
		if runs, cost, err := g.TenantUsage(cb); err != nil || runs != 5 || cost != 10 {
			t.Fatalf("tenant B usage = %d, %v, %v; want 5, 10", runs, cost, err)
		}
		// No tenant in context is an error, never a cross-tenant leak.
		if runs, cost, err := g.TenantUsage(bg()); err == nil || runs != 0 || cost != 0 {
			t.Fatalf("usage without tenant = (%d,%v), want (0,0) and an error", runs, cost)
		}
	})
}
