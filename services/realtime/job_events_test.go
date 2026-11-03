package realtimehttp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// putJob stores a job a person started (no scheduled run caused it) in
// room-1 and returns it.
func (f *schedFixture) putJob(j store.AgentJob) store.AgentJob {
	f.t.Helper()
	if j.RoomID == "" {
		j.RoomID = "room-1"
	}
	if j.ClientMsgID == "" {
		j.ClientMsgID = "c-" + j.JobID
	}
	j.RequesterUserID, j.OwnerUserID, j.Provider = "alice", "bob", "codex"
	got, err := f.jobs.PutAgentJob(tenant.NewContext(context.Background(), tenant.Default), j)
	if err != nil {
		f.t.Fatal(err)
	}
	return got
}

// jobUpdated is activity's agent_job_updated of j with status (and reason).
func jobUpdated(j store.AgentJob, status store.AgentJobStatus, reason store.AgentJobFailureReason) wire.Event {
	j.Status, j.FailureReason = status, reason
	return wire.Event{ChannelID: j.ChannelID, Kind: wire.EventAgentJobUpdated, Payload: j.View()}
}

// A job's end is a room event of its channel: replied, failed (a timeout
// too) or cancelled, carrying the job, its agent and requester and its
// reply; a status that is not final (a retry requeued) emits nothing; a
// task's channel filter takes only its channel's.
func TestRoomJobEventsOnAJobsEnd(t *testing.T) {
	f := newSchedFixture(t)
	room := map[string]any{"kind": "room"}
	all := f.create("alice", eventBody("Note every job's end.", room, "bnbloc/room", "room.job.*"))
	review := eventBody("Review what the writer did.", room, "bnbloc/room", "room.job.replied")
	review["recurrence"].(map[string]any)["filter"] = map[string]any{"channel": "ch_review"}
	inReview := f.create("alice", review)
	taken := func(id string) *wire.CloudEvent { return f.get(id).Event }

	diag, _ := json.Marshal(map[string]any{wire.RunDiagReply: wire.AgentRunReply{Text: "Done: the draft is in draft.md."}})
	writer := f.putJob(store.AgentJob{JobID: "job_writer", ChannelID: "ch_write", MessageSeq: 7, DiagnosticsJSON: diag})

	f.publishRoom("room-1", jobUpdated(writer, store.AgentJobQueued, store.FailureCoreUnavailable))
	f.publishRoom("room-1", jobUpdated(writer, store.AgentJobRunning, ""))
	never(t, "a job that has not ended set a task", func() bool { return taken(all.ScheduleID) != nil })

	f.publishRoom("room-1", jobUpdated(writer, store.AgentJobReplied, ""))
	eventually(t, "the job's reply set the task", func() bool { return taken(all.ScheduleID) != nil })
	ev := taken(all.ScheduleID)
	var data wire.RoomJobEndedData
	_ = json.Unmarshal(ev.Data, &data)
	if ev.Type != wire.RoomJobReplied || ev.Subject != "job_writer" || ev.Channel != "ch_write" ||
		data.JobID != "job_writer" || data.OwnerID != "bob" || data.Provider != "codex" || data.Requester != "alice" ||
		data.Status != string(store.AgentJobReplied) || data.Reply != "Done: the draft is in draft.md." {
		t.Fatalf("event = %+v (%+v)", ev, data)
	}
	if ev.Cause != "" || ev.Chain != ev.ID {
		t.Fatalf("a person's job's event starts its own chain: cause %q chain %q (id %s)", ev.Cause, ev.Chain, ev.ID)
	}
	never(t, "a channel filter took another channel's job", func() bool { return taken(inReview.ScheduleID) != nil })

	reviewer := f.putJob(store.AgentJob{JobID: "job_review", ChannelID: "ch_review", MessageSeq: 8})
	f.publishRoom("room-1", jobUpdated(reviewer, store.AgentJobReplied, ""))
	eventually(t, "the review channel's job set its task", func() bool { return taken(inReview.ScheduleID) != nil })

	for _, c := range []struct {
		status store.AgentJobStatus
		reason store.AgentJobFailureReason
		want   string
	}{
		{store.AgentJobFailed, store.FailureAgentError, wire.RoomJobFailed},
		{store.AgentJobTimedOut, "", wire.RoomJobFailed},
		{store.AgentJobCancelled, "", wire.RoomJobCancelled},
	} {
		j := f.putJob(store.AgentJob{JobID: "job_" + string(c.status), ChannelID: "ch_write", MessageSeq: 20 + int64(len(c.status))})
		f.publishRoom("room-1", jobUpdated(j, c.status, c.reason))
		eventually(t, c.want+" for "+string(c.status), func() bool {
			ev := taken(all.ScheduleID)
			return ev != nil && ev.Subject == j.JobID
		})
		ev := taken(all.ScheduleID)
		data = wire.RoomJobEndedData{}
		_ = json.Unmarshal(ev.Data, &data)
		if ev.Type != c.want || data.Status != string(c.status) || data.FailureReason != string(c.reason) {
			t.Fatalf("%s: event %s, data %+v", c.status, ev.Type, data)
		}
	}
}

// A job a run started carries the run's event on: its job events name that
// event as their cause and keep its chain.
func TestRoomJobEventCarriesItsCause(t *testing.T) {
	f := newSchedFixture(t)
	sc := f.create("alice", eventBody("Note every job's end.", map[string]any{"kind": "room"}, "bnbloc/room", "room.job.*"))
	j := f.putJob(store.AgentJob{JobID: "job_caused", ChannelID: "ch_write", MessageSeq: 9, CauseEventID: "evt_order", ChainEventID: "evt_root"})
	f.publishRoom("room-1", jobUpdated(j, store.AgentJobReplied, ""))
	eventually(t, "the caused job's end set the task", func() bool { return f.get(sc.ScheduleID).Event != nil })
	if ev := f.get(sc.ScheduleID).Event; ev.Cause != "evt_order" || ev.Chain != "evt_root" {
		t.Fatalf("lineage = cause %q chain %q", ev.Cause, ev.Chain)
	}
}

// A scheduled run answering an event posts with that event as its job's
// cause and its chain carried on (the root event's id when it had none).
func TestScheduledRunGivesItsJobTheEvent(t *testing.T) {
	f := newSchedFixture(t)
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	sc := f.create("alice", eventBody("Write it up.", map[string]any{"kind": "agent", "ownerId": "bob", "provider": "codex"}, "bnbloc/member", "order.placed"))
	for _, c := range []struct{ id, chain, wantChain string }{{"evt_first", "", "evt_first"}, {"evt_next", "evt_first", "evt_first"}} {
		sc.Event = &wire.CloudEvent{SpecVersion: wire.CloudEventsVersion, ID: c.id, Source: wire.SourceMemberPrefix + "alice", Type: "order.placed",
			Time: wire.EventTime(time.Now()), Chain: c.chain}
		if _, err := f.svc.postScheduled(ctx, sc, time.Now().Add(time.Duration(len(c.id))*time.Minute)); err != nil {
			t.Fatal(err)
		}
		jobs, err := f.jobs.ListAgentRuns(ctx, store.AgentRunQuery{RoomID: "room-1"})
		if err != nil || len(jobs) == 0 {
			t.Fatalf("jobs = %+v (%v)", jobs, err)
		}
		var got store.AgentJob
		for _, j := range jobs {
			if j.CauseEventID == c.id {
				got = j
			}
		}
		if got.JobID == "" || got.ChainEventID != c.wantChain {
			t.Fatalf("%s: job lineage = %+v", c.id, jobs)
		}
	}
}
