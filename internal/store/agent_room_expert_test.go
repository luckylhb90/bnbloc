package store

import (
	"errors"
	"testing"
	"time"
)

// exerciseRoomExpert records a job's room expert: a change is reported once,
// a repeat is not, a later resolution may move it, and reads carry it.
func exerciseRoomExpert(t *testing.T, jobs interface {
	BridgeJobStore
	AgentRunReader
	RoomExpertRecorder
}, suffix string) {
	t.Helper()
	ctx := testCtx()
	id := "job-room-expert-" + suffix
	if _, err := jobs.PutAgentJob(ctx, AgentJob{JobID: id, RoomID: "room-re-" + suffix, MessageSeq: 1, RequesterUserID: "maya", OwnerUserID: "jack",
		Core: "acme", Provider: "codex", CreatedAtMs: time.Now().UnixMilli(), ExpertID: "cc-chat", ExpertVersion: "1.0.0", ExpertName: "Reviewer"}); err != nil {
		t.Fatalf("put job: %v", err)
	}
	house := JobExpert{ID: "cc-house-" + suffix, Version: "1.2.0", Name: "House style"}
	if changed, err := jobs.RecordJobRoomExpert(ctx, id, house); err != nil || !changed {
		t.Fatalf("first record = %v, %v; want changed", changed, err)
	}
	if changed, err := jobs.RecordJobRoomExpert(ctx, id, house); err != nil || changed {
		t.Fatalf("repeat record = %v, %v; want unchanged", changed, err)
	}
	got, err := jobs.GetAgentRun(ctx, id)
	if err != nil || got.RoomExpertID != house.ID || got.RoomExpertVersion != "1.2.0" || got.RoomExpertName != "House style" || got.ExpertID != "cc-chat" {
		t.Fatalf("job = %+v, %v", got, err)
	}
	moved := JobExpert{ID: house.ID, Version: "1.3.0", Name: "House style"}
	if changed, err := jobs.RecordJobRoomExpert(ctx, id, moved); err != nil || !changed {
		t.Fatalf("moved record = %v, %v; want changed", changed, err)
	}
	if _, err := jobs.RecordJobRoomExpert(ctx, "job-missing-"+suffix, house); !errors.Is(err, ErrAgentJobNotFound) {
		t.Fatalf("unknown job err = %v", err)
	}
	if _, err := jobs.RecordJobRoomExpert(tenantCtx("other-"+suffix), id, house); !errors.Is(err, ErrAgentJobNotFound) {
		t.Fatalf("other tenant err = %v; a job is recorded only in its tenant", err)
	}
}

func TestMemoryRoomExpertRecord(t *testing.T) {
	exerciseRoomExpert(t, NewMemoryBridgeJobs(), "mem")
}

func TestMySQLRoomExpertRecord(t *testing.T) {
	m, done := mysqlJobs(t)
	defer done()
	exerciseRoomExpert(t, m, itoaBase(time.Now().UnixNano()))
}
