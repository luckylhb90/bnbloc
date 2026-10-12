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
	if err := JobLeasedBy(ctx, r, running, "Maya"); err != nil {
		t.Fatalf("owner = %v", err)
	}
	if err := JobLeasedBy(ctx, r, running, "jack"); !errors.Is(err, ErrJobNotCallers) {
		t.Fatalf("other member = %v", err)
	}
	if err := JobLeasedBy(ctx, r, AgentJob{Status: AgentJobRunning, LeaseBridgeID: "br-gone"}, "maya"); !errors.Is(err, ErrJobNotCallers) {
		t.Fatalf("unknown bridge = %v", err)
	}
	for _, st := range []AgentJobStatus{AgentJobQueued, AgentJobReplied, AgentJobFailed} {
		if err := JobLeasedBy(ctx, r, AgentJob{Status: st, LeaseBridgeID: "br-maya"}, "maya"); !errors.Is(err, ErrJobNotLive) {
			t.Fatalf("%s = %v", st, err)
		}
	}
	if err := JobLeasedBy(ctx, r, AgentJob{Status: AgentJobClaimed}, "maya"); !errors.Is(err, ErrJobNotLive) {
		t.Fatalf("unleased = %v", err)
	}
}
