package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func TestMemoryRoomScheduleStore(t *testing.T) {
	testRoomScheduleStore(t, NewMemoryRoomSchedules(), "sched-mem")
}

func TestMemoryRoomScheduleClaimRace(t *testing.T) {
	testScheduleClaimRace(t, NewMemoryRoomSchedules(), "sched-mem-race")
}

// The MySQL variants run the same contract (gated on BNBLOC_TEST_MYSQL_DSN).
// Claims are cross-tenant, so the tests use a clock far in the past (no live
// row is due at nowMs <= 1e6) and remove their own tenants' rows.
func mysqlScheduleBackends(t *testing.T, prefix string) (*Backends, string) {
	t.Helper()
	dsn := mysqlTestDSN(t)
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	tid := prefix + itoaBase(time.Now().UnixNano())
	_, _ = b.DB.Exec(`DELETE FROM room_schedules WHERE tenant_id LIKE ?`, prefix+"%")
	t.Cleanup(func() {
		_, _ = b.DB.Exec(`DELETE FROM room_schedules WHERE tenant_id LIKE ?`, tid+"%")
		_ = b.DB.Close()
	})
	return b, tid
}

func TestMySQLRoomScheduleStore(t *testing.T) {
	b, tid := mysqlScheduleBackends(t, "sched-it-")
	testRoomScheduleStore(t, b.Schedules, tid)
}

func TestMySQLRoomScheduleClaimRace(t *testing.T) {
	b, tid := mysqlScheduleBackends(t, "sched-race-")
	testScheduleClaimRace(t, b.Schedules, tid)
}

func sampleSchedule(id, room, by string, next int64) wire.RoomSchedule {
	return wire.RoomSchedule{
		ScheduleID: id, RoomID: room, CreatedBy: by, Text: "standup?",
		Recipient:  wire.ScheduleRecipient{Kind: wire.ScheduleRecipientAgent, OwnerID: "bob", Provider: "codex", Core: "c1"},
		Recurrence: wire.ScheduleRecurrence{Kind: wire.RecurWeekly, Time: "09:00", Days: []int{1, 3}},
		Timezone:   "Europe/Berlin", Enabled: true, NextRunAtMs: next, CreatedAtMs: 1, UpdatedAtMs: 1,
	}
}

func testRoomScheduleStore(t *testing.T, s RoomScheduleStore, tid string) {
	ctx := tenant.NewContext(context.Background(), tid)
	other := tenant.NewContext(context.Background(), tid+"-other")
	id := func(n string) string { return tid + "-" + n }
	if _, err := s.CreateSchedule(ctx, sampleSchedule(id("a"), "r1", "alice", 500)); err != nil {
		t.Fatal(err)
	}
	b := sampleSchedule(id("b"), "r1", "bob", 0)
	b.CreatedAtMs = 2
	b.Recipient = wire.ScheduleRecipient{Kind: wire.ScheduleRecipientRoom}
	if _, err := s.CreateSchedule(ctx, b); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSchedule(ctx, "r1", id("a"))
	if err != nil {
		t.Fatal(err)
	}
	if got.TenantID != tid || got.Recipient.Provider != "codex" || len(got.Recurrence.Days) != 2 || got.NextRunAtMs != 500 || !got.Enabled {
		t.Fatalf("round trip = %+v", got)
	}
	// Tenant + room scope.
	if _, err := s.GetSchedule(other, "r1", id("a")); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("cross-tenant get = %v, want not found", err)
	}
	if _, err := s.GetSchedule(ctx, "r2", id("a")); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("cross-room get = %v, want not found", err)
	}
	list, err := s.ListSchedules(ctx, "r1")
	if err != nil || len(list) != 2 || list[0].ScheduleID != id("a") {
		t.Fatalf("list = %+v, %v", list, err)
	}
	room, mine, err := s.CountSchedules(ctx, "r1", "alice")
	if err != nil || room != 2 || mine != 1 {
		t.Fatalf("count = %d/%d, %v", room, mine, err)
	}
	// Update rewrites mutable fields.
	got.Text, got.Enabled, got.NextRunAtMs, got.UpdatedAtMs = "retro?", false, 0, 9
	if err := s.UpdateSchedule(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, _ := s.GetSchedule(ctx, "r1", id("a"))
	if again.Text != "retro?" || again.Enabled || again.NextRunAtMs != 0 || again.UpdatedAtMs != 9 {
		t.Fatalf("after update = %+v", again)
	}
	if err := s.UpdateSchedule(other, got); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("cross-tenant update = %v", err)
	}
	// A manual run records only the last-run fields.
	if err := s.CompleteScheduleRun(ctx, ScheduleRunResult{ScheduleID: id("a"), AtMs: 77, Status: wire.ScheduleRunOK, Seq: 12}); err != nil {
		t.Fatal(err)
	}
	again, _ = s.GetSchedule(ctx, "r1", id("a"))
	if again.LastRunAtMs != 77 || again.LastSeq != 12 || again.RunCount != 1 || again.NextRunAtMs != 0 {
		t.Fatalf("after manual run = %+v", again)
	}
	if err := s.DeleteSchedule(ctx, "r1", id("b")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSchedule(ctx, "r1", id("b")); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("second delete = %v", err)
	}
}

