package store

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestContractUsageBudgetStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) { budgetCases(t, b.Budgets, uniq("t-budget-")) })
}

// A cost is kept at the column's precision (DECIMAL(18,6)) on both
// backends: saving the same budget with a cost of more decimals does not
// re-arm the month's warnings, and reads return the stored precision.
func TestContractUsageBudgetStoreCostPrecision(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s := b.Budgets
		ctx := tenantCtx(uniq("t-budget-prec-"))
		cost := 1.23456789
		save := func() {
			t.Helper()
			if err := s.ReplaceUsageBudgets(ctx, []UsageBudget{{UserID: "maya", Cost: &cost, WarnAtPercent: 80, UpdatedBy: "admin", UpdatedAtMs: 1}}); err != nil {
				t.Fatal(err)
			}
		}
		save()
		got, ok, err := s.GetUsageBudget(ctx, "maya")
		if err != nil || !ok || got.Cost == nil || *got.Cost != 1.234568 {
			t.Fatalf("stored cost = %+v %v %v, want 1.234568", got.Cost, ok, err)
		}
		if ok, err := s.ClaimBudgetWarning(ctx, "maya", "2026-10", 80); err != nil || !ok {
			t.Fatalf("first claim = %v %v", ok, err)
		}
		save()
		if ok, err := s.ClaimBudgetWarning(ctx, "maya", "2026-10", 80); err != nil || ok {
			t.Fatalf("claim after an unchanged save = %v %v; the save must not re-arm", ok, err)
		}
		if list := must(s.ListUsageBudgets(tenantCtx(uniq("t-budget-none-"))))(t); list == nil || len(list) != 0 {
			t.Fatalf("empty list = %#v, want []", list)
		}
	})
}

func budgetCases(t *testing.T, s UsageBudgetStore, tnt string) {
	t.Helper()
	ctx := tenantCtx(tnt)
	tokens, cost := int64(1000), 5.0
	if err := s.ReplaceUsageBudgets(ctx, []UsageBudget{
		{UserID: "", Tokens: &tokens, WarnAtPercent: 80, HardStop: true, UpdatedBy: "admin", UpdatedAtMs: 1},
		{UserID: "bob", Cost: &cost, WarnAtPercent: 50, UpdatedBy: "admin", UpdatedAtMs: 1},
		{UserID: "alice", Tokens: &tokens, Cost: &cost, WarnAtPercent: 90, UpdatedBy: "admin", UpdatedAtMs: 1},
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	list, err := s.ListUsageBudgets(ctx)
	if err != nil || len(list) != 3 || list[0].UserID != "" || list[1].UserID != "alice" || list[2].UserID != "bob" {
		t.Fatalf("list = %+v, %v (want tenant, alice, bob)", list, err)
	}
	if !list[0].HardStop || list[0].Tokens == nil || *list[0].Tokens != 1000 || list[0].Cost != nil {
		t.Fatalf("tenant budget = %+v", list[0])
	}
	if b, ok, err := s.GetUsageBudget(ctx, "bob"); err != nil || !ok || b.Cost == nil || *b.Cost != 5 || b.Tokens != nil || b.WarnAtPercent != 50 {
		t.Fatalf("get bob = %+v %v %v", b, ok, err)
	}
	if _, ok, _ := s.GetUsageBudget(tenantCtx(tnt+"-other"), "bob"); ok {
		t.Fatal("budget visible from another tenant")
	}

	// Concurrent evaluators: exactly one wins each (period, level).
	var won atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, err := s.ClaimBudgetWarning(ctx, "bob", "2026-10", 50); err != nil {
				t.Errorf("claim: %v", err)
			} else if ok {
				won.Add(1)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("claims won = %d, want 1", won.Load())
	}
	claim := func(user, period string, level int) bool {
		t.Helper()
		ok, err := s.ClaimBudgetWarning(ctx, user, period, level)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		return ok
	}
	if !claim("bob", "2026-10", 100) || claim("bob", "2026-10", 100) || claim("bob", "2026-10", 50) {
		t.Fatal("the 100% warning must be claimable once, and a lower level after it never")
	}
	if !claim("bob", "2026-11", 50) {
		t.Fatal("a new month re-arms the warnings")
	}
	if claim("nobody", "2026-10", 50) {
		t.Fatal("a warning for an absent budget was claimed")
	}
	// Unchanged limits keep the month's warning state; changed limits re-arm.
	_ = claim("alice", "2026-11", 90)
	cost2 := 10.0
	if err := s.ReplaceUsageBudgets(ctx, []UsageBudget{
		{UserID: "bob", Cost: &cost, WarnAtPercent: 50, UpdatedBy: "admin2", UpdatedAtMs: 2},
		{UserID: "alice", Tokens: &tokens, Cost: &cost2, WarnAtPercent: 90, UpdatedBy: "admin2", UpdatedAtMs: 2},
	}); err != nil {
		t.Fatalf("replace again: %v", err)
	}
	if claim("bob", "2026-11", 50) {
		t.Fatal("unchanged budget lost its warning state")
	}
	if !claim("alice", "2026-11", 90) {
		t.Fatal("changed budget did not re-arm its warnings")
	}
	if list, _ := s.ListUsageBudgets(ctx); len(list) != 2 || list[1].UpdatedBy != "admin2" {
		t.Fatalf("after replace list = %+v (tenant budget must be deleted)", list)
	}
	if err := s.ReplaceUsageBudgets(ctx, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if list, _ := s.ListUsageBudgets(ctx); len(list) != 0 {
		t.Fatalf("after clear list = %+v", list)
	}
}
