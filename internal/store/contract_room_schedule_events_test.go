package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
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
	sc.Recurrence = wire.ScheduleRecurrence{Kind: wire.RecurEvent, Source: wire.SubscribePlatform, Types: events,
		Filter: &wire.EventFilter{SubjectPrefix: "br", Data: map[string]string{"kind": "mcp"}}}
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
		one := must(s.ListEventSchedules(bg, EventScheduleQuery{Tenants: []string{tid}}))(t)
		if got := ids(one); !slices.Equal(got, []string{follow}) {
			t.Fatalf("one tenant's event schedules = %v, want only the enabled event one", got)
		}
		if one[0].Recurrence.Kind != wire.RecurEvent || !slices.Equal(one[0].Recurrence.Types, []string{wire.PlatformDeployed, wire.ComponentPublished}) || one[0].Recurrence.Filter == nil || one[0].Recurrence.Filter.Data["kind"] != "mcp" {
			t.Fatalf("listed recurrence = %+v", one[0].Recurrence)
		}
		want := []string{follow, elsewhere}
		slices.Sort(want)
		if got := ids(must(s.ListEventSchedules(bg, EventScheduleQuery{}))(t)); !slices.Equal(got, want) {
			t.Fatalf("every tenant's event schedules = %v, want %v", got, want)
		}

		const gap = 300_000
		ev := func(version string) wire.CloudEvent {
			return wire.CloudEvent{SpecVersion: "1.0", ID: "evt-" + version, Source: wire.SourcePlatform, Type: wire.ComponentPublished,
				Time: "2026-10-21T01:00:00Z", Subject: "browser", Data: json.RawMessage(`{"kind":"mcp","name":"browser","version":"` + version + `"}`)}
		}
		trigger := func(id string, e wire.CloudEvent, now int64) (int64, bool) {
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
		if got.NextRunAtMs != 10_000 || got.Event == nil || got.Event.ID != "evt-1.0.0" || got.Event.Subject != "browser" || !strings.Contains(string(got.Event.Data), `"version":"1.0.0"`) {
			t.Fatalf("after the first trigger = %+v (event %+v)", got, got.Event)
		}
		// A second before it ran keeps the time and takes the latest facts.
		if at, ok := trigger(follow, ev("1.0.1"), 20_000); !ok || at != 10_000 {
			t.Fatalf("pending trigger = %d, %v; want the pending time", at, ok)
		}
		token, rows := claimOwn(t, s, tid, 30_000, 150_000, MaxScheduleClaim)
		if len(rows) != 1 || rows[0].ScheduleID != follow || rows[0].Event == nil || rows[0].Event.ID != "evt-1.0.1" {
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
		if got.NextRunAtMs != 10_000+gap || got.Event == nil || got.Event.ID != "evt-1.0.2" || got.LastRunAtMs != 45_000 || !got.Enabled {
			t.Fatalf("after a run the trigger overtook = %+v (event %+v)", got, got.Event)
		}
		token, rows = claimOwn(t, s, tid, 10_000+gap, 10_000+gap+120_000, MaxScheduleClaim)
		if len(rows) != 1 || rows[0].Event == nil || rows[0].Event.ID != "evt-1.0.2" {
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
		if got := must(s.GetSchedule(ctx, room, follow))(t); got.Event == nil || got.Event.ID != "evt-1.0.3" || got.Text != "edited" {
			t.Fatalf("after an edit = %+v (event %+v)", got, got.Event)
		}
	})
}

