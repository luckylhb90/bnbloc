package store

import (
	"context"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

var _ AgentRunReader = (*MySQLBridgeJobs)(nil)

// ListAgentRuns implements AgentRunReader over the tenant/room/owner/requester
// created_at_ms indexes (2026_10_b3_r10_agent_run_records). The WHERE clause
// mirrors AgentRunQuery.matches.
func (m *MySQLBridgeJobs) ListAgentRuns(ctx context.Context, q AgentRunQuery) ([]AgentJob, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	where := []string{"tenant_id = ?"}
	args := []any{tid}
	add := func(cond string, vals ...any) {
		where = append(where, cond)
		args = append(args, vals...)
	}
	if q.RoomID != "" {
		add("room_id = ?", q.RoomID)
	}
	if q.Participant != "" {
		add("(owner_user_id = ? OR requester_user_id = ?)", q.Participant, q.Participant)
	}
	if q.Owner != "" {
		add("owner_user_id = ?", q.Owner)
	}
	if q.Core != "" {
		add("core = ?", q.Core)
	}
	if q.Provider != "" {
		add("provider = ?", q.Provider)
	}
	if len(q.Statuses) > 0 {
		marks := make([]string, len(q.Statuses))
		vals := make([]any, len(q.Statuses))
		for i, s := range q.Statuses {
			marks[i], vals[i] = "?", string(s)
		}
		add("status IN ("+strings.Join(marks, ",")+")", vals...)
	}
	if q.FromMs > 0 {
		add("created_at_ms >= ?", q.FromMs)
	}
	if q.ToMs > 0 {
		add("created_at_ms < ?", q.ToMs)
	}
	if q.Before.CreatedAtMs > 0 {
		add("(created_at_ms < ? OR (created_at_ms = ? AND job_id < ?))", q.Before.CreatedAtMs, q.Before.CreatedAtMs, q.Before.JobID)
	}
	args = append(args, q.limit())
	rows, err := m.DB.QueryContext(ctx, `SELECT `+agentJobColumns+` FROM agent_jobs WHERE `+
		strings.Join(where, " AND ")+` ORDER BY created_at_ms DESC, job_id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentJob
	for rows.Next() {
		j, err := scanAgentJobRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// GetAgentRun implements AgentRunReader.
func (m *MySQLBridgeJobs) GetAgentRun(ctx context.Context, jobID string) (AgentJob, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AgentJob{}, err
	}
	return m.scanAgentJob(ctx, `SELECT `+agentJobColumns+` FROM agent_jobs WHERE tenant_id = ? AND job_id = ?`, tid, jobID)
}
