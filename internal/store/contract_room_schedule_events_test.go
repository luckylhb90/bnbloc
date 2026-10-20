package store

import (
	"context"
	"slices"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// RoomScheduleStore contract, event schedules
// (docs/prd/2026-10-20-event-triggered-schedules.md): the dispatch lists
// the enabled event schedules of the organizations an event reaches, and
// a trigger sets one run, coalesced by the minimum gap; the run's claim
// and completion carry and then clear the event.

func eventSchedule(id, room string, events ...string) wire.RoomSchedule {
	sc := sampleSchedule(id, room, "maya", 0)
	sc.Recurrence = wire.ScheduleRecurrence{Kind: wire.RecurEvent, Events: events}
	return sc
}

func TestContractRoomScheduleStoreEvents(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s := b.Schedules
		bg := context.Background()
		tid, tid2 := uniq("t-evs-"), uniq("t-evs-other-")
		ctx, other := tenantCtx(tid), tenantCtx(tid2)
		room := uniq("room-")
		follow, paused, clock, elsewhere := uniq("sch-ev-"), uniq("sch-ev-off-"), uniq("sch-clock-"), uniq("sch-ev-t2-")
		mustCreate := func(c context.Context, sc wire.RoomSchedule) {
			t.Helper()
			if _, err := s.CreateSchedule(c, sc); err != nil {
				t.Fatal(err)
			}
		}
		mustCreate(ctx, eventSchedule(follow, room, wire.PlatformDeployed, wire.ComponentPublished))
		off := eventSchedule(paused, room, wire.PlatformDeployed)
		off.Enabled = false
		mustCreate(ctx, off)
		mustCreate(ctx, sampleSchedule(clock, room, "maya", 0))
		mustCreate(other, eventSchedule(elsewhere, uniq("room-"), wire.DesktopPublished))

		ids := func(list []wire.RoomSchedule) []string {
			var out []string
			for _, sc := range list {
				if sc.TenantID == tid || sc.TenantID == tid2 {
					out = append(out, sc.ScheduleID)
				}
			}
			slices.Sort(out)
			return out
		}
		one := must(s.ListEventSchedules(bg, []string{tid}))(t)
		if got := ids(one); !slices.Equal(got, []string{follow}) {
			t.Fatalf("one tenant's event schedules = %v, want only the enabled event one", got)
		}
		if one[0].Recurrence.Kind != wire.RecurEvent || !slices.Equal(one[0].Recurrence.Events, []string{wire.PlatformDeployed, wire.ComponentPublished}) {
			t.Fatalf("listed recurrence = %+v", one[0].Recurrence)
		}
		want := []string{follow, elsewhere}
		slices.Sort(want)
		if got := ids(must(s.ListEventSchedules(bg, nil))(t)); !slices.Equal(got, want) {
			t.Fatalf("every tenant's event schedules = %v, want %v", got, want)
		}

		const gap = 300_000
		ev := func(version string) wire.PlatformEvent {
			return wire.PlatformEvent{Event: wire.ComponentPublished, Kind: "mcp", Name: "browser", Version: version, Rollout: "auto", Targets: []string{"a", "b"}, AtMs: 7}
		}
		trigger := func(id string, e wire.PlatformEvent, now int64) (int64, bool) {
			t.Helper()
			at, ok, err := s.TriggerEventSchedule(bg, id, e, now, gap)
			if err != nil {
				t.Fatalf("trigger %s: %v", id, err)
			}
			return at, ok
		}
		// A first event runs it now and carries the event's facts.
		if at, ok := trigger(follow, ev("1.0.0"), 10_000); !ok || at != 10_000 {
			t.Fatalf("first trigger = %d, %v; want now", at, ok)
		}
		got := must(s.GetSchedule(ctx, room, follow))(t)
		if got.NextRunAtMs != 10_000 || got.Event == nil || got.Event.Version != "1.0.0" || !slices.Equal(got.Event.Targets, []string{"a", "b"}) || got.Event.AtMs != 7 {
			t.Fatalf("after the first trigger = %+v (event %+v)", got, got.Event)
		}
		// A second before it ran keeps the time and takes the latest facts.
		if at, ok := trigger(follow, ev("1.0.1"), 20_000); !ok || at != 10_000 {
			t.Fatalf("pending trigger = %d, %v; want the pending time", at, ok)
		}
		token, rows := claimOwn(t, s, tid, 30_000, 150_000, MaxScheduleClaim)
		if len(rows) != 1 || rows[0].ScheduleID != follow || rows[0].Event == nil || rows[0].Event.Version != "1.0.1" {
			t.Fatalf("claimed = %+v", rows)
		}
		// One more while it runs follows the run by the gap, and the run's
		// completion no longer moves the schedule.
		if at, ok := trigger(follow, ev("1.0.2"), 40_000); !ok || at != 10_000+gap {
			t.Fatalf("trigger during a run = %d, %v; want %d", at, ok, 10_000+gap)
		}
		if err := s.CompleteScheduleRun(bg, ScheduleRunResult{ScheduleID: follow, ClaimToken: token, AtMs: 45_000, Status: wire.ScheduleRunOK}); err != nil {
			t.Fatal(err)
		}
		got = must(s.GetSchedule(ctx, room, follow))(t)
		if got.NextRunAtMs != 10_000+gap || got.Event == nil || got.Event.Version != "1.0.2" || got.LastRunAtMs != 45_000 || !got.Enabled {
			t.Fatalf("after a run the trigger overtook = %+v (event %+v)", got, got.Event)
		}
		token, rows = claimOwn(t, s, tid, 10_000+gap, 10_000+gap+120_000, MaxScheduleClaim)
		if len(rows) != 1 || rows[0].Event == nil || rows[0].Event.Version != "1.0.2" {
			t.Fatalf("second claim = %+v", rows)
		}
		if err := s.CompleteScheduleRun(bg, ScheduleRunResult{ScheduleID: follow, ClaimToken: token, AtMs: 10_000 + gap, Status: wire.ScheduleRunOK}); err != nil {
			t.Fatal(err)
		}
		got = must(s.GetSchedule(ctx, room, follow))(t)
		if got.NextRunAtMs != 0 || got.Event != nil || !got.Enabled || got.RunCount != 2 {
			t.Fatalf("after its own run = %+v (event %+v); want no next run, no event, still on", got, got.Event)
		}
		// An event soon after a run waits for the gap.
		if at, ok := trigger(follow, ev("1.0.3"), 10_000+gap+20_000); !ok || at != 10_000+2*gap {
			t.Fatalf("trigger soon after a run = %d, %v; want %d", at, ok, 10_000+2*gap)
		}
		// A paused, a clock or a missing schedule is never set.
		for _, id := range []string{paused, clock, uniq("sch-none-")} {
			if _, ok := trigger(id, ev("2.0.0"), 50_000); ok {
				t.Errorf("trigger %s was set", id)
			}
		}
		if got := must(s.GetSchedule(ctx, room, clock))(t); got.NextRunAtMs != 0 || got.Event != nil {
			t.Fatalf("a clock schedule took an event: %+v", got)
		}
		// An edit keeps a pending event with its run.
		got = must(s.GetSchedule(ctx, room, follow))(t)
		got.Text = "edited"
		if err := s.UpdateSchedule(ctx, got); err != nil {
			t.Fatal(err)
		}
		if got := must(s.GetSchedule(ctx, room, follow))(t); got.Event == nil || got.Event.Version != "1.0.3" || got.Text != "edited" {
			t.Fatalf("after an edit = %+v (event %+v)", got, got.Event)
		}
	})
}
