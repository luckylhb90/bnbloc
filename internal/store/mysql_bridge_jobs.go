package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

type MySQLBridgeJobs struct{ DB *sql.DB }

func NewMySQLBridgeJobs(db *sql.DB) *MySQLBridgeJobs { return &MySQLBridgeJobs{DB: db} }

var _ BridgeJobStore = (*MySQLBridgeJobs)(nil)

func (m *MySQLBridgeJobs) PutAgentJob(ctx context.Context, j AgentJob) (AgentJob, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AgentJob{}, err
	}
	j.TenantID = tid
	if j.Status == "" {
		j.Status = AgentJobQueued
	}
	// See MemoryBridgeJobs.PutAgentJob: queue timeouts are computed from
	// CreatedAtMs, so a missing value must become server time here rather than
	// persisting an epoch timestamp that is instantly stale.
	if j.CreatedAtMs <= 0 {
		j.CreatedAtMs = time.Now().UnixMilli()
	}
	if j.UpdatedAtMs <= 0 {
		j.UpdatedAtMs = j.CreatedAtMs
	}
	if _, err := m.DB.ExecContext(ctx, `
INSERT INTO agent_jobs (job_id, tenant_id, room_id, channel_id, message_seq, client_msg_id, requester_user_id, owner_user_id, core, provider, target_id, message_text, attachment_ids, model, reasoning_effort, status, failure_reason, attempt, lease_bridge_id, lease_expires_at_ms, created_at_ms, updated_at_ms, started_at_ms, finished_at_ms, diagnostics_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE job_id = job_id`, j.JobID, tid, j.RoomID, j.ChannelID, j.MessageSeq, j.ClientMsgID, j.RequesterUserID, j.OwnerUserID, j.Core, j.Provider, j.TargetID, j.MessageText, jsonStringSlice(j.AttachmentIDs), j.Model, j.ReasoningEffort, string(j.Status), string(j.FailureReason), j.Attempt, j.LeaseBridgeID, j.LeaseExpiresAtMs, j.CreatedAtMs, j.UpdatedAtMs, j.StartedAtMs, j.FinishedAtMs, nullJSON(j.DiagnosticsJSON)); err != nil {
		return AgentJob{}, err
	}
	return m.agentJobByOrigin(ctx, tid, j)
}

func (m *MySQLBridgeJobs) agentJobByOrigin(ctx context.Context, tid string, j AgentJob) (AgentJob, error) {
	return m.scanAgentJob(ctx, `
SELECT `+agentJobColumns+`
FROM agent_jobs WHERE tenant_id=? AND room_id=? AND message_seq=? AND owner_user_id=? AND core=? AND provider=?`, tid, j.RoomID, j.MessageSeq, j.OwnerUserID, j.Core, j.Provider)
}

