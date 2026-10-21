package realtimehttp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/schedule"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

var roomCaller = svcauth.ServiceSubject("room")

// publishRoom publishes ev in room as the room service does.
func (f *schedFixture) publishRoom(room string, ev wire.Event) {
	f.t.Helper()
	if code, out := f.do(roomCaller, http.MethodPost, "/internal/v1/rooms/"+room+"/publish", ev); code != http.StatusOK {
		f.t.Fatalf("publish %s in %s = %d %s", ev.Kind, room, code, out)
	}
}

// fileAdded is the room service's artifact_updated of a new version.
func fileAdded(name string, version int, sourceJob string) wire.Event {
	return wire.Event{Kind: wire.EventArtifactUpdated, Payload: wire.ArtifactUpdated{ArtifactID: "art_" + name, Kind: "file", Title: name,
		FileName: name, CurrentVersion: version, UpdatedBy: "alice", UpdatedAtMs: time.Now().UnixMilli(), SourceJobID: sourceJob}}
}

// eventually waits for cond (the room's events dispatch off the publish).
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// never checks cond stays false while the room's events settle.
func never(t *testing.T, what string, cond func() bool) {
	t.Helper()
	time.Sleep(150 * time.Millisecond)
	if cond() {
		t.Fatalf("happened: %s", what)
	}
}

// otherRoomTask is a task of room-2 (same tenant) taking every room event.
func (f *schedFixture) otherRoomTask() wire.RoomSchedule {
	f.t.Helper()
	sc := wire.RoomSchedule{ScheduleID: "sch_room2", RoomID: "room-2", CreatedBy: "maya", Text: "x", Enabled: true,
		Recipient:  wire.ScheduleRecipient{Kind: wire.ScheduleRecipientRoom},
		Recurrence: wire.ScheduleRecurrence{Kind: wire.RecurEvent, Source: wire.SubscribeRoom, Types: []string{"room.*"}}, Timezone: "UTC"}
	if _, err := f.sched.CreateSchedule(tenant.NewContext(context.Background(), tenant.Default), sc); err != nil {
		f.t.Fatal(err)
	}
	return sc
}

