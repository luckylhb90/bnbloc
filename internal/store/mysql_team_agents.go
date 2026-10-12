package store

import (
	"context"
	"database/sql"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

var (
	_ TeamAgentReader  = (*MySQLBridgeJobs)(nil)
	_ AgentUsageReader = (*MySQL)(nil)
)

// TenantTeamAgents implements TeamAgentReader: the tenant's advertised agents
// LEFT JOINed to their bridge heartbeat, so an agent whose bridge went away is
// still listed (offline, last seen at its final heartbeat).
func (m *MySQLBridgeJobs) TenantTeamAgents(ctx context.Context, ownerUserID string, nowMs int64) ([]TeamAgent, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT a.owner_user_id, a.core, a.provider, a.target_id,
       COALESCE(h.last_seen_at_ms, a.last_seen_at_ms),
       COALESCE(` + liveBridgePred + `, 0)
FROM bridge_agents a
LEFT JOIN bridge_heartbeats h ON h.bridge_id = a.bridge_id
WHERE a.tenant_id = ? AND a.disabled = 0`
	args := []any{bridgeLiveSince(nowMs), tid}
	if ownerUserID != "" {
		q += ` AND a.owner_user_id = ?`
		args = append(args, ownerUserID)
	}
	out, err := queryAll(ctx, m.DB, q+` ORDER BY `+bridgeAgentOrder, func(r rowScanner) (TeamAgent, error) {
		var a TeamAgent
		err := r.Scan(&a.OwnerUserID, &a.Core, &a.Provider, &a.TargetID, &a.LastSeenAtMs, &a.Online)
		return a, err
	}, args...)
	if out == nil && err == nil {
		out = []TeamAgent{}
	}
	return out, err
}

// TenantTeamJobs implements TeamAgentReader as two indexed reads, not one OR
// that defeats both indexes: the jobs created at/after sinceMs
// (idx_agent_jobs_tenant_created, or idx_agent_jobs_owner_created for one
// owner), then, while the page has room, the still-active jobs created
// before it (idx_agent_jobs_status_created: live jobs are few). Every row of
// the first read is newer than every row of the second, so appending keeps
// the newest-first order.
func (m *MySQLBridgeJobs) TenantTeamJobs(ctx context.Context, ownerUserID string, sinceMs int64) ([]AgentJob, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	owner, ownerArgs := "", []any(nil)
	if ownerUserID != "" {
		owner, ownerArgs = ` AND owner_user_id = ?`, []any{ownerUserID}
	}
	read := func(where string, args []any, limit int) ([]AgentJob, error) {
		args = append(append(args, ownerArgs...), limit)
		return agentJobList(ctx, m.DB, `SELECT `+agentJobColumns+`
FROM agent_jobs WHERE `+where+owner+` ORDER BY created_at_ms DESC, job_id DESC LIMIT ?`, args...)
	}
	out, err := read(`tenant_id = ? AND created_at_ms >= ?`, []any{tid, sinceMs}, MaxTeamJobs)
	if err != nil {
		return nil, err
	}
	if room := MaxTeamJobs - len(out); room > 0 {
		active, err := read(`status IN (`+sqlActiveJobStatuses+`) AND tenant_id = ? AND created_at_ms < ?`, []any{tid, sinceMs}, room)
		if err != nil {
			return nil, err
		}
		out = append(out, active...)
	}
	return out, nil
}

// AgentUsage implements AgentUsageReader: sums the usage columns AppendMessages
// stored for each reported reply (ReplyUsageOf, normalized once at append) per
// owner/provider, over idx_agent_messages_tenant_time. Replies that reported
// no usage have usage_reported = 0 and do not count.
func (m *MySQL) AgentUsage(ctx context.Context, ownerUserID string, sinceMs int64) ([]AgentTurnUsage, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT usage_owner_user_id, usage_provider, COUNT(*), COALESCE(SUM(usage_input_tokens), 0),
  COALESCE(SUM(usage_output_tokens), 0), COALESCE(SUM(usage_cost_usd), 0), COUNT(usage_cost_usd)
FROM agent_messages
WHERE tenant_id = ? AND created_at_ms >= ? AND usage_reported = 1`
	args := []any{tnt, sinceMs}
	if ownerUserID != "" {
		q += ` AND usage_owner_user_id = ?`
		args = append(args, ownerUserID)
	}
	q += ` GROUP BY usage_owner_user_id, usage_provider ORDER BY usage_owner_user_id, usage_provider`
	out, err := queryAll(ctx, m.DB, q, func(r rowScanner) (AgentTurnUsage, error) {
		var u AgentTurnUsage
		var cost sql.NullFloat64
		err := r.Scan(&u.OwnerUserID, &u.Provider, &u.ReportedTurns, &u.InputTokens, &u.OutputTokens, &cost, &u.CostReportedBy)
		u.CostUsd = cost.Float64
		return u, err
	}, args...)
	if out == nil && err == nil {
		out = []AgentTurnUsage{}
	}
	return out, err
}
