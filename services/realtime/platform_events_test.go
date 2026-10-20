package realtimehttp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/schedule"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func eventBody(text string, recipient map[string]any, events ...string) map[string]any {
	return map[string]any{
		"text": text, "recipient": recipient, "timezone": "UTC",
		"recurrence": map[string]any{"kind": "event", "events": events},
	}
}

// dispatch posts ev to the internal route as caller and answers how many
// schedules it set.
func (f *schedFixture) dispatch(caller string, ev wire.PlatformEvent) (int, int) {
	f.t.Helper()
	code, out := f.do(caller, http.MethodPost, "/internal/v1/platform-events", ev)
	var reply wire.PlatformEventReply
	_ = json.Unmarshal(out, &reply)
	return code, reply.Triggered
}

var gatewayCaller = svcauth.ServiceSubject("gateway")

// An event schedule has no clock: it is created with no next run, and an
// unknown event is refused.
func TestEventScheduleCreate(t *testing.T) {
	f := newSchedFixture(t)
	sc := f.create("alice", eventBody("run the end-to-end plan", map[string]any{"kind": "room"}, "desktop.published", "platform.deployed"))
	if !sc.Enabled || sc.NextRunAtMs != 0 || sc.Recurrence.Kind != wire.RecurEvent || strings.Join(sc.Recurrence.Events, ",") != "platform.deployed,desktop.published" {
		t.Fatalf("created = %+v", sc)
	}
	if code, out := f.do("alice", http.MethodPost, "/v1/rooms/room-1/schedules", eventBody("x", map[string]any{"kind": "room"}, "room.created")); code != http.StatusBadRequest {
		t.Fatalf("an unknown event = %d %s", code, out)
	}
}

// A platform event runs every enabled schedule following it once, through
// the scheduler's one run path, with the event's facts fenced in its text;
// the schedule stays on with no next run until the next event.
func TestPlatformEventRunsFollowingSchedulesOnce(t *testing.T) {
	f := newSchedFixture(t)
	ctx := context.Background()
	agent := map[string]any{"kind": "agent", "ownerId": "bob", "provider": "codex"}
	follows := f.create("alice", eventBody("Run the end-to-end test plan.", agent, "platform.deployed"))
	components := f.create("bob", eventBody("Draft release notes.", map[string]any{"kind": "room"}, "component.published"))
	clock := f.create("alice", dailyBody("standup", map[string]any{"kind": "room"}))
	off := eventBody("paused", map[string]any{"kind": "room"}, "platform.deployed")
	off["enabled"] = false
	paused := f.create("alice", off)
	// A request waiting for its installer's agent is never run, even if a
	// row says it is on.
	waiting := wire.RoomSchedule{ScheduleID: "sch_waiting", RoomID: "room-1", CreatedBy: "alice", Text: "waits", Enabled: true,
		Recipient:  wire.ScheduleRecipient{Kind: wire.ScheduleRecipientAgent},
		Recurrence: wire.ScheduleRecurrence{Kind: wire.RecurEvent, Events: []string{wire.PlatformDeployed}}, Timezone: "UTC"}
	if _, err := f.sched.CreateSchedule(tenant.NewContext(ctx, tenant.Default), waiting); err != nil {
		t.Fatal(err)
	}

	deploy := wire.PlatformEvent{Event: wire.PlatformDeployed, Version: "abc123", Previous: "9f8e7d"}
	if code, n := f.dispatch("alice", deploy); code != http.StatusForbidden || n != 0 {
		t.Fatalf("a member dispatching = %d (%d set)", code, n)
	}
	if code, n := f.dispatch(gatewayCaller, wire.PlatformEvent{Event: "room.created"}); code != http.StatusBadRequest || n != 0 {
		t.Fatalf("an unknown event = %d (%d set)", code, n)
	}
	before := time.Now()
	if code, n := f.dispatch(gatewayCaller, deploy); code != http.StatusOK || n != 1 {
		t.Fatalf("dispatch = %d, set %d; want 1 (only the enabled, runnable schedule following deploys)", code, n)
	}
	if len(f.svc.schedulerWake()) != 1 {
		t.Fatal("the dispatch did not wake the scheduler")
	}
	for _, id := range []string{components.ScheduleID, clock.ScheduleID, paused.ScheduleID, waiting.ScheduleID} {
		if got := f.get(id); got.Event != nil || (id != clock.ScheduleID && got.NextRunAtMs != 0) || (id == clock.ScheduleID && got.NextRunAtMs != clock.NextRunAtMs) {
			t.Errorf("schedule %s was set by an event it does not take: %+v", id, got)
		}
	}
	set := f.get(follows.ScheduleID)
	if set.NextRunAtMs < before.UnixMilli() || set.NextRunAtMs > time.Now().UnixMilli() || set.Event == nil || set.Event.Version != "abc123" {
		t.Fatalf("set = %+v (event %+v)", set, set.Event)
	}
	if n := f.svc.RunDueSchedules(ctx, time.Now().Add(time.Second)); n != 1 {
		t.Fatalf("ran %d schedules, want 1", n)
	}
	msgs := f.history()
	if len(msgs) != 1 {
		t.Fatalf("history has %d messages, want 1", len(msgs))
	}
	var p map[string]any
	_ = json.Unmarshal(msgs[0].Payload, &p)
	text, _ := p["text"].(string)
	if msgs[0].Author != "alice" || !strings.HasPrefix(text, "Run the end-to-end test plan.\n\n<<<untrusted-content ") ||
		!strings.Contains(text, "platform.deployed version=abc123 previous=9f8e7d") {
		t.Fatalf("posted %q as %s", text, msgs[0].Author)
	}
	if rcpt, _ := p["recipient"].(map[string]any); rcpt["ownerId"] != "bob" {
		t.Fatalf("recipient = %v", p["recipient"])
	}
	after := f.get(follows.ScheduleID)
	if !after.Enabled || after.NextRunAtMs != 0 || after.Event != nil || after.RunCount != 1 || after.LastStatus != wire.ScheduleRunOK {
		t.Fatalf("after its run = %+v; want on, no next run, no event", after)
	}
	if n := f.svc.RunDueSchedules(ctx, time.Now().Add(time.Hour)); n != 0 {
		t.Fatalf("an event schedule ran again without an event (%d)", n)
	}
}