func (f *schedFixture) getIn(room, id string) wire.RoomSchedule {
	f.t.Helper()
	sc, err := f.sched.GetSchedule(tenant.NewContext(context.Background(), tenant.Default), room, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return sc
}

// A file's new version is room.file.added of that room only: the room's
// task takes it (its subject the file's name), another room's never; a
// thumbnail and an artifact with no version yet are no file event; the
// filter narrows by subject.
func TestRoomFileAddedReachesOnlyItsRoom(t *testing.T) {
	f := newSchedFixture(t)
	body := eventBody("Review the new report.", map[string]any{"kind": "room"}, "bnbloc/room", "room.file.added")
	body["recurrence"].(map[string]any)["filter"] = map[string]any{"subjectPrefix": "report"}
	sc := f.create("alice", body)
	other := f.otherRoomTask()

	f.publishRoom("room-1", wire.Event{Kind: wire.EventArtifactUpdated, Payload: wire.ArtifactUpdated{ArtifactID: "art_x", FileName: "report.pdf", CurrentVersion: 1, Reason: "thumbnail"}})
	f.publishRoom("room-1", fileAdded("report-draft.md", 0, ""))
	f.publishRoom("room-1", fileAdded("notes.md", 1, ""))
	never(t, "a thumbnail, an empty artifact or a file the filter refuses set the task", func() bool { return f.get(sc.ScheduleID).NextRunAtMs != 0 })

	f.publishRoom("room-1", fileAdded("report.pdf", 2, ""))
	eventually(t, "room.file.added set the room's task", func() bool { return f.get(sc.ScheduleID).NextRunAtMs != 0 })
	got := f.get(sc.ScheduleID).Event
	var data wire.RoomFileAddedData
	_ = json.Unmarshal(got.Data, &data)
	if got.Type != wire.RoomFileAdded || got.Source != "bnbloc/room/room-1" || got.Subject != "report.pdf" || data.Version != 2 || data.By != "alice" {
		t.Fatalf("event = %+v (%+v)", got, data)
	}
	never(t, "room-1's file reached room-2's task", func() bool { return f.getIn("room-2", other.ScheduleID).NextRunAtMs != 0 })
	f.publishRoom("room-2", fileAdded("other.md", 1, ""))
	eventually(t, "room-2's own file reached its task", func() bool { return f.getIn("room-2", other.ScheduleID).NextRunAtMs != 0 })
}

// A person joining is room.member.joined, subject their id.
func TestRoomMemberJoined(t *testing.T) {
	f := newSchedFixture(t)
	sc := f.create("alice", eventBody("Welcome them.", map[string]any{"kind": "room"}, "bnbloc/room", "room.member.joined"))
	f.publishRoom("room-1", wire.Event{Kind: wire.EventMemberJoined, Payload: wire.MemberJoinedPayload{UserID: "nora"}})
	eventually(t, "the join set the task", func() bool { return f.get(sc.ScheduleID).NextRunAtMs != 0 })
	if ev := f.get(sc.ScheduleID).Event; ev.Type != wire.RoomMemberJoined || ev.Subject != "nora" || string(ev.Data) != `{"userId":"nora"}` {
		t.Fatalf("event = %+v", ev)
	}
}

// No self-trigger: a scheduled run's agent publishing a file (its job's id
// on the version) does not set its own room's file task again, nor does a
// job that run's agent dispatched; a person's file and a file of a job a
// person started do.
func TestRoomEventsNoSelfTrigger(t *testing.T) {
	f := newSchedFixture(t)
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	agent := map[string]any{"kind": "agent", "ownerId": "bob", "provider": "codex"}
	// The run of a task: its request to bob's agent becomes a job.
	runner := f.create("alice", dailyBody("Write the weekly report as a file.", agent))
	if code, out := f.do("alice", http.MethodPost, "/v1/rooms/room-1/schedules/"+runner.ScheduleID+"/run", nil); code != http.StatusOK {
		t.Fatalf("run now = %d %s", code, out)
	}
	jobs, err := f.jobs.ListAgentRuns(ctx, store.AgentRunQuery{RoomID: "room-1"})
	if err != nil || len(jobs) != 1 || !schedule.IsRunSlotClientMsgID(jobs[0].ClientMsgID) {
		t.Fatalf("the run's job = %+v (%v)", jobs, err)
	}
	runJob := jobs[0].JobID
	dispatched, err := f.jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job_dispatched", RoomID: "room-1", MessageSeq: 101, ClientMsgID: "dsp:x", RequesterUserID: "alice",
		OwnerUserID: "bob", Provider: "codex", Status: store.AgentJobQueued, DispatchedByJobID: runJob, DispatchDepth: 1})
	if err != nil {
		t.Fatal(err)
	}
	person, err := f.jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job_person", RoomID: "room-1", MessageSeq: 102, ClientMsgID: "c-typed", RequesterUserID: "alice",
		OwnerUserID: "bob", Provider: "codex", Status: store.AgentJobQueued})
	if err != nil {
		t.Fatal(err)
	}
	follower := f.create("alice", eventBody("Review every new file.", map[string]any{"kind": "room"}, "bnbloc/room", "room.file.added"))
	pending := func() bool { return f.get(follower.ScheduleID).NextRunAtMs != 0 }

	f.publishRoom("room-1", fileAdded("weekly.md", 1, runJob))
	f.publishRoom("room-1", fileAdded("weekly-chart.png", 1, dispatched.JobID))
	never(t, "the run's own file set its room's task", pending)
	// Red check of the rule: the same file without the run's job sets it.
	f.publishRoom("room-1", fileAdded("weekly.md", 2, person.JobID))
	eventually(t, "a person's job's file set the task", pending)
}

// An app's data changes are one event at once, then the rest of the window
// as one event at its end counting them; an agent's write carries its job
// (the cause), never to the app's own change feed.
func TestRoomAppDataChangedCoalesced(t *testing.T) {
	f := newSchedFixture(t)
	f.svc.AppEventWindow = 500 * time.Millisecond
	sc := f.create("alice", eventBody("Tally the votes.", map[string]any{"kind": "room"}, "bnbloc/room", "room.app.data.changed"))
	change := func(coll string) {
		f.publishRoom("room-1", wire.Event{Kind: wire.EventAppChanged, Payload: wire.AppChanged{AppID: "art_poll", Collection: coll, ID: "d1", Version: 1}})
	}
	change("votes")
	eventually(t, "the first change set the task", func() bool { return f.get(sc.ScheduleID).Event != nil })
	first := f.get(sc.ScheduleID).Event
	if first.Subject != "art_poll" || string(first.Data) != `{"appId":"art_poll","collection":"votes","changes":1}` {
		t.Fatalf("first event = %+v", first)
	}
	change("votes")
	change("comments")
	change("votes")
	eventually(t, "the window's changes became one event", func() bool {
		ev := f.get(sc.ScheduleID).Event
		return ev != nil && strings.Contains(string(ev.Data), `"changes":3`)
	})
	if ev := f.get(sc.ScheduleID).Event; string(ev.Data) != `{"appId":"art_poll","collection":"votes","changes":3}` {
		t.Fatalf("coalesced event = %s", ev.Data)
	}
	if c, _ := appChangeOf(wire.Event{Kind: wire.EventAppChanged, Payload: wire.AppChanged{AppID: "a", SourceJobID: "job_1"}}); c.SourceJobID != "" {
		t.Fatal("an app's change feed carries the writer's job")
	}
}
