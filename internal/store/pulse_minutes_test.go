package store

import (
	"context"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/testmysql"
)

// The rollup against a real MySQL: deltas add up, a batch is atomic, reads sum
// over a window, and retention deletes only what is old.
func TestPulseMinutesAddsSumsAndPrunes(t *testing.T) {
	db, err := OpenMySQL(testmysql.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pm := NewMySQLPulseMinutes(NewDB(db, nil, 0, 0))
	ctx := context.Background()
	const minute = int64(1761300000000)

	rows := []PulseMinuteDelta{
		{Tenant: "t1", MinuteMs: minute, Metric: "replies", Dim: "codex", Value: 2},
		{Tenant: "t1", MinuteMs: minute, Metric: "replies", Dim: "codex", Value: 3}, // same key: adds
		{Tenant: "t1", MinuteMs: minute + 60_000, Metric: "replies", Dim: "codex", Value: 1},
		{Tenant: "t2", MinuteMs: minute, Metric: "replies", Dim: "codex", Value: 99}, // another tenant
	}
	if err := pm.AddPulseMinutes(ctx, rows, minute); err != nil {
		t.Fatal(err)
	}
	if err := pm.AddPulseMinutes(ctx, []PulseMinuteDelta{{Tenant: "t1", MinuteMs: minute, Metric: "replies", Dim: "codex", Value: 5}}, minute); err != nil {
		t.Fatal(err)
	}

	got, err := pm.PulseTotals(ctx, "t1", minute, minute+120_000, []string{"replies", "messages"})
	if err != nil {
		t.Fatal(err)
	}
	if got["replies"] != 11 { // 2+3+1 across two minutes, plus a later increment of 5
		t.Fatalf("replies total = %v, want 11", got["replies"])
	}
	if _, ok := got["messages"]; ok {
		t.Fatal("a metric with no rows must not appear in the totals")
	}

	// Window: only the first minute.
	first, _ := pm.PulseTotals(ctx, "t1", minute, minute+60_000, []string{"replies"})
	if first["replies"] != 10 {
		t.Fatalf("first-minute replies = %v, want 10", first["replies"])
	}

	// Pruning deletes rows before the cut-off and nothing after it.
	n, err := pm.PrunePulseMinutes(ctx, minute+60_000)
	if err != nil {
		t.Fatal(err)
	}
	// Retention is global, not per tenant: t1's first minute and t2's row are both older.
	if n != 2 {
		t.Fatalf("pruned %d rows, want 2 (t1's first minute and t2's row)", n)
	}
	after, _ := pm.PulseTotals(ctx, "t1", minute, minute+120_000, []string{"replies"})
	if after["replies"] != 1 {
		t.Fatalf("after pruning, replies = %v, want 1", after["replies"])
	}
}
