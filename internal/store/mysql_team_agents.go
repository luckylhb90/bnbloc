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
func (m *MySQLBridgeJobs) TenantTeamAgents(ctx context.Context, ownerUserID string, nowMs int64) []TeamAgent {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	q := `SELECT a.owner_user_id, a.core, a.provider, a.target_id,
       COALESCE(h.last_seen_at_ms, a.last_seen_at_ms),
       COALESCE(h.status = 'online' AND h.last_seen_at_ms >= ?, 0)
FROM bridge_agents a
LEFT JOIN bridge_heartbeats h ON h.bridge_id = a.bridge_id
WHERE a.tenant_id = ? AND a.disabled = 0`
	args := []any{nowMs - BridgeOfflineAfterMs, tid}
	if ownerUserID != "" {
		q += ` AND a.owner_user_id = ?`
		args = append(args, ownerUserID)
	}
	rows, err := m.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []TeamAgent
	for rows.Next() {
		var a TeamAgent
		var online bool
		if rows.Scan(&a.OwnerUserID, &a.Core, &a.Provider, &a.TargetID, &a.LastSeenAtMs, &online) == nil {
			a.Online = online
			out = append(out, a)
		}
	}
	return out
}

// TenantTeamJobs implements TeamAgentReader over idx_agent_jobs_tenant_created
// (recent jobs) plus idx_agent_jobs_claim's status prefix (active jobs).
func (m *MySQLBridgeJobs) TenantTeamJobs(ctx context.Context, ownerUserID string, sinceMs int64) []AgentJob {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	q := `SELECT ` + agentJobColumns + `
FROM agent_jobs
WHERE tenant_id = ? AND (created_at_ms >= ? OR status IN ('queued','claimed','running','reflowing'))`
	args := []any{tid, sinceMs}
	if ownerUserID != "" {
		q += ` AND owner_user_id = ?`
		args = append(args, ownerUserID)
	}
	q += ` ORDER BY created_at_ms DESC LIMIT ?`
	args = append(args, MaxTeamJobs)
	rows, err := m.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []AgentJob
	for rows.Next() {
		if j, err := scanAgentJobRows(rows); err == nil {
			out = append(out, j)
		}
	}
	return out
}

// AgentUsage implements AgentUsageReader: sums the reported metrics carried in
// agent_messages.payload_json (the bridge's reply record) per owner/provider,
// over idx_agent_messages_tenant_time. Replies whose metrics say
// reported=false, or carry none, do not count.
func (m *MySQL) AgentUsage(ctx context.Context, ownerUserID string, sinceMs int64) []AgentTurnUsage {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	q := `SELECT o, p, COUNT(*), COALESCE(SUM(inp), 0), COALESCE(SUM(outp), 0), COALESCE(SUM(cost), 0), COUNT(cost)
FROM (
  SELECT COALESCE(JSON_UNQUOTE(JSON_EXTRACT(payload_json, '$.ownerUserId')), '') AS o,
         COALESCE(JSON_UNQUOTE(JSON_EXTRACT(payload_json, '$.provider')), '') AS p,
         CAST(JSON_EXTRACT(payload_json, '$.metrics.inputTokens') AS DECIMAL(20,0)) AS inp,
         CAST(JSON_EXTRACT(payload_json, '$.metrics.outputTokens') AS DECIMAL(20,0)) AS outp,
         CAST(JSON_EXTRACT(payload_json, '$.metrics.costUsd') AS DECIMAL(18,6)) AS cost
  FROM agent_messages
  WHERE tenant_id = ? AND created_at_ms >= ?
    AND JSON_TYPE(JSON_EXTRACT(payload_json, '$.metrics')) = 'OBJECT'
    AND NOT (JSON_EXTRACT(payload_json, '$.metrics.reported') <=> CAST('false' AS JSON))
) t`
	args := []any{tnt, sinceMs}
	if ownerUserID != "" {
		q += ` WHERE o = ?`
		args = append(args, ownerUserID)
	}
	q += ` GROUP BY o, p`
	rows, err := m.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []AgentTurnUsage
	for rows.Next() {
		var u AgentTurnUsage
		var cost sql.NullFloat64
		if rows.Scan(&u.OwnerUserID, &u.Provider, &u.ReportedTurns, &u.InputTokens, &u.OutputTokens, &cost, &u.CostReportedBy) == nil {
			u.CostUsd = cost.Float64
			out = append(out, u)
		}
	}
	return out
}
