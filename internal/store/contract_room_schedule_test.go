package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// RoomScheduleStore contract. The scheduler methods are cross-tenant and the
// package's tests share one database, so the claim cases look only at rows
// of their own tenant.

func sampleSchedule(id, room, by string, next int64) wire.RoomSchedule {
	return wire.RoomSchedule{
		ScheduleID: id, RoomID: room, CreatedBy: by, Text: "standup?",
		Recipient:  wire.ScheduleRecipient{Kind: wire.ScheduleRecipientAgent, OwnerID: "jack", Provider: "codex", Core: "c1"},
		Recurrence: wire.ScheduleRecurrence{Kind: wire.RecurWeekly, Time: "09:00", Days: []int{1, 3}},
		Timezone:   "Europe/Berlin", Enabled: true, NextRunAtMs: next, CreatedAtMs: 1, UpdatedAtMs: 1,
	}
}

// claimOwn claims due schedules and keeps the rows of tid.
func claimOwn(t *testing.T, s RoomScheduleStore, tid string, nowMs, leaseUntilMs int64, limit int) (string, []wire.RoomSchedule) {
	t.Helper()
	token, rows, err := s.ClaimDueSchedules(context.Background(), nowMs, leaseUntilMs, limit)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	own := []wire.RoomSchedule{}
	for _, r := range rows {
		if r.TenantID == tid {
			own = append(own, r)
		}
	}
	return token, own
}

func TestContractRoomScheduleStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s := b.Schedules
		tid := uniq("t-sched-")
		ctx, other := tenantCtx(tid), tenantCtx(uniq("t-sched-other-"))
		room := uniq("room-")
		ida, idb := uniq("sch-a-"), uniq("sch-b-")
		if _, err := s.CreateSchedule(ctx, sampleSchedule(ida, room, "maya", 500)); err != nil {
			t.Fatal(err)
		}
		sb := sampleSchedule(idb, room, "jack", 0)
		sb.CreatedAtMs = 2
		sb.Recipient = wire.ScheduleRecipient{Kind: wire.ScheduleRecipientRoom}
		if _, err := s.CreateSchedule(ctx, sb); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateSchedule(ctx, sampleSchedule(ida, room, "maya", 1)); !errors.Is(err, ErrScheduleExists) {
			t.Fatalf("re-create = %v, want ErrScheduleExists", err)
		}
		if _, err := s.CreateSchedule(other, sampleSchedule(ida, room, "mallory", 1)); !errors.Is(err, ErrScheduleExists) {
			t.Fatalf("re-create from another tenant = %v, want ErrScheduleExists", err)
		}
		got, err := s.GetSchedule(ctx, room, ida)
		if err != nil || got.TenantID != tid || got.Recipient.Provider != "codex" || len(got.Recurrence.Days) != 2 || got.NextRunAtMs != 500 || !got.Enabled || got.CreatedBy != "maya" {
			t.Fatalf("round trip = %+v, %v", got, err)
		}
		if _, err := s.GetSchedule(other, room, ida); !errors.Is(err, ErrScheduleNotFound) {
			t.Fatalf("cross-tenant get = %v", err)
		}
		if _, err := s.GetSchedule(ctx, uniq("room-"), ida); !errors.Is(err, ErrScheduleNotFound) {
			t.Fatalf("cross-room get = %v", err)
		}
		var order []string
		for _, sc := range must(s.ListSchedules(ctx, room))(t) {
			order = append(order, sc.ScheduleID)
		}
		if !slices.Equal(order, []string{ida, idb}) {
			t.Fatalf("list = %v, want oldest first", order)
		}
		if l := must(s.ListSchedules(other, room))(t); l == nil || len(l) != 0 {
			t.Fatalf("cross-tenant list = %#v, want []", l)
		}
		if roomN, mine, err := s.CountSchedules(ctx, room, "maya"); err != nil || roomN != 2 || mine != 1 {
			t.Fatalf("count = %d/%d, %v", roomN, mine, err)
		}
		if _, mine, _ := s.CountSchedules(ctx, room, "Maya"); mine != 0 {
			t.Fatalf("a case variant of the creator owns %d", mine)
		}
		// Update rewrites the mutable fields (an error is stored truncated).
		got.Text, got.Enabled, got.NextRunAtMs, got.UpdatedAtMs, got.LastError = "retro?", false, 0, 9, strings.Repeat("é", 400)
		if err := s.UpdateSchedule(ctx, got); err != nil {
			t.Fatal(err)
		}
		again, _ := s.GetSchedule(ctx, room, ida)
		if again.Text != "retro?" || again.Enabled || again.NextRunAtMs != 0 || again.UpdatedAtMs != 9 || again.LastError != truncateRunError(got.LastError) {
			t.Fatalf("after update = %+v", again)
		}
		if err := s.UpdateSchedule(other, got); !errors.Is(err, ErrScheduleNotFound) {
			t.Fatalf("cross-tenant update = %v", err)
		}
		// A manual run records only the last-run fields.
		if err := s.CompleteScheduleRun(ctx, ScheduleRunResult{ScheduleID: ida, AtMs: 77, Status: wire.ScheduleRunOK, Seq: 12}); err != nil {
			t.Fatal(err)
		}
		again, _ = s.GetSchedule(ctx, room, ida)
		if again.LastRunAtMs != 77 || again.LastSeq != 12 || again.RunCount != 1 || again.NextRunAtMs != 0 {
			t.Fatalf("after manual run = %+v", again)
		}
		if err := s.CompleteScheduleRun(ctx, ScheduleRunResult{ScheduleID: uniq("nope-"), AtMs: 1, Status: wire.ScheduleRunOK}); err != nil {
			t.Fatalf("completing a deleted schedule = %v, want nil", err)
		}
		if err := s.DeleteSchedule(other, room, idb); !errors.Is(err, ErrScheduleNotFound) {
			t.Fatalf("cross-tenant delete = %v", err)
		}
		if err := s.DeleteSchedule(ctx, room, idb); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteSchedule(ctx, room, idb); !errors.Is(err, ErrScheduleNotFound) {
			t.Fatalf("second delete = %v", err)
		}
	})
}

