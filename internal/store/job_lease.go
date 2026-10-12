package store

import (
	"context"
	"errors"
)

// The live-lease rule: work done on behalf of a running job (an agent
// dispatching through room tools, a bridge resolving the job's expert and
// skill packs) is accepted only from the member whose bridge holds that
// job's lease while the job is live.

var (
	// ErrJobNotLive: the job is not claimed, running or reflowing, or holds
	// no lease.
	ErrJobNotLive = errors.New("store: agent job is not running")
	// ErrJobNotCallers: the job's lease is held by another member's bridge.
	ErrJobNotCallers = errors.New("store: agent job is not running on the caller's bridge")
)

// JobLive reports whether a job is in a live phase (claimed, running or
// reflowing).
func JobLive(j AgentJob) bool {
	switch j.Status {
	case AgentJobClaimed, AgentJobRunning, AgentJobReflowing:
		return true
	}
	return false
}

// JobLeasedBy checks that job is live and leased by a bridge registered to
// caller: ErrJobNotLive, ErrJobNotCallers, or a store error reading the
// bridge's owner.
func JobLeasedBy(ctx context.Context, r AgentDispatchReader, job AgentJob, caller string) error {
	if !JobLive(job) || job.LeaseBridgeID == "" {
		return ErrJobNotLive
	}
	owner, err := r.BridgeOwner(ctx, job.LeaseBridgeID)
	if errors.Is(err, ErrBridgeNotFound) || (err == nil && owner != caller) {
		return ErrJobNotCallers
	}
	return err
}
