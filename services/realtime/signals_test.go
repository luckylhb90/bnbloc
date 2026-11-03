package realtimehttp

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/schedule"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// bridgeOf registers bob's bridge so a job leased by it runs "on bob's
// bridge" for the signal's check.
func (f *schedFixture) bridgeOf(owner, id string) {
	f.t.Helper()
	if err := f.jobs.PutBridgeHeartbeat(tenant.NewContext(context.Background(), tenant.Default),
		store.BridgeHeartbeat{BridgeID: id, OwnerUserID: owner, Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *schedFixture) signal(caller string, in wire.SignalRequest) (int, wire.MemberEventReply, string) {
	f.t.Helper()
	code, out := f.do(caller, http.MethodPost, "/internal/v1/rooms/room-1/signals", in)
	var reply wire.MemberEventReply
	_ = json.Unmarshal(out, &reply)
	return code, reply, string(out)
}

// A job's agent signals as the job's requester: the event is that member's,
// of the job's channel, naming the job's event as its cause and keeping its
// chain; the room's task waiting for it runs; the task whose run started the
// job does not (a flow's next station takes it, the sender never loops on
// it). Two tasks of one room that differ only in which one started the job.
func TestSignalFromAJobRunsTheNextTaskNotItsOwn(t *testing.T) {
	f := newSchedFixture(t)
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	f.bridgeOf("bob", "bridge-bob")
	member := map[string]any{"kind": "room"}
	writer := f.create("alice", eventBody("Write the draft.", member, "bnbloc/member", "draft.*"))
	reviewer := f.create("alice", eventBody("Review the draft.", member, "bnbloc/member", "draft.ready"))
	// The writer task's run asked bob's agent; that job runs on bob's bridge.
	job, err := f.jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job_write", RoomID: "room-1", ChannelID: "ch_write", MessageSeq: 5,
		ClientMsgID: schedule.RunSlotClientMsgID(writer.ScheduleID, time.Now()), RequesterUserID: "alice", OwnerUserID: "bob", Provider: "codex",
		Status: store.AgentJobRunning, LeaseBridgeID: "bridge-bob", CauseEventID: "evt_order", ChainEventID: "evt_root"})
	if err != nil {
		t.Fatal(err)
	}

	code, reply, out := f.signal("bob", wire.SignalRequest{JobID: job.JobID, Type: "draft.ready", Subject: "draft.md", Data: json.RawMessage(`{"pages":3}`)})
	if code != http.StatusAccepted {
		t.Fatalf("signal = %d %s", code, out)
	}
	ev := reply.Event
	if ev.Source != wire.SourceMemberPrefix+"alice" || ev.Type != "draft.ready" || ev.Subject != "draft.md" || ev.Channel != "ch_write" ||
		ev.Cause != "evt_order" || ev.Chain != "evt_root" || string(ev.Data) != `{"pages":3}` {
		t.Fatalf("event = %+v", ev)
	}
	if reply.Triggered != 1 || f.get(reviewer.ScheduleID).Event == nil {
		t.Fatalf("the reviewer did not take it: triggered %d", reply.Triggered)
	}
	if f.get(writer.ScheduleID).Event != nil {
		t.Fatal("the task whose run started the job ran on its own signal")
	}
}

// A dispatched job's signal is its root task's: the task that started the
// chain still does not take it.
func TestSignalFromADispatchedJobKeepsItsTask(t *testing.T) {
	f := newSchedFixture(t)
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	f.bridgeOf("olga", "bridge-olga")
	writer := f.create("alice", eventBody("Write.", map[string]any{"kind": "room"}, "bnbloc/member", "draft.*"))
	root, err := f.jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job_root", RoomID: "room-1", MessageSeq: 5,
		ClientMsgID: schedule.RunSlotClientMsgID(writer.ScheduleID, time.Now()), RequesterUserID: "alice", OwnerUserID: "bob", Provider: "codex", Status: store.AgentJobRunning})
	if err != nil {
		t.Fatal(err)
	}
	child, err := f.jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job_child", RoomID: "room-1", MessageSeq: 6, ClientMsgID: "dispatch-x",
		RequesterUserID: "alice", OwnerUserID: "olga", Provider: "claude", Status: store.AgentJobRunning, LeaseBridgeID: "bridge-olga", DispatchedByJobID: root.JobID, DispatchDepth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if code, reply, out := f.signal("olga", wire.SignalRequest{JobID: child.JobID, Type: "draft.ready"}); code != http.StatusAccepted || reply.Triggered != 0 {
		t.Fatalf("signal = %d %s (triggered %d)", code, out, reply.Triggered)
	}
	if f.get(writer.ScheduleID).Event != nil {
		t.Fatal("the root task ran on its dispatched job's signal")
	}
}

// What a signal may not be: a platform type, from a job not running on the
// caller's bridge, for a job of another room, neither or both of job and
// task, a task of someone else's; a requester who may no longer post.
func TestSignalRefusals(t *testing.T) {
	f := newSchedFixture(t)
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	f.bridgeOf("bob", "bridge-bob")
	seq := int64(0)
	put := func(id, room, requester string, status store.AgentJobStatus) {
		seq++ // one origin per job (the store dedupes jobs by room, message and agent)
		if _, err := f.jobs.PutAgentJob(ctx, store.AgentJob{JobID: id, RoomID: room, MessageSeq: seq, ClientMsgID: "c-" + id,
			RequesterUserID: requester, OwnerUserID: "bob", Provider: "codex", Status: status, LeaseBridgeID: "bridge-bob"}); err != nil {
			t.Fatal(err)
		}
	}
	put("job_live", "room-1", "alice", store.AgentJobRunning)
	put("job_done", "room-1", "alice", store.AgentJobReplied)
	put("job_away", "room-2", "alice", store.AgentJobRunning)
	put("job_gone", "room-1", "nora", store.AgentJobRunning)
	task := f.create("alice", eventBody("x", map[string]any{"kind": "room"}, "bnbloc/member", "a.*"))
	for _, c := range []struct {
		name   string
		caller string
		in     wire.SignalRequest
		want   int
	}{
		{"a platform type", "bob", wire.SignalRequest{JobID: "job_live", Type: "room.job.replied"}, http.StatusBadRequest},
		{"no job or task", "bob", wire.SignalRequest{Type: "a.b"}, http.StatusBadRequest},
		{"both job and task", "bob", wire.SignalRequest{JobID: "job_live", ScheduleID: task.ScheduleID, Type: "a.b"}, http.StatusBadRequest},
		{"another member's bridge", "alice", wire.SignalRequest{JobID: "job_live", Type: "a.b"}, http.StatusForbidden},
		{"a job that ended", "bob", wire.SignalRequest{JobID: "job_done", Type: "a.b"}, http.StatusConflict},
		{"a job of another room", "bob", wire.SignalRequest{JobID: "job_away", Type: "a.b"}, http.StatusNotFound},
		{"a requester who left", "bob", wire.SignalRequest{JobID: "job_gone", Type: "a.b"}, http.StatusForbidden},
		{"someone else's task", "bob", wire.SignalRequest{ScheduleID: task.ScheduleID, Type: "a.b"}, http.StatusForbidden},
	} {
		if code, _, out := f.signal(c.caller, c.in); code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, code, out, c.want)
		}
	}
	if code, _, out := f.signal("alice", wire.SignalRequest{ScheduleID: task.ScheduleID, Type: "a.b"}); code != http.StatusAccepted {
		t.Fatalf("the task's creator's run = %d %s", code, out)
	}
}
