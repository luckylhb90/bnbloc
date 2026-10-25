package store

import (
	"context"
	"testing"
)

// TestContractPulseMinutes is the rollup store's contract, run against every
// backend: deltas add, a read sums over its window and tenant, a breakdown keeps
// the dimension, freshness tracks the last write, and retention prunes by age.
func TestContractPulseMinutes(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		pm := b.PulseMinutes
		if pm == nil {
			t.Fatal("the backends must carry the rollup store")
		}
		ctx := context.Background()
		const minute = int64(1761300000000)
		tnt := "pm-contract-" + t.Name()

		if err := pm.AddPulseMinutes(ctx, []PulseMinuteDelta{
			{Tenant: tnt, MinuteMs: minute, Metric: "replies", Dim: "codex", Value: 2},
			{Tenant: tnt, MinuteMs: minute, Metric: "replies", Dim: "codex", Value: 3},
			{Tenant: tnt, MinuteMs: minute, Metric: "replies", Dim: "claude-code", Value: 1},
			{Tenant: tnt + "-other", MinuteMs: minute, Metric: "replies", Dim: "codex", Value: 99},
		}, minute); err != nil {
			t.Fatal(err)
		}

		totals, err := pm.PulseTotals(ctx, tnt, minute, minute+60_000, []string{"replies"})
		if err != nil {
			t.Fatal(err)
		}
		if totals["replies"] != 6 {
			t.Fatalf("totals = %v, want replies 6 (the other tenant excluded)", totals)
		}

		breakdown, err := pm.PulseBreakdown(ctx, tnt, minute, minute+60_000, []string{"replies"})
		if err != nil {
			t.Fatal(err)
		}
		if breakdown["replies"]["codex"] != 5 || breakdown["replies"]["claude-code"] != 1 {
			t.Fatalf("breakdown = %v, want codex 5 and claude-code 1", breakdown)
		}

		at, err := pm.PulseFreshness(ctx, tnt)
		if err != nil {
			t.Fatal(err)
		}
		if at != minute {
			t.Fatalf("freshness = %d, want the write time %d", at, minute)
		}
		if never, _ := pm.PulseFreshness(ctx, tnt+"-never"); never != 0 {
			t.Fatalf("a tenant never written has freshness %d, want 0", never)
		}

		if err := pm.AddPulseMinutes(ctx, nil, minute); err != nil {
			t.Fatalf("an empty batch is a no-op: %v", err)
		}

		// Retention: rows older than the cut-off go; the newer row stays.
		if err := pm.AddPulseMinutes(ctx, []PulseMinuteDelta{
			{Tenant: tnt, MinuteMs: minute + 60_000, Metric: "replies", Dim: "codex", Value: 7},
		}, minute); err != nil {
			t.Fatal(err)
		}
		if _, err := pm.PrunePulseMinutes(ctx, minute+60_000); err != nil {
			t.Fatal(err)
		}
		after, _ := pm.PulseTotals(ctx, tnt, minute, minute+120_000, []string{"replies"})
		if after["replies"] != 7 {
			t.Fatalf("after retention replies = %v, want 7 (only the newer minute)", after["replies"])
		}
	})
}
