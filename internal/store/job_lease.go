package store

import (
	"context"
	"errors"
	"net/http"
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
	// ErrJobNotInRoom: no such job in the ctx tenant, or it belongs to
	// another room (one answer: a caller learns nothing about other rooms).
	ErrJobNotInRoom = errors.New("store: agent job not found in this room")
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

// jobLeasedBy checks that job is live and leased by a bridge registered to
// caller: ErrJobNotLive, ErrJobNotCallers, or a store error reading the
// bridge's owner.
func jobLeasedBy(ctx context.Context, r BridgeOwnerReader, job AgentJob, caller string) error {
	if !JobLive(job) || job.LeaseBridgeID == "" {
		return ErrJobNotLive
	}
	owner, err := r.BridgeOwner(ctx, job.LeaseBridgeID)
	if errors.Is(err, ErrBridgeNotFound) || (err == nil && owner != caller) {
		return ErrJobNotCallers
	}
	return err
}

// BridgeOwnerReader answers who a bridge id is registered to.
type BridgeOwnerReader interface {
	BridgeOwner(ctx context.Context, bridgeID string) (string, error)
}

// LeasedJobReader is what LoadLeasedJob reads: the job, and the owner of
// the bridge holding its lease.
type LeasedJobReader interface {
	GetAgentRun(ctx context.Context, jobID string) (AgentJob, error)
	BridgeOwnerReader
}

// LoadLeasedJob is the ONE admission of work on behalf of a running job
// (an agent dispatch, a team plan, a content request under a job grant):
// job jobID of the ctx tenant, in roomID, live and leased by a bridge of
// caller. It returns ErrJobNotInRoom, ErrJobNotLive, ErrJobNotCallers or a
// store error (with the job when it was read); LeasedJobStatus maps them.
func LoadLeasedJob(ctx context.Context, r LeasedJobReader, jobID, roomID, caller string) (AgentJob, error) {
	job, err := r.GetAgentRun(ctx, jobID)
	if errors.Is(err, ErrAgentJobNotFound) || (err == nil && job.RoomID != roomID) {
		return AgentJob{}, ErrJobNotInRoom
	}
	if err != nil {
		return AgentJob{}, err
	}
	return job, jobLeasedBy(ctx, r, job, caller)
}

// LeasedJobStatus is the HTTP status of a LoadLeasedJob error, the same at
// every caller: 404 not in the room, 409 not running, 403 running on another
// member's bridge, 500 for a store failure (which the caller logs).
func LeasedJobStatus(err error) int {
	switch {
	case errors.Is(err, ErrJobNotInRoom):
		return http.StatusNotFound
	case errors.Is(err, ErrJobNotLive):
		return http.StatusConflict
	case errors.Is(err, ErrJobNotCallers):
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}
