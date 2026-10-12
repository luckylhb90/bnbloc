package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// The room expert a turn wore (docs/prd/2026-10-12-experts-and-teams.md,
// "Runtime gap closure" 2). A room's expert is resolved when the bridge
// running a job asks what the turn wears (the gateway's effective
// resolution), not when the chat is posted, so it is recorded on the job
// there: the job and its run record then name it beside the chat's expert.

// JobExpert is an expert at the version a job wore.
type JobExpert struct {
	ID      string
	Version string
	Name    string
}

// RoomExpertRecorder records the room expert a job's turn wore.
type RoomExpertRecorder interface {
	// RecordJobRoomExpert sets the ctx tenant's job's room expert and
	// reports whether it changed (a retried resolution of the same turn
	// records nothing new). ErrAgentJobNotFound for an unknown job.
	RecordJobRoomExpert(ctx context.Context, jobID string, e JobExpert) (bool, error)
}

var (
	_ RoomExpertRecorder = (*MemoryBridgeJobs)(nil)
	_ RoomExpertRecorder = (*MySQLBridgeJobs)(nil)
)

// RecordJobRoomExpert implements RoomExpertRecorder for the memory store.
func (s *MemoryBridgeJobs) RecordJobRoomExpert(ctx context.Context, jobID string, e JobExpert) (bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[jobID]
	if !ok || j.TenantID != tid {
		return false, ErrAgentJobNotFound
	}
	if j.RoomExpertID == e.ID && j.RoomExpertVersion == e.Version && j.RoomExpertName == e.Name {
		return false, nil
	}
	j.RoomExpertID, j.RoomExpertVersion, j.RoomExpertName = e.ID, e.Version, e.Name
	s.jobs[jobID] = j
	return true, nil
}

// RecordJobRoomExpert implements RoomExpertRecorder for MySQL: one
// conditional UPDATE, so "changed" is its affected row count.
func (s *MySQLBridgeJobs) RecordJobRoomExpert(ctx context.Context, jobID string, e JobExpert) (bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return false, err
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE agent_jobs SET room_expert_id = ?, room_expert_version = ?, room_expert_name = ?
WHERE tenant_id = ? AND job_id = ? AND (room_expert_id <> ? OR room_expert_version <> ? OR room_expert_name <> ?)`,
		e.ID, e.Version, e.Name, tid, jobID, e.ID, e.Version, e.Name)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	var one int
	switch err := s.DB.QueryRowContext(ctx, `SELECT 1 FROM agent_jobs WHERE tenant_id = ? AND job_id = ?`, tid, jobID).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return false, ErrAgentJobNotFound
	case err != nil:
		return false, err
	}
	return false, nil
}