func (m *MySQLBridgeJobs) ListAgentJobs(ctx context.Context, roomID, channelID string, limit int) []AgentJob {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT ` + agentJobColumns + ` FROM agent_jobs WHERE tenant_id=? AND room_id=?`
	args := []any{tid, roomID}
	if channelID != "" {
		q += ` AND channel_id=?`
		args = append(args, channelID)
	}
	q += ` ORDER BY created_at_ms DESC LIMIT ?`
	args = append(args, limit)
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

func (m *MySQLBridgeJobs) ClaimAgentJobs(ctx context.Context, req ClaimRequest) ([]AgentJob, error) {
	if req.MaxJobs <= 0 {
		req.MaxJobs = 1
	}
	if req.LeaseMs <= 0 {
		req.LeaseMs = DefaultLeaseMs
	}
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// Claim is CROSS-TENANT by design: a job lives in its ROOM's tenant, but the
	// bridge that must drive it advertises/heartbeats in the tenant its owner's
	// session logged into (which may differ for a multi-tenant owner). bridge_id
	// (heartbeats PK) and (bridge_id,core,provider,target_id) (agents PK) are
	// globally unique, so the join needs no tenant coupling. Authorization is
	// bridge OWNERSHIP + LIVENESS, not tenant: the owner clause + the live
	// bridge_heartbeats join gate who may claim what. j.tenant_id is carried on
	// the returned job so the bridge drives each turn in that job's tenant.
	//
	// The bridge must belong to the claiming owner (h.owner_user_id) and the
	// capability is the owner's own advertisement (a.owner_user_id). Normally
	// only the owner's jobs match; ServeAllOwners (single-host testing, gated
	// server-side by the activity service) lets the owner's advertised
	// core/provider drive every member's job — the same matching the memory
	// store applies.
	liveSince := req.NowMs - BridgeOfflineAfterMs
	ownerClause := " AND j.owner_user_id = ?"
	args := []any{req.OwnerUserID, liveSince, req.OwnerUserID, req.BridgeID, req.OwnerUserID, req.NowMs, req.MaxJobs}
	if req.ServeAllOwners {
		ownerClause = ""
		args = []any{req.OwnerUserID, liveSince, req.OwnerUserID, req.BridgeID, req.NowMs, req.MaxJobs}
	}
	q := `SELECT j.job_id FROM agent_jobs j
JOIN bridge_agents a ON a.owner_user_id=? AND a.core=j.core AND a.provider=j.provider AND a.disabled=0
JOIN bridge_heartbeats h ON h.bridge_id=a.bridge_id AND h.status='online' AND h.last_seen_at_ms >= ? AND h.owner_user_id=?
WHERE a.bridge_id=?` + ownerClause + ` AND (j.status='queued' OR (j.status IN ('claimed','running','reflowing') AND j.lease_expires_at_ms < ?)) ORDER BY j.created_at_ms ASC LIMIT ? FOR UPDATE`
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	var out []AgentJob
	for _, id := range ids {
		res, err := tx.ExecContext(ctx, `UPDATE agent_jobs SET status='claimed', lease_bridge_id=?, lease_expires_at_ms=?, attempt=attempt+1, updated_at_ms=?, claimed_at_ms=? WHERE job_id=? AND (status='queued' OR (status IN ('claimed','running','reflowing') AND lease_expires_at_ms < ?))`, req.BridgeID, req.NowMs+req.LeaseMs, req.NowMs, req.NowMs, id, req.NowMs)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		j, err := scanAgentJobTxByID(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, tx.Commit()
}

// UpdateAgentJobStatus moves a job the caller's bridge still holds. The WHERE
// clause is the concurrency guard: a stale worker whose lease was taken over,
// and any report against an already-terminal job, affects zero rows and reports
// ErrAgentJobLeaseMismatch instead of clobbering the recorded outcome.
func (m *MySQLBridgeJobs) UpdateAgentJobStatus(ctx context.Context, upd JobStatusUpdate) (AgentJob, error) {
	if upd.LeaseMs <= 0 {
		upd.LeaseMs = DefaultLeaseMs
	}
	// Bridge-driven job mutations key on job_id (the PK) and are authorized by
	// lease_bridge_id (only the bridge currently holding the lease may report),
	// NOT by tenant: the driving bridge's session tenant may differ from the
	// job's (room's) tenant for a multi-tenant owner. The lease guard is the real
	// concurrency + authorization control.
	if upd.Status == AgentJobFailed && upd.FailureReason.Retryable() {
		res, err := m.DB.ExecContext(ctx, `UPDATE agent_jobs SET status='queued', failure_reason=?, diagnostics_json=?, updated_at_ms=?, lease_bridge_id='', lease_expires_at_ms=0 WHERE job_id=? AND lease_bridge_id=? AND status NOT IN ('replied','failed','timed_out','cancelled') AND attempt < ?`,
			string(upd.FailureReason), nullJSON(upd.DiagnosticsJSON), upd.NowMs, upd.JobID, upd.BridgeID, maxAttempts(upd.MaxAttempts))
		if err != nil {
			return AgentJob{}, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return m.scanAgentJob(ctx, `
SELECT `+agentJobColumns+`
FROM agent_jobs WHERE job_id=?`, upd.JobID)
		}
		// Attempt budget spent: fall through and record the terminal failure.
	}
	status := string(upd.Status)
	res, err := m.DB.ExecContext(ctx, `UPDATE agent_jobs
SET status=?,
    failure_reason=?,
    diagnostics_json=?,
    updated_at_ms=?,
    started_at_ms=IF(?='running' AND started_at_ms=0, ?, started_at_ms),
    finished_at_ms=IF(? IN ('replied','failed','timed_out','cancelled'), ?, finished_at_ms),
    lease_expires_at_ms=IF(? IN ('replied','failed','timed_out','cancelled'), 0, ?)
WHERE job_id=? AND lease_bridge_id=? AND status NOT IN ('replied','failed','timed_out','cancelled')`,
		status, string(upd.FailureReason), nullJSON(upd.DiagnosticsJSON), upd.NowMs,
		status, upd.NowMs,
		status, upd.NowMs,
		status, upd.NowMs+upd.LeaseMs,
		upd.JobID, upd.BridgeID)
	if err != nil {
		return AgentJob{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return AgentJob{}, ErrAgentJobLeaseMismatch
	}
	return m.scanAgentJob(ctx, `
SELECT `+agentJobColumns+`
FROM agent_jobs WHERE job_id=?`, upd.JobID)
}

func (m *MySQLBridgeJobs) RenewAgentJobLease(ctx context.Context, ren LeaseRenewal) error {
	if ren.LeaseMs <= 0 {
		ren.LeaseMs = DefaultLeaseMs
	}
	// Keyed on job_id (PK) + lease ownership, not tenant (see UpdateAgentJobStatus).
	res, err := m.DB.ExecContext(ctx, `UPDATE agent_jobs SET lease_expires_at_ms=?, updated_at_ms=? WHERE job_id=? AND lease_bridge_id=? AND status NOT IN ('replied','failed','timed_out','cancelled')`, ren.NowMs+ren.LeaseMs, ren.NowMs, ren.JobID, ren.BridgeID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAgentJobLeaseMismatch
	}
	return nil
}

// CancelAgentJob moves a live job to cancelled. The status guard makes it safe
// under a racing worker report: whichever lands first wins and the other is
// rejected, so a cancelled job is never reopened and a replied job is never
// retroactively cancelled. Clearing the lease stops any further worker report.
func (m *MySQLBridgeJobs) CancelAgentJob(ctx context.Context, req CancelRequest) (AgentJob, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AgentJob{}, err
	}
	q := `UPDATE agent_jobs SET status='cancelled', failure_reason=?, updated_at_ms=?, finished_at_ms=?, lease_expires_at_ms=0, lease_bridge_id='' WHERE tenant_id=? AND job_id=? AND status NOT IN ('replied','failed','timed_out','cancelled')`
	args := []any{string(FailureCancelled), req.NowMs, req.NowMs, tid, req.JobID}
	if req.RoomID != "" {
		q += ` AND room_id=?`
		args = append(args, req.RoomID)
	}
	res, err := m.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return AgentJob{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return AgentJob{}, ErrAgentJobLeaseMismatch
	}
	return m.scanAgentJob(ctx, `
SELECT `+agentJobColumns+`
FROM agent_jobs WHERE tenant_id=? AND job_id=?`, tid, req.JobID)
}

// SweepAgentJobs fails jobs no worker can finish. It runs as cross-tenant
// maintenance, so it carries no tenant scope and returns the affected jobs for
// per-room notification.
func (m *MySQLBridgeJobs) SweepAgentJobs(ctx context.Context, req SweepRequest) ([]AgentJob, error) {
	if req.QueuedTimeoutMs <= 0 {
		req.QueuedTimeoutMs = DefaultQueuedTimeoutMs
	}
	if req.MaxAttempts <= 0 {
		req.MaxAttempts = DefaultMaxJobAttempts
	}
	rows, err := m.DB.QueryContext(ctx, `SELECT job_id, tenant_id, IF(status='queued', ?, ?) FROM agent_jobs WHERE status NOT IN ('replied','failed','timed_out','cancelled') AND ((status='queued' AND created_at_ms + ? <= ?) OR (status<>'queued' AND lease_expires_at_ms < ? AND attempt >= ?)) ORDER BY created_at_ms ASC LIMIT 200`,
		string(FailureNoBridgeOnline), string(FailureTurnTimeout), req.QueuedTimeoutMs, req.NowMs, req.NowMs, req.MaxAttempts)
	if err != nil {
		return nil, err
	}
	type candidate struct{ id, tid, reason string }
	var cands []candidate
	for rows.Next() {
		var c candidate
		if rows.Scan(&c.id, &c.tid, &c.reason) == nil {
			cands = append(cands, c)
		}
	}
	rows.Close()
	var out []AgentJob
	for _, c := range cands {
		res, err := m.DB.ExecContext(ctx, `UPDATE agent_jobs SET status='failed', failure_reason=?, updated_at_ms=?, finished_at_ms=?, lease_expires_at_ms=0 WHERE tenant_id=? AND job_id=? AND status NOT IN ('replied','failed','timed_out','cancelled')`, c.reason, req.NowMs, req.NowMs, c.tid, c.id)
		if err != nil {
			return out, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		j, err := m.scanAgentJob(ctx, `
SELECT `+agentJobColumns+`
FROM agent_jobs WHERE tenant_id=? AND job_id=?`, c.tid, c.id)
		if err != nil {
			continue
		}
		out = append(out, j)
	}
	return out, nil
}

func (m *MySQLBridgeJobs) LookupBridge(ctx context.Context, bridgeID string) (BridgeHeartbeat, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return BridgeHeartbeat{}, err
	}
	var hb BridgeHeartbeat
	var status string
	var raw []byte
	err = m.DB.QueryRowContext(ctx, `SELECT bridge_id, tenant_id, owner_user_id, app_instance_id, version, status, serve_all_owners, last_seen_at_ms, metadata_json FROM bridge_heartbeats WHERE tenant_id=? AND bridge_id=?`, tid, bridgeID).
		Scan(&hb.BridgeID, &hb.TenantID, &hb.OwnerUserID, &hb.AppInstanceID, &hb.Version, &status, &hb.ServeAllOwners, &hb.LastSeenAtMs, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return BridgeHeartbeat{}, ErrBridgeNotFound
	}
	if err != nil {
		return BridgeHeartbeat{}, err
	}
	hb.Status = BridgeStatus(status)
	if len(raw) > 0 {
		hb.MetadataJSON = json.RawMessage(raw)
	}
	return hb, nil
}

func (m *MySQLBridgeJobs) PutBridgeHeartbeat(ctx context.Context, hb BridgeHeartbeat) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	hb.TenantID = tid
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// A bridge id is bound to its first owner: lock the row and refuse a
	// different owner instead of letting the upsert re-own the bridge.
	var prevOwner string
	switch err := tx.QueryRowContext(ctx, `SELECT owner_user_id FROM bridge_heartbeats WHERE bridge_id=? FOR UPDATE`, hb.BridgeID).Scan(&prevOwner); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	case prevOwner != hb.OwnerUserID:
		return ErrBridgeOwnerMismatch
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO bridge_heartbeats (bridge_id, tenant_id, owner_user_id, app_instance_id, version, status, serve_all_owners, last_seen_at_ms, metadata_json) VALUES (?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE tenant_id=VALUES(tenant_id), app_instance_id=VALUES(app_instance_id), version=VALUES(version), status=VALUES(status), serve_all_owners=VALUES(serve_all_owners), last_seen_at_ms=VALUES(last_seen_at_ms), metadata_json=VALUES(metadata_json)`, hb.BridgeID, tid, hb.OwnerUserID, hb.AppInstanceID, hb.Version, string(hb.Status), hb.ServeAllOwners, hb.LastSeenAtMs, nullJSON(hb.MetadataJSON)); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *MySQLBridgeJobs) ReplaceBridgeAgents(ctx context.Context, bridgeID string, agents []BridgeAgent) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM bridge_agents WHERE bridge_id=?`, bridgeID); err != nil {
		return err
	}
	for _, a := range agents {
		// A bridge advertises several distinct targets under one core/provider
		// (codex, claude-code, opencode all run on one core), so target_id is part
		// of the identity. ON DUPLICATE KEY keeps a re-advertised target a refresh
		// rather than a failed heartbeat.
		if _, err := tx.ExecContext(ctx, `INSERT INTO bridge_agents (bridge_id, tenant_id, owner_user_id, core, provider, target_id, model_options_json, disabled, last_seen_at_ms) VALUES (?,?,?,?,?,?,?,?,?)
