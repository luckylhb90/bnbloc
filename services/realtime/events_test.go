package realtimehttp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/schedule"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// eventBody is a create body of a task whose trigger takes types of source.
func eventBody(text string, recipient map[string]any, source string, types ...string) map[string]any {
	return map[string]any{
		"text": text, "recipient": recipient, "timezone": "UTC",
		"recurrence": map[string]any{"kind": "event", "source": source, "types": types},
	}
}

// platformEvent is a platform event of typ with data.
func platformEvent(typ, subject, data string) wire.CloudEvent {
	return wire.CloudEvent{SpecVersion: wire.CloudEventsVersion, ID: ids.New("evt_"), Source: wire.SourcePlatform, Type: typ,
		Time: wire.EventTime(time.Now()), Subject: subject, Data: json.RawMessage(data)}
}

// dispatch posts d to the internal route as caller and answers how many
// tasks it set.
func (f *schedFixture) dispatch(caller string, d wire.EventDispatch) (int, int) {
	f.t.Helper()
	code, out := f.do(caller, http.MethodPost, "/internal/v1/events", d)
	var reply wire.EventDispatchReply
	_ = json.Unmarshal(out, &reply)
	return code, reply.Triggered
}

// platformDispatch dispatches a platform event in process.
func (f *schedFixture) platformDispatch(ev wire.CloudEvent, now time.Time, tenants ...string) int {
	f.t.Helper()
	n, _, err := f.svc.dispatchEvent(context.Background(), eventDelivery{event: ev, reach: store.EventScheduleQuery{Tenants: tenants}}, now)
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

var gatewayCaller = svcauth.ServiceSubject("gateway")

// An event task has no clock: it is created with no next run; an unknown
// source, a platform type that does not exist and a pattern with a star
// inside are refused.
func TestEventScheduleCreate(t *testing.T) {
	f := newSchedFixture(t)
	sc := f.create("alice", eventBody("run the end-to-end plan", map[string]any{"kind": "room"}, "bnbloc/platform", "desktop.published", "platform.deployed", "desktop.published"))
	if !sc.Enabled || sc.NextRunAtMs != 0 || sc.Recurrence.Kind != wire.RecurEvent || strings.Join(sc.Recurrence.Types, ",") != "desktop.published,platform.deployed" {
		t.Fatalf("created = %+v", sc)
	}
	for name, body := range map[string]map[string]any{
		"unknown source":        eventBody("x", map[string]any{"kind": "room"}, "bnbloc/other", "x"),
		"unknown platform type": eventBody("x", map[string]any{"kind": "room"}, "bnbloc/platform", "room.created"),
		"a room type elsewhere": eventBody("x", map[string]any{"kind": "room"}, "bnbloc/platform", "room.file.added"),
		"star inside":           eventBody("x", map[string]any{"kind": "room"}, "webhook", "com.*.push"),
		"no type":               eventBody("x", map[string]any{"kind": "room"}, "webhook"),
	} {
		if code, out := f.do("alice", http.MethodPost, "/v1/rooms/room-1/schedules", body); code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", name, code, out)
		}
	}
	if sc := f.create("alice", eventBody("x", map[string]any{"kind": "room"}, "bnbloc/room", "room.*")); strings.Join(sc.Recurrence.Types, ",") != "room.*" {
		t.Fatalf("a prefix of the room's types = %+v", sc.Recurrence)
	}
}

