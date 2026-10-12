package store

import (
	"context"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Agent dispatch (room tools, docs/prd/2026-10-12-experts-and-teams.md §7):
// an agent running a job asks another agent of the room to do part of the
// work. The dispatched job is an ordinary agent job whose
// DispatchedByJobID/DispatchDepth name its parent; the dispatch guards read
// the parent chain and a parent's dispatches through this read side.

// MaxDispatchedJobsListed bounds one parent's dispatch listing. The fan-out
// guard refuses far below it, so a full listing is never truncated in
// practice; the bound only keeps the read finite.
const MaxDispatchedJobsListed = 64

// AgentDispatchReader is the bridge-job store's dispatch read side.
type AgentDispatchReader interface {
	// ListDispatchedJobs returns the ctx tenant's jobs dispatched by
	// parentJobID, oldest first (at most MaxDispatchedJobsListed).
	ListDispatchedJobs(ctx context.Context, parentJobID string) ([]AgentJob, error)
	// BridgeOwner is BridgeJobStore.BridgeOwner: a bridge id is globally
	// unique and bound to its first owner, so the lookup is cross-tenant,
	// like the claim: a multi-tenant member's bridge heartbeats in one tenant
	// and leases jobs of every tenant they are in.
	BridgeOwner(ctx context.Context, bridgeID string) (string, error)
}

var (
	_ AgentDispatchReader = (*MemoryBridgeJobs)(nil)
	_ AgentDispatchReader = (*MySQLBridgeJobs)(nil)
)

// ListDispatchedJobs implements AgentDispatchReader for the memory store.
func (s *MemoryBridgeJobs) ListDispatchedJobs(ctx context.Context, parentJobID string) ([]AgentJob, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []AgentJob{}
	for _, j := range s.jobs {
		if j.TenantID == tid && parentJobID != "" && j.DispatchedByJobID == parentJobID {
			out = append(out, j)
		}
	}
	sortJobsOldestFirst(out)
	if len(out) > MaxDispatchedJobsListed {
		out = out[:MaxDispatchedJobsListed]
	}
	return out, nil
}

// BridgeOwner implements AgentDispatchReader for the memory store.
func (s *MemoryBridgeJobs) BridgeOwner(_ context.Context, bridgeID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hb, ok := s.heartbeats[bridgeID]
	if !ok || bridgeID == "" {
		return "", ErrBridgeNotFound
	}
	return hb.OwnerUserID, nil
}

// ListDispatchedJobs implements AgentDispatchReader on
// idx_agent_jobs_dispatched_by.
func (m *MySQLBridgeJobs) ListDispatchedJobs(ctx context.Context, parentJobID string) ([]AgentJob, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	if parentJobID == "" {
		return []AgentJob{}, nil
	}
	return agentJobList(ctx, m.DB, `SELECT `+agentJobColumns+` FROM agent_jobs
WHERE tenant_id=? AND dispatched_by_job_id=? ORDER BY created_at_ms ASC, job_id ASC LIMIT ?`, tid, parentJobID, MaxDispatchedJobsListed)
}

// BridgeOwner implements AgentDispatchReader (bridge_heartbeats primary key).
func (m *MySQLBridgeJobs) BridgeOwner(ctx context.Context, bridgeID string) (string, error) {
	hb, found, err := bridgeHeartbeat(ctx, m.DB, bridgeID)
	if err == nil && !found {
		err = ErrBridgeNotFound
	}
	return hb.OwnerUserID, err
}