ON DUPLICATE KEY UPDATE owner_user_id=VALUES(owner_user_id), model_options_json=VALUES(model_options_json), disabled=VALUES(disabled), last_seen_at_ms=VALUES(last_seen_at_ms)`, bridgeID, tid, a.OwnerUserID, a.Core, a.Provider, a.TargetID, nullJSON(a.ModelOptionsJSON), a.Disabled, a.LastSeenAtMs); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListBridgeAgents returns capabilities advertised by bridges that are still
// alive. Advertisement rows outlive the bridge process, so serving them would
// offer members an agent nobody can drive and the job would then sit until the
// queue timeout.
//
// The listing is deliberately NOT tenant-filtered: an agent belongs to its
// OWNER's bridge (bridge_id, owner_user_id — both globally unique), and the
// same user's bridge serves their agents in every room they are a member of,
// whichever tenant that room (or the user's login session) lands in. The
// roster join with room membership remains the visibility boundary: a member
// sees exactly the agents advertised in rooms they are in. liveness is keyed
// by bridge_id only.
func (m *MySQLBridgeJobs) ListBridgeAgents(ctx context.Context, _ string) []BridgeAgent {
	if _, err := tenant.Require(ctx); err != nil {
		return nil
	}
	rows, err := m.DB.QueryContext(ctx, `SELECT a.bridge_id, a.tenant_id, a.owner_user_id, a.core, a.provider, a.target_id, a.model_options_json, a.disabled, a.last_seen_at_ms
