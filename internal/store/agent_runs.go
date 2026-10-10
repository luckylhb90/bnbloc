package store

import (
	"context"
	"sort"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Agent run records (R10): a paged, filtered read side over agent_jobs. A run
// IS a job — the record adds no table of its own; the phases, outcome, and the
// reply/usage summary the bridge reports in the job's diagnostics are all on
// the job row.

// AgentRunCursor pages runs older than (CreatedAtMs, JobID) in the
// newest-first order (created_at_ms DESC, job_id DESC).
type AgentRunCursor struct {
	CreatedAtMs int64
	JobID       string
}

// MaxAgentRunsPage bounds one run listing page.
const MaxAgentRunsPage = 100

// AgentRunQuery selects runs in the ctx tenant. Every non-zero field narrows
// the result; the store never widens the caller's scope.
type AgentRunQuery struct {
	// RoomID limits to one room.
	RoomID string
	// Participant limits to runs the user took part in: as the addressed
	// agent's owner OR as the requester.
	Participant string
	// Owner / Core / Provider select one agent (or one owner's agents).
	Owner    string
	Core     string
	Provider string
	// Statuses limits to these statuses (empty = all).
	Statuses []AgentJobStatus
	// FromMs / ToMs bound created_at_ms to [FromMs, ToMs) (0 = open).
	FromMs int64
	ToMs   int64
	Before AgentRunCursor
	// Limit is clamped to 1..MaxAgentRunsPage (0 = MaxAgentRunsPage/2).
	Limit int
}

// AgentRunReader is the bridge-job store's run-record read side.
type AgentRunReader interface {
	// ListAgentRuns returns the ctx tenant's runs matching q, newest first.
	ListAgentRuns(ctx context.Context, q AgentRunQuery) ([]AgentJob, error)
	// GetAgentRun returns one run of the ctx tenant (ErrAgentJobNotFound when
	// absent or in another tenant).
	GetAgentRun(ctx context.Context, jobID string) (AgentJob, error)
}

var _ AgentRunReader = (*MemoryBridgeJobs)(nil)

func (q AgentRunQuery) limit() int {
	switch {
	case q.Limit <= 0:
		return MaxAgentRunsPage / 2
	case q.Limit > MaxAgentRunsPage:
		return MaxAgentRunsPage
	}
	return q.Limit
}

// matches applies q to one job (memory store; the MySQL WHERE mirrors it).
func (q AgentRunQuery) matches(j AgentJob) bool {
	if q.RoomID != "" && j.RoomID != q.RoomID {
		return false
	}
	if q.Participant != "" && !strings.EqualFold(j.OwnerUserID, q.Participant) && !strings.EqualFold(j.RequesterUserID, q.Participant) {
		return false
	}
	if q.Owner != "" && !strings.EqualFold(j.OwnerUserID, q.Owner) {
		return false
	}
	if q.Core != "" && j.Core != q.Core {
		return false
	}
	if q.Provider != "" && j.Provider != q.Provider {
		return false
	}
	if len(q.Statuses) > 0 {
		ok := false
		for _, s := range q.Statuses {
			ok = ok || j.Status == s
		}
		if !ok {
			return false
		}
	}
	if q.FromMs > 0 && j.CreatedAtMs < q.FromMs {
		return false
	}
	if q.ToMs > 0 && j.CreatedAtMs >= q.ToMs {
		return false
	}
	if q.Before.CreatedAtMs > 0 && !runOlder(j, q.Before) {
		return false
	}
	return true
}

// runOlder reports whether j sorts after the cursor in newest-first order.
func runOlder(j AgentJob, c AgentRunCursor) bool {
	if j.CreatedAtMs != c.CreatedAtMs {
		return j.CreatedAtMs < c.CreatedAtMs
	}
	return j.JobID < c.JobID
}

// ListAgentRuns implements AgentRunReader for the memory store.
func (s *MemoryBridgeJobs) ListAgentRuns(ctx context.Context, q AgentRunQuery) ([]AgentJob, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []AgentJob
	for _, j := range s.jobs {
		if j.TenantID == tid && q.matches(j) {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(i, k int) bool {
		return runOlder(out[k], AgentRunCursor{CreatedAtMs: out[i].CreatedAtMs, JobID: out[i].JobID})
	})
	if n := q.limit(); len(out) > n {
		out = out[:n]
	}
	return out, nil
}

// GetAgentRun implements AgentRunReader for the memory store.
func (s *MemoryBridgeJobs) GetAgentRun(ctx context.Context, jobID string) (AgentJob, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AgentJob{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[jobID]
	if !ok || j.TenantID != tid {
		return AgentJob{}, ErrAgentJobNotFound
	}
	return j, nil
}

// AgentRuns returns the bridge-job store's run-record read side (nil when the
// configured store does not provide one).
func (b *Backends) AgentRuns() AgentRunReader {
	r, _ := b.BridgeJobs.(AgentRunReader)
	return r
}