// N concurrent claimers over M due schedules: every schedule is claimed
// exactly once; a live claim blocks a re-claim until its lease ends (at
// now == lease end it has ended); a completion advances the next run and
// releases the claim; an edit during the run wins over the completion.
func TestContractRoomScheduleStoreClaims(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s := b.Schedules
		tid := uniq("t-claim-")
		ctx := tenantCtx(tid)
		room := uniq("room-")
		id := func(i int) string { return fmt.Sprintf("%s-%02d", tid, i) }
		const n = 24
		for i := 0; i < n; i++ {
			if _, err := s.CreateSchedule(ctx, sampleSchedule(id(i), room, "maya", int64(100+i))); err != nil {
				t.Fatal(err)
			}
		}
		// A not-yet-due and a paused schedule are never claimed.
		if _, err := s.CreateSchedule(ctx, sampleSchedule(tid+"-future", room, "maya", 900_000)); err != nil {
			t.Fatal(err)
		}
		paused := sampleSchedule(tid+"-paused", room, "maya", 50)
		paused.Enabled = false
		if _, err := s.CreateSchedule(ctx, paused); err != nil {
			t.Fatal(err)
		}

		var mu sync.Mutex
		seen := map[string]string{}
		var wg sync.WaitGroup
		for c := 0; c < 8; c++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for round := 0; round < 4; round++ {
					token, rows, err := s.ClaimDueSchedules(context.Background(), 1000, 5000, 5)
					if err != nil {
						t.Errorf("claim: %v", err)
						return
					}
					mu.Lock()
					for _, r := range rows {
						if r.TenantID != tid {
							continue
						}
						if prev, dup := seen[r.ScheduleID]; dup {
							t.Errorf("schedule %s claimed by %s and %s", r.ScheduleID, prev, token)
						}
						seen[r.ScheduleID] = token
					}
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if len(seen) != n {
			t.Fatalf("claimed %d schedules, want %d", len(seen), n)
		}
		// Live claims block a re-claim until the lease ends.
		if _, rows := claimOwn(t, s, tid, 4999, 9000, MaxScheduleClaim); len(rows) != 0 {
			t.Fatalf("re-claimed %d live claims", len(rows))
		}
		first := id(0)
		firstTok := seen[first]
		fresh, rows := claimOwn(t, s, tid, 5000, 9000, MaxScheduleClaim)
		if len(rows) != n {
			t.Fatalf("at the lease end re-claimed %d, want %d", len(rows), n)
		}
		for i, r := range rows {
			if r.ScheduleID != id(i) {
				t.Fatalf("claim order = %s at %d, want earliest due first", r.ScheduleID, i)
			}
		}
		// The stale claimer's completion only records the run; the fresh claim
		// still advances it.
		if err := s.CompleteScheduleRun(ctx, ScheduleRunResult{ScheduleID: first, ClaimToken: firstTok, AtMs: 6100, Status: wire.ScheduleRunOK, Seq: 3, NextRunAtMs: 999_999}); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetSchedule(ctx, room, first)
		if got.NextRunAtMs != 100 || got.LastSeq != 3 {
			t.Fatalf("stale completion moved the next run: %+v", got)
		}
		if err := s.CompleteScheduleRun(ctx, ScheduleRunResult{ScheduleID: first, ClaimToken: fresh, AtMs: 6200, Status: wire.ScheduleRunOK, Seq: 3, NextRunAtMs: 700_000}); err != nil {
			t.Fatal(err)
		}
		got, _ = s.GetSchedule(ctx, room, first)
		if got.NextRunAtMs != 700_000 || !got.Enabled || got.RunCount != 2 {
			t.Fatalf("completion = %+v", got)
		}
		// An auto-pause completion disables the schedule.
		second := id(1)
		if err := s.CompleteScheduleRun(ctx, ScheduleRunResult{ScheduleID: second, ClaimToken: fresh, AtMs: 6300, Status: wire.ScheduleRunSkipped, Error: "creator left", Disable: true}); err != nil {
			t.Fatal(err)
		}
		got, _ = s.GetSchedule(ctx, room, second)
		if got.Enabled || got.NextRunAtMs != 0 || got.LastError != "creator left" || got.RunCount != 0 {
			t.Fatalf("auto-pause = %+v", got)
		}
		// An edit during a claimed run wins: the completion cannot overwrite it.
		third := id(2)
		edit, _ := s.GetSchedule(ctx, room, third)
		edit.NextRunAtMs, edit.UpdatedAtMs = 800_000, 7000
		if err := s.UpdateSchedule(ctx, edit); err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteScheduleRun(ctx, ScheduleRunResult{ScheduleID: third, ClaimToken: fresh, AtMs: 7100, Status: wire.ScheduleRunOK, NextRunAtMs: 123}); err != nil {
			t.Fatal(err)
		}
		got, _ = s.GetSchedule(ctx, room, third)
		if got.NextRunAtMs != 800_000 {
			t.Fatalf("completion overwrote an edit: %+v", got)
		}
	})
}