FROM bridge_agents a
JOIN bridge_heartbeats h ON h.bridge_id=a.bridge_id AND h.status='online' AND h.last_seen_at_ms >= ?
WHERE a.disabled=0 ORDER BY a.owner_user_id, a.core, a.provider`, time.Now().UnixMilli()-BridgeOfflineAfterMs)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []BridgeAgent
	for rows.Next() {
		var a BridgeAgent
		var raw []byte
		if rows.Scan(&a.BridgeID, &a.TenantID, &a.OwnerUserID, &a.Core, &a.Provider, &a.TargetID, &raw, &a.Disabled, &a.LastSeenAtMs) == nil {
			a.ModelOptionsJSON = json.RawMessage(raw)
			out = append(out, a)
		}
	}
	return out
}

func (m *MySQLBridgeJobs) scanAgentJob(ctx context.Context, q string, args ...any) (AgentJob, error) {
	return scanAgentJobRow(m.DB.QueryRowContext(ctx, q, args...))
}
func scanAgentJobTx(ctx context.Context, tx *sql.Tx, tid, id string) (AgentJob, error) {
	return scanAgentJobRow(tx.QueryRowContext(ctx, `SELECT `+agentJobColumns+` FROM agent_jobs WHERE tenant_id=? AND job_id=?`, tid, id))
}

// scanAgentJobTxByID reads a job by its primary key alone (job_id is globally
// unique), for the cross-tenant claim path where the claiming bridge's session
// tenant may differ from the job's (room's) tenant.
func scanAgentJobTxByID(ctx context.Context, tx *sql.Tx, id string) (AgentJob, error) {
	return scanAgentJobRow(tx.QueryRowContext(ctx, `SELECT `+agentJobColumns+` FROM agent_jobs WHERE job_id=?`, id))
}

// agentJobColumns is the one column list every agent_jobs read selects, in
// scanAgentJobRow's order.
const agentJobColumns = `job_id, tenant_id, room_id, channel_id, message_seq, client_msg_id, requester_user_id, owner_user_id, core, provider, target_id, message_text, attachment_ids, model, reasoning_effort, status, failure_reason, attempt, lease_bridge_id, lease_expires_at_ms, created_at_ms, updated_at_ms, started_at_ms, finished_at_ms, diagnostics_json, claimed_at_ms`

type agentJobScanner interface{ Scan(dest ...any) error }

type rowsScanner struct{ *sql.Rows }

func scanAgentJobRows(rows *sql.Rows) (AgentJob, error) { return scanAgentJobRow(rowsScanner{rows}) }
func scanAgentJobRow(row agentJobScanner) (AgentJob, error) {
	var j AgentJob
	var status, reason string
	var messageText, model, reasoningEffort sql.NullString
	var attachmentIDs []byte
	var diag []byte
	err := row.Scan(&j.JobID, &j.TenantID, &j.RoomID, &j.ChannelID, &j.MessageSeq, &j.ClientMsgID, &j.RequesterUserID, &j.OwnerUserID, &j.Core, &j.Provider, &j.TargetID, &messageText, &attachmentIDs, &model, &reasoningEffort, &status, &reason, &j.Attempt, &j.LeaseBridgeID, &j.LeaseExpiresAtMs, &j.CreatedAtMs, &j.UpdatedAtMs, &j.StartedAtMs, &j.FinishedAtMs, &diag, &j.ClaimedAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentJob{}, ErrAgentJobNotFound
	}
	if err != nil {
		return AgentJob{}, err
	}
	j.Status = AgentJobStatus(status)
	j.FailureReason = AgentJobFailureReason(reason)
	j.MessageText = messageText.String
	j.AttachmentIDs = parseJSONStringSlice(attachmentIDs)
	j.Model = model.String
	j.ReasoningEffort = reasoningEffort.String
	if len(diag) > 0 {
		j.DiagnosticsJSON = json.RawMessage(diag)
	}
	return j, nil
}

func jsonStringSlice(ids []string) any {
	if len(ids) == 0 {
		return nil
	}
	raw, err := jsonArg(ids)
	if err != nil {
		return nil
	}
	return raw
}

func parseJSONStringSlice(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil
	}
	return ids
}

func nullJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw // JSON text, never []byte (see jsonArg)
}

// ListBridgeAgentsForOwner returns the owner's advertised agents across all
// rooms and tenants, joined to a live heartbeat on bridge_id (the globally
// unique PK). The owner identity is global, so no tenant filter applies —
// a member's own agents are their own agents wherever their bridge runs.
func (m *MySQLBridgeJobs) ListBridgeAgentsForOwner(ctx context.Context, ownerUserID string) []BridgeAgent {
	rows, err := m.DB.QueryContext(ctx, `SELECT a.bridge_id, a.tenant_id, a.owner_user_id, a.core, a.provider, a.target_id, a.model_options_json, a.disabled, a.last_seen_at_ms
