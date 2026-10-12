package store

import (
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// reservationCases exercises reserve-then-settle (R8) against any GrantStore.
func reservationCases(t *testing.T, g GrantStore, prefix string) {
	t.Helper()
	ctx := tenantCtx(tenantA)
	future := int64(4_000_000_000_000)
	put := func(id string, quota *int, allowance *float64) {
		t.Helper()
		if err := g.PutGrant(ctx, Grant{GrantID: id, RoomID: "r1", TargetID: "shared-agent:x", OwnerUserID: "alice", GranteeUserID: "*", RunQuota: quota, CostAllowance: allowance, MaxConcurrent: 2, ExpiresAtMs: &future, CreatedAtMs: 1}); err != nil {
			t.Fatalf("put %s: %v", id, err)
		}
	}
	reserve := func(grantID, rid string, at int64) error {
		_, err := g.ReserveRun(ctx, RunReservation{ReservationID: rid, GrantID: grantID, RoomID: "r1", OwnerUserID: "alice", BorrowerUserID: "bob", Model: "m-req", AtMs: at})
		return err
	}

	t.Run("concurrent reservations never exceed the quota", func(t *testing.T) {
		quota := 5
		id := prefix + "-conc"
		put(id, &quota, nil)
		var admitted, exhausted atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < 40; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				err := reserve(id, id+"-r"+strconv.Itoa(i), 1000)
				switch {
				case err == nil:
					admitted.Add(1)
				case errors.Is(err, ErrRunQuotaExhausted):
					exhausted.Add(1)
				default:
					t.Errorf("reserve %d: %v", i, err)
				}
			}(i)
		}
		wg.Wait()
		if admitted.Load() != int32(quota) || exhausted.Load() != int32(40-quota) {
			t.Fatalf("admitted=%d exhausted=%d, want %d/%d", admitted.Load(), exhausted.Load(), quota, 40-quota)
		}
		if got, _ := g.GetGrant(ctx, id); got.RunsUsed != quota {
			t.Fatalf("runsUsed = %d, want %d", got.RunsUsed, quota)
		}
		turns := g.BorrowTurns(ctx, BorrowTurnQuery{FromMs: 0})
		n := 0
		for _, r := range turns {
			if r.GrantID == id {
				n++
			}
		}
		if n != quota {
			t.Fatalf("recorded borrow turns = %d, want %d (one per admitted reservation)", n, quota)
		}
		// An approved quota request raises the window; usage is kept.
		if _, err := g.RaiseRunQuota(ctx, id, 1, 1000); err != nil {
			t.Fatalf("raise: %v", err)
		}
		// A quota raise is for a live grant only: at its expiry the grant
		// takes no more runs (Grant.Live).
		if _, err := g.RaiseRunQuota(ctx, id, 1, future); !errors.Is(err, ErrGrantNotFound) {
			t.Fatalf("raise at expiry err = %v, want ErrGrantNotFound", err)
		}
		if err := reserve(id, id+"-after-raise", 1000); err != nil {
			t.Fatalf("reserve after raise: %v", err)
		}
		if err := reserve(id, id+"-after-raise-2", 1000); !errors.Is(err, ErrRunQuotaExhausted) {
			t.Fatalf("reserve past raised quota err = %v, want ErrRunQuotaExhausted", err)
		}
	})

	t.Run("settle attaches usage once", func(t *testing.T) {
		allowance := 1.0
		id := prefix + "-settle"
		put(id, nil, &allowance)
		rid := id + "-r1"
		if err := reserve(id, rid, 1000); err != nil {
			t.Fatalf("reserve: %v", err)
		}
		cost, in, out := 0.6, int64(120), int64(30)
		if err := g.SettleRun(ctx, id, rid, RunUsage{Model: "m-reported", Cost: &cost, InputTokens: &in, OutputTokens: &out}, 2000); err != nil {
			t.Fatalf("settle: %v", err)
		}
		if err := g.SettleRun(ctx, id, rid, RunUsage{Cost: &cost}, 2001); !errors.Is(err, ErrReservationSettled) {
			t.Fatalf("second settle err = %v, want ErrReservationSettled", err)
		}
		if err := g.SettleRun(ctx, id, id+"-nope", RunUsage{}, 2001); !errors.Is(err, ErrReservationNotFound) {
			t.Fatalf("unknown settle err = %v, want ErrReservationNotFound", err)
		}
		if err := g.SettleRun(ctx, prefix+"-other-grant", rid, RunUsage{}, 2001); !errors.Is(err, ErrReservationNotFound) {
			t.Fatalf("settle under another grant err = %v, want ErrReservationNotFound", err)
		}
		got, _ := g.GetGrant(ctx, id)
		if got.RunsUsed != 1 || got.CostUsed != 0.6 {
			t.Fatalf("grant runs=%d cost=%v, want 1 / 0.6", got.RunsUsed, got.CostUsed)
		}
		var rec *AuditRecord
		for _, r := range g.BorrowTurns(ctx, BorrowTurnQuery{FromMs: 0}) {
			if r.GrantID == id {
				r := r
				rec = &r
			}
		}
		if rec == nil || rec.Model != "m-reported" || rec.Cost == nil || *rec.Cost != 0.6 ||
			rec.InputTokens == nil || *rec.InputTokens != 120 || rec.OutputTokens == nil || *rec.OutputTokens != 30 {
			t.Fatalf("settled record = %+v", rec)
		}
		// Cost is checked at reserve against the cost settled so far: 0.6 < 1.0
		// admits one more run, whose settle may carry the grant past the
		// allowance; after that nothing is admitted.
		if err := reserve(id, id+"-r2", 3000); err != nil {
			t.Fatalf("reserve with allowance left: %v", err)
		}
		if err := g.SettleRun(ctx, id, id+"-r2", RunUsage{Cost: &cost}, 3001); err != nil {
			t.Fatalf("settle r2: %v", err)
		}
		if err := reserve(id, id+"-r3", 4000); !errors.Is(err, ErrCostAllowanceExhausted) {
			t.Fatalf("reserve past allowance err = %v, want ErrCostAllowanceExhausted", err)
		}
	})

	t.Run("an unsettled reservation expires and still counts", func(t *testing.T) {
		quota := 1
		id := prefix + "-expire"
		put(id, &quota, nil)
		rid := id + "-r1"
		if err := reserve(id, rid, 1000); err != nil {
			t.Fatalf("reserve: %v", err)
		}
		if err := g.SettleRun(ctx, id, rid, RunUsage{}, 1000+RunReservationTTLms); !errors.Is(err, ErrReservationExpired) {
			t.Fatalf("late settle err = %v, want ErrReservationExpired", err)
		}
		if got, _ := g.GetGrant(ctx, id); got.RunsUsed != 1 {
			t.Fatalf("runsUsed = %d, want 1 (an expired reservation is a run)", got.RunsUsed)
		}
		if err := reserve(id, id+"-r2", 1000+RunReservationTTLms); !errors.Is(err, ErrRunQuotaExhausted) {
			t.Fatalf("reserve after expiry err = %v, want ErrRunQuotaExhausted", err)
		}
	})

	t.Run("a revoked or expired grant reserves nothing", func(t *testing.T) {
		id := prefix + "-revoked"
		put(id, nil, nil)
		if err := g.RevokeGrant(ctx, id, 500); err != nil {
			t.Fatalf("revoke: %v", err)
		}
		if err := reserve(id, id+"-r1", 1000); !errors.Is(err, ErrGrantNotFound) {
			t.Fatalf("reserve on revoked err = %v, want ErrGrantNotFound", err)
		}
		if err := reserve(prefix+"-absent", prefix+"-absent-r1", 1000); !errors.Is(err, ErrGrantNotFound) {
			t.Fatalf("reserve on absent err = %v, want ErrGrantNotFound", err)
		}
		if err := reserve(prefix+"-settle", prefix+"-late", future); !errors.Is(err, ErrGrantNotFound) {
			t.Fatalf("reserve at expiry err = %v, want ErrGrantNotFound", err)
		}
		if got, _ := g.GetGrant(ctx, id); got.RunsUsed != 0 {
			t.Fatalf("revoked grant runsUsed = %d, want 0", got.RunsUsed)
		}
		// A run reserved before a revocation still settles: it ran.
		live := prefix + "-revoke-after"
		put(live, nil, nil)
		if err := reserve(live, live+"-r1", 1000); err != nil {
			t.Fatalf("reserve: %v", err)
		}
		_ = g.RevokeGrant(ctx, live, 1500)
		if err := g.SettleRun(ctx, live, live+"-r1", RunUsage{}, 2000); err != nil {
			t.Fatalf("settle after revoke: %v", err)
		}
	})
}

func TestMemoryRunReservations(t *testing.T) {
	reservationCases(t, NewMemoryGrant(), "mem")
}

func TestMySQLRunReservations(t *testing.T) {
	dsn := mysqlTestDSN(t)
	db, err := OpenMySQL(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	reservationCases(t, NewMySQLGrant(db), "it"+strconv.FormatInt(time.Now().UnixNano(), 36))
}