// A burst of events runs a schedule once per schedule.MinInterval: events
// before its run take its pending run, one soon after a run waits for the
// interval.
func TestPlatformEventsCoalesce(t *testing.T) {
	f := newSchedFixture(t)
	ctx := context.Background()
	sc := f.create("alice", eventBody("notes", map[string]any{"kind": "room"}, "component.published"))
	ev := func(name string) wire.PlatformEvent {
		return wire.PlatformEvent{Event: wire.ComponentPublished, Kind: "mcp", Name: name, Version: "1.0.0"}
	}
	now := time.Now()
	for _, name := range []string{"a", "b", "c"} {
		if _, err := f.svc.DispatchPlatformEvent(ctx, ev(name), now); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.svc.RunDueSchedules(ctx, now.Add(time.Second)); n != 1 || len(f.history()) != 1 {
		t.Fatalf("three events ran %d times (%d messages), want once", n, len(f.history()))
	}
	var p map[string]any
	_ = json.Unmarshal(f.history()[0].Payload, &p)
	if text, _ := p["text"].(string); !strings.Contains(text, "name=c ") {
		t.Fatalf("the run answers the latest event: %q", text)
	}
	if _, err := f.svc.DispatchPlatformEvent(ctx, ev("d"), now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	last := f.get(sc.ScheduleID).LastRunAtMs
	if got := f.get(sc.ScheduleID).NextRunAtMs; got != last+schedule.MinInterval.Milliseconds() {
		t.Fatalf("an event soon after a run is set at %d, want %d (the interval after the run)", got, last+schedule.MinInterval.Milliseconds())
	}
	if n := f.svc.RunDueSchedules(ctx, now.Add(time.Minute)); n != 0 {
		t.Fatalf("ran %d within the interval", n)
	}
	if n := f.svc.RunDueSchedules(ctx, time.UnixMilli(last).Add(schedule.MinInterval+time.Second)); n != 1 || len(f.history()) != 2 {
		t.Fatalf("after the interval ran %d (%d messages)", n, len(f.history()))
	}
}

// An organization's own component publish reaches only that
// organization's schedules; a platform event reaches every organization.
func TestPlatformEventOrganizationScope(t *testing.T) {
	f := newSchedFixture(t)
	ctx := context.Background()
	mk := func(tid, id string) {
		sc := wire.RoomSchedule{ScheduleID: id, RoomID: "room-" + tid, CreatedBy: "maya", Text: "x", Enabled: true,
			Recipient:  wire.ScheduleRecipient{Kind: wire.ScheduleRecipientRoom},
			Recurrence: wire.ScheduleRecurrence{Kind: wire.RecurEvent, Events: []string{wire.ComponentPublished}}, Timezone: "UTC"}
		if _, err := f.sched.CreateSchedule(tenant.NewContext(ctx, tid), sc); err != nil {
			t.Fatal(err)
		}
	}
	mk("acme", "sch_acme")
	mk("nimbus", "sch_nimbus")
	now := time.Now()
	n, err := f.svc.DispatchPlatformEvent(ctx, wire.PlatformEvent{Event: wire.ComponentPublished, Kind: "skill", Name: "notes", Version: "1.0.0", Tenants: []string{"acme"}}, now)
	if err != nil || n != 1 {
		t.Fatalf("an organization's publish set %d (%v), want 1", n, err)
	}
	next := func(tid, id string) int64 {
		sc, err := f.sched.GetSchedule(tenant.NewContext(ctx, tid), "room-"+tid, id)
		if err != nil {
			t.Fatal(err)
		}
		return sc.NextRunAtMs
	}
	if next("acme", "sch_acme") == 0 || next("nimbus", "sch_nimbus") != 0 {
		t.Fatal("the organization's publish reached another organization")
	}
	if n, err := f.svc.DispatchPlatformEvent(ctx, wire.PlatformEvent{Event: wire.ComponentPublished, Kind: "mcp", Name: "browser", Version: "2.0.0"}, now); err != nil || n != 2 {
		t.Fatalf("a platform publish set %d (%v), want both", n, err)
	}
}

// Run now of an event schedule posts its text without an event's facts,
// even with one pending (that run keeps its own).
func TestEventScheduleRunNowCarriesNoEvent(t *testing.T) {
	f := newSchedFixture(t)
	sc := f.create("alice", eventBody("check the deploy", map[string]any{"kind": "room"}, "platform.deployed"))
	if _, err := f.svc.DispatchPlatformEvent(context.Background(), wire.PlatformEvent{Event: wire.PlatformDeployed, Version: "abc"}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if code, out := f.do("alice", http.MethodPost, "/v1/rooms/room-1/schedules/"+sc.ScheduleID+"/run", nil); code != http.StatusOK {
		t.Fatalf("run now = %d %s", code, out)
	}
	var p map[string]any
	_ = json.Unmarshal(f.history()[0].Payload, &p)
	if p["text"] != "check the deploy" {
		t.Fatalf("run now posted %q", p["text"])
	}
	if got := f.get(sc.ScheduleID); got.Event == nil || got.NextRunAtMs == 0 {
		t.Fatalf("run now consumed the pending event run: %+v", got)
	}
}