FROM bridge_agents a
JOIN bridge_heartbeats h ON h.bridge_id=a.bridge_id AND h.status='online' AND h.last_seen_at_ms >= ?
WHERE a.owner_user_id=? AND a.disabled=0
ORDER BY a.core, a.provider`, time.Now().UnixMilli()-BridgeOfflineAfterMs, ownerUserID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []BridgeAgent
	for rows.Next() {
		var a BridgeAgent
		var raw []byte
		if rows.Scan(&a.BridgeID, &a.TenantID, &a.OwnerUserID, &a.Core, &a.Provider, &a.TargetID, &raw, &a.Disabled, &a.LastSeenAtMs) == nil {
			a.ModelOptionsJSON = json.RawMessage(raw)
			out = append(out, a)
		}
	}
	return out
}

// PendingAgentJobs counts the owner's not-yet-started jobs across tenants
// (see BridgeJobStore), on idx_agent_jobs_owner_status.
func (m *MySQLBridgeJobs) PendingAgentJobs(ctx context.Context, ownerUserID string) ([]AgentJobCount, error) {
	rows, err := m.DB.QueryContext(ctx, `SELECT core, provider, COUNT(*) FROM agent_jobs
WHERE owner_user_id=? AND status IN ('queued','claimed')
GROUP BY core, provider ORDER BY core, provider`, ownerUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentJobCount
	for rows.Next() {
		var c AgentJobCount
		if err := rows.Scan(&c.Core, &c.Provider, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