// A platform event runs every enabled task whose trigger takes it once,
// through the scheduler's one run path, with the event fenced in its text;
// the task stays on with no next run until the next event.
func TestPlatformEventRunsFollowingSchedulesOnce(t *testing.T) {
	f := newSchedFixture(t)
	ctx := context.Background()
	agent := map[string]any{"kind": "agent", "ownerId": "bob", "provider": "codex"}
	follows := f.create("alice", eventBody("Run the end-to-end test plan.", agent, "bnbloc/platform", "platform.deployed"))
	components := f.create("bob", eventBody("Draft release notes.", map[string]any{"kind": "room"}, "bnbloc/platform", "component.published"))
	roomFiles := f.create("bob", eventBody("Look at the file.", map[string]any{"kind": "room"}, "bnbloc/room", "room.*"))
	clock := f.create("alice", dailyBody("standup", map[string]any{"kind": "room"}))
	off := eventBody("paused", map[string]any{"kind": "room"}, "bnbloc/platform", "platform.deployed")
	off["enabled"] = false
	paused := f.create("alice", off)
	// A request waiting for its installer's agent is never run, even if a
	// row says it is on.
	waiting := wire.RoomSchedule{ScheduleID: "sch_waiting", RoomID: "room-1", CreatedBy: "alice", Text: "waits", Enabled: true,
		Recipient:  wire.ScheduleRecipient{Kind: wire.ScheduleRecipientAgent},
		Recurrence: wire.ScheduleRecurrence{Kind: wire.RecurEvent, Source: wire.SubscribePlatform, Types: []string{wire.PlatformDeployed}}, Timezone: "UTC"}
	if _, err := f.sched.CreateSchedule(tenant.NewContext(ctx, tenant.Default), waiting); err != nil {
		t.Fatal(err)
	}

	deploy := wire.EventDispatch{Event: platformEvent(wire.PlatformDeployed, "", `{"version":"abc123","previous":"9f8e7d"}`)}
	if code, n := f.dispatch("alice", deploy); code != http.StatusForbidden || n != 0 {
		t.Fatalf("a member dispatching = %d (%d set)", code, n)
	}
	if code, n := f.dispatch(gatewayCaller, wire.EventDispatch{Event: platformEvent("room.created", "", "{}")}); code != http.StatusBadRequest || n != 0 {
		t.Fatalf("an unknown event = %d (%d set)", code, n)
	}
	roomEvent := platformEvent(wire.RoomFileAdded, "", "{}")
	roomEvent.Source = wire.SourceRoomPrefix + "room-1"
	if code, n := f.dispatch(gatewayCaller, wire.EventDispatch{Event: roomEvent}); code != http.StatusBadRequest || n != 0 {
		t.Fatalf("a room's event from the gateway = %d (%d set): only realtime emits them", code, n)
	}
	bad := deploy
	bad.Event.Data = json.RawMessage(`{"x":"` + strings.Repeat("a", wire.MaxEventDataBytes) + `"}`)
	if code, n := f.dispatch(gatewayCaller, bad); code != http.StatusBadRequest || n != 0 {
		t.Fatalf("data over the bound = %d (%d set)", code, n)
	}
	before := time.Now()
	if code, n := f.dispatch(gatewayCaller, deploy); code != http.StatusOK || n != 1 {
		t.Fatalf("dispatch = %d, set %d; want 1 (only the enabled, runnable task taking deploys)", code, n)
	}
	if len(f.svc.schedulerWake()) != 1 {
		t.Fatal("the dispatch did not wake the scheduler")
	}
	for _, id := range []string{components.ScheduleID, roomFiles.ScheduleID, clock.ScheduleID, paused.ScheduleID, waiting.ScheduleID} {
		if got := f.get(id); got.Event != nil || (id != clock.ScheduleID && got.NextRunAtMs != 0) || (id == clock.ScheduleID && got.NextRunAtMs != clock.NextRunAtMs) {
			t.Errorf("schedule %s was set by an event it does not take: %+v", id, got)
		}
	}
	set := f.get(follows.ScheduleID)
	if set.NextRunAtMs < before.UnixMilli() || set.NextRunAtMs > time.Now().UnixMilli() || set.Event == nil || set.Event.Type != wire.PlatformDeployed {
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
		!strings.Contains(text, "event: platform.deployed\nsource: bnbloc/platform") || !strings.Contains(text, `data: {"version":"abc123","previous":"9f8e7d"}`) {
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

// A burst of events runs a task once per schedule.MinInterval: events
// before its run take its pending run, one soon after a run waits for the
// interval. The filter narrows it to one component.
func TestPlatformEventsCoalesce(t *testing.T) {
	f := newSchedFixture(t)
	ctx := context.Background()
	body := eventBody("notes", map[string]any{"kind": "room"}, "bnbloc/platform", "component.published")
	body["recurrence"].(map[string]any)["filter"] = map[string]any{"data": map[string]any{"kind": "mcp"}}
	sc := f.create("alice", body)
	ev := func(name, kind string) wire.CloudEvent {
		return platformEvent(wire.ComponentPublished, name, `{"kind":"`+kind+`","name":"`+name+`","version":"1.0.0"}`)
	}
	now := time.Now()
	for _, name := range []string{"a", "b", "c"} {
		f.platformDispatch(ev(name, "mcp"), now)
	}
	if n := f.platformDispatch(ev("skills", "skill"), now); n != 0 {
		t.Fatalf("an event the filter refuses set %d", n)
	}
	if n := f.svc.RunDueSchedules(ctx, now.Add(time.Second)); n != 1 || len(f.history()) != 1 {
		t.Fatalf("three events ran %d times (%d messages), want once", n, len(f.history()))
	}
	var p map[string]any
	_ = json.Unmarshal(f.history()[0].Payload, &p)
	if text, _ := p["text"].(string); !strings.Contains(text, "subject: c\n") {
		t.Fatalf("the run answers the latest event: %q", text)
	}
	f.platformDispatch(ev("d", "mcp"), now.Add(2*time.Second))
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
// organization's tasks; a platform event reaches every organization.
func TestPlatformEventOrganizationScope(t *testing.T) {
	f := newSchedFixture(t)
	ctx := context.Background()
	mk := func(tid, id string) {
		sc := wire.RoomSchedule{ScheduleID: id, RoomID: "room-" + tid, CreatedBy: "maya", Text: "x", Enabled: true,
			Recipient:  wire.ScheduleRecipient{Kind: wire.ScheduleRecipientRoom},
			Recurrence: wire.ScheduleRecurrence{Kind: wire.RecurEvent, Source: wire.SubscribePlatform, Types: []string{wire.ComponentPublished}}, Timezone: "UTC"}
		if _, err := f.sched.CreateSchedule(tenant.NewContext(ctx, tid), sc); err != nil {
			t.Fatal(err)
		}
	}
	mk("acme", "sch_acme")
	mk("nimbus", "sch_nimbus")
	now := time.Now()
	if n := f.platformDispatch(platformEvent(wire.ComponentPublished, "notes", `{"kind":"skill","name":"notes","version":"1.0.0"}`), now, "acme"); n != 1 {
		t.Fatalf("an organization's publish set %d, want 1", n)
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
	if n := f.platformDispatch(platformEvent(wire.ComponentPublished, "browser", `{"kind":"mcp","name":"browser","version":"2.0.0"}`), now); n != 2 {
		t.Fatalf("a platform publish set %d, want both", n)
	}
}

// Run now of an event task posts its text without an event, even with one
// pending (that run keeps its own).
func TestEventScheduleRunNowCarriesNoEvent(t *testing.T) {
	f := newSchedFixture(t)
	sc := f.create("alice", eventBody("check the deploy", map[string]any{"kind": "room"}, "bnbloc/platform", "platform.deployed"))
	f.platformDispatch(platformEvent(wire.PlatformDeployed, "", `{"version":"abc"}`), time.Now().Add(time.Hour))
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

// Rate limits: one source's burst and one task's burst; an event over a
// limit is dropped and counted on the task, and the room's owner is told
// once (until the task is edited).
func TestEventRateLimitsDropAndCount(t *testing.T) {
	f := newSchedFixture(t)
	sc := f.create("alice", eventBody("x", map[string]any{"kind": "room"}, "bnbloc/platform", "platform.deployed"))
	now := time.Now()
	set, dropped := 0, 0
	for i := 0; i < int(taskEventBurst)+5; i++ {
		n, d, err := f.svc.dispatchEvent(context.Background(), eventDelivery{event: platformEvent(wire.PlatformDeployed, "", `{"version":"v"}`)}, now)
		if err != nil {
			t.Fatal(err)
		}
		set, dropped = set+n, dropped+d
	}
	if set != int(taskEventBurst) || dropped != 5 {
		t.Fatalf("set %d, dropped %d; want %d and 5 (the task's burst)", set, dropped, int(taskEventBurst))
	}
	if got := f.get(sc.ScheduleID); got.DroppedEvents != 5 {
		t.Fatalf("dropped events counted = %d, want 5", got.DroppedEvents)
	}
	if owners := f.svc.roomOwners(context.Background(), "room-1"); strings.Join(owners, ",") != "olga" {
		t.Fatalf("the drop notice goes to %v, want the room's owner", owners)
	}
	// The source's own burst bounds every task of one source (the tasks'
	// own limits set wide here to isolate it).
	g := newSchedFixture(t)
	g.svc.eventLimits.task.Get(1000, 1000, nil)
	for i := 0; i < 3; i++ {
		g.create("alice", eventBody("x", map[string]any{"kind": "room"}, "bnbloc/platform", "desktop.published"))
	}
	total, totalDropped := 0, 0
	for i := 0; i < int(sourceEventBurst)+1; i++ {
		n, d, _ := g.svc.dispatchEvent(context.Background(), eventDelivery{event: platformEvent(wire.DesktopPublished, "", `{}`)}, now)
		total, totalDropped = total+n, totalDropped+d
	}
	if total != 3*int(sourceEventBurst) || totalDropped != 3 {
		t.Fatalf("the source's limit set %d and dropped %d; want %d and 3 (the event past its burst, for every task)", total, totalDropped, 3*int(sourceEventBurst))
	}
}

// The drop notice says which task, which event and what to do, once per
// task until it is edited (its dedup key moves with the edit).
func TestDropNotice(t *testing.T) {
	sc := wire.RoomSchedule{ScheduleID: "sch_1", RoomID: "room-1", Text: "Run the end-to-end test plan and report what failed to the room now", UpdatedAtMs: 42}
	n := dropNotice(sc, wire.CloudEvent{Type: "com.github.push", Source: "webhook/hk_1"}, []string{"olga"})
	if n.Type != wire.NotificationScheduleNotice || strings.Join(n.Recipients, ",") != "olga" || n.DedupKey != "schedule-drops:sch_1:42" ||
		!strings.Contains(n.Text, "com.github.push from webhook/hk_1") || !strings.Contains(n.Text, "Run the end-to-end test plan and report what failed to the room…") {
		t.Fatalf("notice = %+v", n)
	}
}

// A platform event an agent's job caused under its grant names that job:
// the room whose scheduled run started the job does not take it, another
// room does.
func TestPlatformEventCausedByARunSkipsItsRoom(t *testing.T) {
	f := newSchedFixture(t)
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	agent := map[string]any{"kind": "agent", "ownerId": "bob", "provider": "codex"}
	runner := f.create("alice", dailyBody("Publish the release notes pack.", agent))
	if code, out := f.do("alice", http.MethodPost, "/v1/rooms/room-1/schedules/"+runner.ScheduleID+"/run", nil); code != http.StatusOK {
		t.Fatalf("run now = %d %s", code, out)
	}
	jobs, err := f.jobs.ListAgentRuns(ctx, store.AgentRunQuery{RoomID: "room-1"})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %+v %v", jobs, err)
	}
	own := f.create("alice", eventBody("x", map[string]any{"kind": "room"}, "bnbloc/platform", "component.published"))
	other := wire.RoomSchedule{ScheduleID: "sch_other_room", RoomID: "room-2", CreatedBy: "maya", Text: "x", Enabled: true,
		Recipient:  wire.ScheduleRecipient{Kind: wire.ScheduleRecipientRoom},
		Recurrence: wire.ScheduleRecurrence{Kind: wire.RecurEvent, Source: wire.SubscribePlatform, Types: []string{wire.ComponentPublished}}, Timezone: "UTC"}
	if _, err := f.sched.CreateSchedule(ctx, other); err != nil {
		t.Fatal(err)
	}
	d := wire.EventDispatch{Event: platformEvent(wire.ComponentPublished, "notes", `{"kind":"skill","name":"notes","version":"1.0.0"}`),
		CauseJob: jobs[0].JobID, CauseTenant: tenant.Default}
	if code, n := f.dispatch(gatewayCaller, d); code != http.StatusOK || n != 1 {
		t.Fatalf("dispatch = %d, set %d; want only the other room's", code, n)
	}
	if f.get(own.ScheduleID).NextRunAtMs != 0 || f.getIn("room-2", other.ScheduleID).NextRunAtMs == 0 {
		t.Fatal("the run's own room took its publish, or another room missed it")
	}
	d.CauseJob = ""
	if _, n := f.dispatch(gatewayCaller, d); n != 2 {
		t.Fatalf("an uncaused publish set %d, want both", n)
	}
}
