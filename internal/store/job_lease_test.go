package store

import (
	"context"
	"errors"
	"testing"
)

type fakeOwners map[string]string

func (f fakeOwners) ListDispatchedJobs(context.Context, string) ([]AgentJob, error) { return nil, nil }
func (f fakeOwners) BridgeOwner(_ context.Context, id string) (string, error) {
	if o, ok := f[id]; ok {
		return o, nil
	}
	return "", ErrBridgeNotFound
}

func TestJobLeasedBy(t *testing.T) {
	ctx := context.Background()
	r := fakeOwners{"br-maya": "maya"}
	running := AgentJob{Status: AgentJobRunning, LeaseBridgeID: "br-maya"}
	if err := jobLeasedBy(ctx, r, running, "maya"); err != nil {
		t.Fatalf("owner = %v", err)
	}
	// Subjects are case-sensitive: "Maya" is another member.
	if err := jobLeasedBy(ctx, r, running, "Maya"); !errors.Is(err, ErrJobNotCallers) {
		t.Fatalf("case variant of the owner = %v, want ErrJobNotCallers", err)
	}
	if err := jobLeasedBy(ctx, r, running, "jack"); !errors.Is(err, ErrJobNotCallers) {
		t.Fatalf("other member = %v", err)
	}
	if err := jobLeasedBy(ctx, r, AgentJob{Status: AgentJobRunning, LeaseBridgeID: "br-gone"}, "maya"); !errors.Is(err, ErrJobNotCallers) {
		t.Fatalf("unknown bridge = %v", err)
	}
	for _, st := range []AgentJobStatus{AgentJobQueued, AgentJobReplied, AgentJobFailed} {
		if err := jobLeasedBy(ctx, r, AgentJob{Status: st, LeaseBridgeID: "br-maya"}, "maya"); !errors.Is(err, ErrJobNotLive) {
			t.Fatalf("%s = %v", st, err)
		}
	}
	if err := jobLeasedBy(ctx, r, AgentJob{Status: AgentJobClaimed}, "maya"); !errors.Is(err, ErrJobNotLive) {
		t.Fatalf("unleased = %v", err)
	}
	// What outlives a turn asks only whose bridge ran it last.
	if err := jobRanBy(ctx, r, AgentJob{Status: AgentJobReplied, LeaseBridgeID: "br-maya"}, "maya"); err != nil {
		t.Fatalf("ended, ran by the caller = %v", err)
	}
	if err := jobRanBy(ctx, r, AgentJob{Status: AgentJobReplied, LeaseBridgeID: "br-maya"}, "jack"); !errors.Is(err, ErrJobNotCallers) {
		t.Fatalf("ended, ran by another = %v", err)
	}
	if err := jobRanBy(ctx, r, AgentJob{Status: AgentJobCancelled}, "maya"); !errors.Is(err, ErrJobNotLive) {
		t.Fatalf("cancelled (lease cleared) = %v", err)
	}
}

type fakeLeasedJobs struct {
	fakeOwners
	jobs map[string]AgentJob
	err  error
}

func (f fakeLeasedJobs) GetAgentRun(_ context.Context, id string) (AgentJob, error) {
	if f.err != nil {
		return AgentJob{}, f.err
	}
	j, ok := f.jobs[id]
	if !ok {
		return AgentJob{}, ErrAgentJobNotFound
	}
	return j, nil
}

// LoadLeasedJob and its one status mapping: every caller (dispatch, team
// plan, content grant) answers the same status for the same refusal.
func TestLoadLeasedJobStatuses(t *testing.T) {
	ctx := context.Background()
	r := fakeLeasedJobs{fakeOwners: fakeOwners{"br-maya": "maya"}, jobs: map[string]AgentJob{
		"job-live": {JobID: "job-live", RoomID: "room-1", Status: AgentJobRunning, LeaseBridgeID: "br-maya"},
		"job-done": {JobID: "job-done", RoomID: "room-1", Status: AgentJobReplied, LeaseBridgeID: "br-maya"},
	}}
	for _, c := range []struct {
		job, room, caller string
		r                 LeasedJobReader
		status            int
	}{
		{"job-live", "room-1", "maya", r, 0},
		{"job-gone", "room-1", "maya", r, 404},
		{"job-live", "room-2", "maya", r, 404},
		{"job-done", "room-1", "maya", r, 409},
		{"job-live", "room-1", "jack", r, 403},
		{"job-live", "room-1", "maya", fakeLeasedJobs{err: errors.New("db down")}, 500},
	} {
		job, err := LoadLeasedJob(ctx, c.r, c.job, c.room, c.caller)
		if c.status == 0 {
			if err != nil || job.JobID != c.job {
				t.Errorf("%s/%s/%s: %v", c.job, c.room, c.caller, err)
			}
			continue
		}
		if got := LeasedJobStatus(err); err == nil || got != c.status {
			t.Errorf("%s/%s/%s: err %v status %d, want %d", c.job, c.room, c.caller, err, got, c.status)
		}
	}
}