// testScheduleClaimRace: N concurrent claimers over M due schedules — every
// schedule is claimed exactly once; a live claim blocks re-claim until its
// lease expires; completing advances next run and releases the claim; an
// edit during the run wins over the completion.
func testScheduleClaimRace(t *testing.T, s RoomScheduleStore, tid string) {
	ctx := tenant.NewContext(context.Background(), tid)
	const n = 24
	for i := 0; i < n; i++ {
		if _, err := s.CreateSchedule(ctx, sampleSchedule(fmt.Sprintf("%s-%02d", tid, i), "race", "alice", int64(100+i))); err != nil {
			t.Fatal(err)
		}
	}
	// A not-yet-due and a paused schedule are never claimed.
	if _, err := s.CreateSchedule(ctx, sampleSchedule(tid+"-future", "race", "alice", 900_000)); err != nil {
		t.Fatal(err)
	}
	paused := sampleSchedule(tid+"-paused", "race", "alice", 50)
	paused.Enabled = false
	if _, err := s.CreateSchedule(ctx, paused); err != nil {
		t.Fatal(err)
	}

	const claimers = 8
	var mu sync.Mutex
	seen := map[string]string{}
	claimTokens := map[string]bool{}
	var wg sync.WaitGroup
	for c := 0; c < claimers; c++ {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			for round := 0; round < 4; round++ {
				token, rows, err := s.ClaimDueSchedules(context.Background(), 1000, 5000, 5)
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				mu.Lock()
				if len(rows) > 0 && claimTokens[token] {
					t.Errorf("claim token %s reused", token)
				}
				claimTokens[token] = true
				for _, r := range rows {
					if prev, dup := seen[r.ScheduleID]; dup {
						t.Errorf("schedule %s claimed by %s and %s", r.ScheduleID, prev, token)
					}
					seen[r.ScheduleID] = token
				}
				mu.Unlock()
			}
		}(c)
	}
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("claimed %d schedules, want %d", len(seen), n)
	}
	// Live claims block a re-claim; an expired lease is re-claimable.
	if _, rows, _ := s.ClaimDueSchedules(context.Background(), 1000, 5000, 100); len(rows) != 0 {
		t.Fatalf("re-claimed %d live claims", len(rows))
	}
	first := fmt.Sprintf("%s-%02d", tid, 0)
	firstTok := seen[first]
	fresh, rows, _ := s.ClaimDueSchedules(context.Background(), 6000, 9000, 100)
	if len(rows) != n {
		t.Fatalf("after lease expiry re-claimed %d, want %d", len(rows), n)
	}
	// The stale claimer's completion only records the run; the fresh claim
	// still advances it.
	if err := s.CompleteScheduleRun(ctx, ScheduleRunResult{ScheduleID: first, ClaimToken: firstTok, AtMs: 6100, Status: wire.ScheduleRunOK, Seq: 3, NextRunAtMs: 999_999}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetSchedule(ctx, "race", first)
	if got.NextRunAtMs != 100 || got.LastSeq != 3 {
		t.Fatalf("stale completion moved next run: %+v", got)
	}
	if err := s.CompleteScheduleRun(ctx, ScheduleRunResult{ScheduleID: first, ClaimToken: fresh, AtMs: 6200, Status: wire.ScheduleRunOK, Seq: 3, NextRunAtMs: 700_000}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSchedule(ctx, "race", first)
	if got.NextRunAtMs != 700_000 || !got.Enabled || got.RunCount != 2 {
		t.Fatalf("completion = %+v", got)
	}
	// An auto-pause completion disables the schedule.
	second := fmt.Sprintf("%s-%02d", tid, 1)
	if err := s.CompleteScheduleRun(ctx, ScheduleRunResult{ScheduleID: second, ClaimToken: fresh, AtMs: 6300, Status: wire.ScheduleRunSkipped, Error: "creator left", Disable: true}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSchedule(ctx, "race", second)
	if got.Enabled || got.NextRunAtMs != 0 || got.LastError != "creator left" || got.RunCount != 0 {
		t.Fatalf("auto-pause = %+v", got)
	}
	// An edit during a claimed run wins: the completion cannot overwrite it.
	third := fmt.Sprintf("%s-%02d", tid, 2)
	edit, _ := s.GetSchedule(ctx, "race", third)
	edit.NextRunAtMs, edit.UpdatedAtMs = 800_000, 7000
	if err := s.UpdateSchedule(ctx, edit); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteScheduleRun(ctx, ScheduleRunResult{ScheduleID: third, ClaimToken: fresh, AtMs: 7100, Status: wire.ScheduleRunOK, NextRunAtMs: 123}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSchedule(ctx, "race", third)
	if got.NextRunAtMs != 800_000 {
		t.Fatalf("completion overwrote an edit: %+v", got)
	}
}