// One claim call takes at most MaxScheduleClaim rows (also its default).
func TestContractRoomScheduleStoreClaimLimit(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s := b.Schedules
		tid := uniq("t-climit-")
		ctx := tenantCtx(tid)
		room := uniq("room-")
		// Due at 1 and claimed at 1: no other case's row is due that early,
		// and the rows go when the case ends (a later claim would take them
		// first).
		const at = 1
		var ids []string
		t.Cleanup(func() {
			for _, id := range ids {
				_ = s.DeleteSchedule(ctx, room, id)
			}
		})
		for i := 0; i < MaxScheduleClaim+5; i++ {
			id := uniq("sch-")
			ids = append(ids, id)
			if _, err := s.CreateSchedule(ctx, sampleSchedule(id, room, "maya", at)); err != nil {
				t.Fatal(err)
			}
		}
		for _, c := range []struct{ limit, want int }{{3, 3}, {0, MaxScheduleClaim}, {MaxScheduleClaim * 4, MaxScheduleClaim + 5 - 3 - MaxScheduleClaim}} {
			_, rows := claimOwn(t, s, tid, at, at+10, c.limit)
			if len(rows) != c.want {
				t.Fatalf("claim(limit %d) = %d rows, want %d", c.limit, len(rows), c.want)
			}
		}
	})
}