// A room's own events reach only that room's schedules; a schedule's
// webhook is set, found by its id (with its sealed secret, which a read
// never carries), counts refusals, is unique, and is revoked; a dropped
// event is counted and noticed once until the schedule is edited.
func TestContractRoomScheduleStoreHooksAndDrops(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s := b.Schedules
		bg := context.Background()
		tid := uniq("t-hooks-")
		ctx := tenantCtx(tid)
		roomA, roomB := uniq("room-a-"), uniq("room-b-")
		a, bID := uniq("sch-a-"), uniq("sch-b-")
		for id, room := range map[string]string{a: roomA, bID: roomB} {
			sc := eventSchedule(id, room, wire.ComponentPublished)
			sc.Hook, sc.DroppedEvents = &wire.ScheduleHook{HookID: "ignored"}, 9 // a create never takes them
			if _, err := s.CreateSchedule(ctx, sc); err != nil {
				t.Fatal(err)
			}
		}
		if got := must(s.GetSchedule(ctx, roomA, a))(t); got.Hook != nil || got.DroppedEvents != 0 {
			t.Fatalf("a new schedule = %+v", got)
		}
		list := must(s.ListEventSchedules(bg, EventScheduleQuery{Tenants: []string{tid}, RoomID: roomA}))(t)
		if len(list) != 1 || list[0].ScheduleID != a {
			t.Fatalf("room A's schedules = %+v", list)
		}

		hookID := uniq("hk_")
		if err := s.SetScheduleHook(ctx, roomA, a, wire.ScheduleHook{HookID: hookID, CreatedAtMs: 5}, "v1:sealed"); err != nil {
			t.Fatal(err)
		}
		if err := s.SetScheduleHook(ctx, roomB, bID, wire.ScheduleHook{HookID: hookID, CreatedAtMs: 6}, "v1:other"); !errors.Is(err, ErrHookExists) {
			t.Fatalf("another schedule taking the hook id = %v", err)
		}
		if err := s.SetScheduleHook(ctx, roomB, uniq("sch-none-"), wire.ScheduleHook{HookID: uniq("hk_")}, "x"); !errors.Is(err, ErrScheduleNotFound) {
			t.Fatalf("a missing schedule's hook = %v", err)
		}
		if err := s.SetScheduleHook(tenantCtx(uniq("t-other-")), roomA, a, wire.ScheduleHook{HookID: uniq("hk_")}, "x"); !errors.Is(err, ErrScheduleNotFound) {
			t.Fatalf("another tenant setting the hook = %v", err)
		}
		sc, secret, err := s.ScheduleByHook(bg, hookID)
		if err != nil || sc.ScheduleID != a || sc.TenantID != tid || secret != "v1:sealed" || sc.Hook == nil || sc.Hook.HookID != hookID || sc.Hook.CreatedAtMs != 5 {
			t.Fatalf("by hook = %+v %q %v", sc, secret, err)
		}
		if _, _, err := s.ScheduleByHook(bg, uniq("hk_none_")); !errors.Is(err, ErrScheduleNotFound) {
			t.Fatalf("an unknown hook = %v", err)
		}
		for i := int64(1); i <= 2; i++ {
			if err := s.CountHookRefused(bg, hookID, 100*i); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.CountHookRefused(bg, uniq("hk_none_"), 1); err != nil {
			t.Fatal(err)
		}
		if got := must(s.GetSchedule(ctx, roomA, a))(t); got.Hook == nil || got.Hook.Refused != 2 || got.Hook.LastRefusedAtMs != 200 {
			t.Fatalf("refusals = %+v", got.Hook)
		}
		// A rotation (same id, new secret) starts the refusals over.
		if err := s.SetScheduleHook(ctx, roomA, a, wire.ScheduleHook{HookID: hookID, CreatedAtMs: 7}, "v1:rotated"); err != nil {
			t.Fatal(err)
		}
		if sc, secret, _ := s.ScheduleByHook(bg, hookID); secret != "v1:rotated" || sc.Hook.Refused != 0 {
			t.Fatalf("rotated = %+v %q", sc.Hook, secret)
		}
		if err := s.DeleteScheduleHook(ctx, roomA, a); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.ScheduleByHook(bg, hookID); !errors.Is(err, ErrScheduleNotFound) {
			t.Fatalf("a revoked hook = %v", err)
		}
		if got := must(s.GetSchedule(ctx, roomA, a))(t); got.Hook != nil {
			t.Fatalf("revoked, still %+v", got.Hook)
		}
		if err := s.DeleteScheduleHook(ctx, roomA, uniq("sch-none-")); !errors.Is(err, ErrScheduleNotFound) {
			t.Fatalf("revoking a missing schedule's hook = %v", err)
		}

		// Drops: counted each time, noticed once until an edit.
		notices := 0
		for i := 0; i < 3; i++ {
			n, err := s.CountEventDropped(bg, a)
			if err != nil {
				t.Fatal(err)
			}
			if n {
				notices++
			}
		}
		got := must(s.GetSchedule(ctx, roomA, a))(t)
		if notices != 1 || got.DroppedEvents != 3 {
			t.Fatalf("drops: %d notices, %d counted; want 1 and 3", notices, got.DroppedEvents)
		}
		if err := s.UpdateSchedule(ctx, got); err != nil {
			t.Fatal(err)
		}
		if n, err := s.CountEventDropped(bg, a); err != nil || !n {
			t.Fatalf("after an edit the next drop notices again: %v %v", n, err)
		}
		if n, err := s.CountEventDropped(bg, uniq("sch-none-")); err != nil || n {
			t.Fatalf("a missing schedule's drop = %v %v", n, err)
		}
	})
}
