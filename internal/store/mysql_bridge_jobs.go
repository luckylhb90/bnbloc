package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

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
	j = j.normalize(tid)
	if _, err := m.DB.ExecContext(ctx, `
INSERT INTO agent_jobs (job_id, tenant_id, room_id, channel_id, message_seq, client_msg_id, requester_user_id, owner_user_id, core, provider, target_id, message_text, attachment_ids, model, reasoning_effort, status, failure_reason, attempt, lease_bridge_id, lease_expires_at_ms, created_at_ms, updated_at_ms, started_at_ms, finished_at_ms, diagnostics_json, dispatched_by_job_id, dispatch_depth, expert_id, expert_version, expert_name)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE job_id = job_id`, j.JobID, tid, j.RoomID, j.ChannelID, j.MessageSeq, j.ClientMsgID, j.RequesterUserID, j.OwnerUserID, j.Core, j.Provider, j.TargetID, j.MessageText, jsonStringSlice(j.AttachmentIDs), j.Model, j.ReasoningEffort, string(j.Status), string(j.FailureReason), j.Attempt, j.LeaseBridgeID, j.LeaseExpiresAtMs, j.CreatedAtMs, j.UpdatedAtMs, j.StartedAtMs, j.FinishedAtMs, nullIfZero(string(j.DiagnosticsJSON)), j.DispatchedByJobID, j.DispatchDepth, j.ExpertID, j.ExpertVersion, j.ExpertName); err != nil {
		return AgentJob{}, err
	}
	// The insert stored the job, or met its origin (the idempotent re-put:
	// read the stored job), or met its id on another origin: the origin is
	// then absent.
	stored, err := m.agentJobByOrigin(ctx, tid, j)
	if errors.Is(err, ErrAgentJobNotFound) {
		return AgentJob{}, ErrAgentJobIDTaken
	}
	return stored, err
}

func (m *MySQLBridgeJobs) agentJobByOrigin(ctx context.Context, tid string, j AgentJob) (AgentJob, error) {
	return m.scanAgentJob(ctx, `
SELECT `+agentJobColumns+`
FROM agent_jobs WHERE tenant_id=? AND room_id=? AND message_seq=? AND owner_user_id=? AND core=? AND provider=?`, tid, j.RoomID, j.MessageSeq, j.OwnerUserID, j.Core, j.Provider)
}

func (m *MySQLBridgeJobs) ListAgentJobs(ctx context.Context, roomID, channelID string, limit int) ([]AgentJob, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + agentJobColumns + ` FROM agent_jobs WHERE tenant_id=? AND room_id=?`
	args := []any{tid, roomID}
	if channelID != "" {
		q += ` AND channel_id=?`
		args = append(args, channelID)
	}
	q += ` ORDER BY created_at_ms DESC, job_id DESC LIMIT ?`
	args = append(args, clampLimit(limit, DefaultAgentJobsListed, MaxAgentJobsListed))
	return agentJobList(ctx, m.DB, q, args...)
}

// agentJobList reads agent_jobs rows (agentJobColumns), an empty list (never
// nil: listings serialize as []) when none match.
func agentJobList(ctx context.Context, db sqlConn, q string, args ...any) ([]AgentJob, error) {
	out, err := queryAll(ctx, db, q, scanAgentJobRow, args...)
	if out == nil && err == nil {
		out = []AgentJob{}
	}
	return out, err
}

// bridgeHeartbeatColumns is the column list scanBridgeHeartbeat reads.
const bridgeHeartbeatColumns = `bridge_id, tenant_id, owner_user_id, app_instance_id, version, status, serve_all_owners, last_seen_at_ms, metadata_json`

// scanBridgeHeartbeat reads one bridgeHeartbeatColumns row.
func scanBridgeHeartbeat(r rowScanner) (BridgeHeartbeat, error) {
	var hb BridgeHeartbeat
	var status string
	var raw []byte
	if err := r.Scan(&hb.BridgeID, &hb.TenantID, &hb.OwnerUserID, &hb.AppInstanceID, &hb.Version, &status, &hb.ServeAllOwners, &hb.LastSeenAtMs, &raw); err != nil {
		return BridgeHeartbeat{}, err
	}
	hb.Status = BridgeStatus(status)
	if len(raw) > 0 {
		hb.MetadataJSON = json.RawMessage(raw)
	}
	return hb, nil
}

// bridgeHeartbeat reads a bridge's heartbeat by its id (found=false when it
// never heartbeat).
func bridgeHeartbeat(ctx context.Context, db sqlConn, bridgeID string) (BridgeHeartbeat, bool, error) {
	// cross-tenant: a bridge id is global (the bridge_heartbeats primary
	// key); its owner answers for it in every tenant.
	hb, err := scanBridgeHeartbeat(db.QueryRowContext(ctx, `SELECT `+bridgeHeartbeatColumns+` FROM bridge_heartbeats WHERE bridge_id=?`, bridgeID))
	if errors.Is(err, sql.ErrNoRows) {
		return BridgeHeartbeat{}, false, nil
	}
	return hb, err == nil, err
}

func (m *MySQLBridgeJobs) ClaimAgentJobs(ctx context.Context, req ClaimRequest) ([]AgentJob, error) {
	// cross-tenant: a job lives in its ROOM's tenant, but the bridge that
	// must drive it advertises and heartbeats in the tenant its owner's
	// session logged into (which may differ for a multi-tenant owner).
	// bridge_id is global, so authorization is bridge OWNERSHIP + LIVENESS
	// (ClaimRequest.through), not tenant; each returned job carries its own
	// tenant for the bridge to drive the turn in.
	req = req.normalize()
	out := []AgentJob{}
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	hb, found, err := bridgeHeartbeat(ctx, tx, req.BridgeID)
	if err != nil {
		return nil, err
	}
	if !found || !req.through(hb) {
		return out, nil
	}
	// The capability is the owner's own advertisement on this bridge.
	// Normally only the owner's jobs match; ServeAllOwners (single-host
	// testing, gated server-side by the activity service) lets the owner's
	// advertised core/provider drive every member's job. EXISTS, not a join:
	// a bridge advertising several targets of one core/provider must not
	// return (and count against MaxJobs) each job once per target.
	q := `SELECT ` + agentJobColumns + ` FROM agent_jobs j
WHERE ` + sqlClaimableJob + ` AND EXISTS (SELECT 1 FROM bridge_agents a
  WHERE a.bridge_id = ? AND a.owner_user_id = ? AND a.core = j.core AND a.provider = j.provider AND a.disabled = 0)`
	args := []any{req.NowMs, req.BridgeID, req.OwnerUserID}
	if !req.ServeAllOwners {
		q += ` AND j.owner_user_id = ?`
		args = append(args, req.OwnerUserID)
	}
	q += ` ORDER BY j.created_at_ms, j.job_id LIMIT ? FOR UPDATE`
	args = append(args, req.MaxJobs)
	cands, err := queryAll(ctx, tx, q, scanAgentJobRow, args...)
	if err != nil {
		return nil, err
	}
	for _, j := range cands {
		j = req.claimed(j)
		if _, err := tx.ExecContext(ctx, `UPDATE agent_jobs SET status=?, lease_bridge_id=?, lease_expires_at_ms=?, attempt=?, updated_at_ms=?, claimed_at_ms=? WHERE job_id=?`,
			string(j.Status), j.LeaseBridgeID, j.LeaseExpiresAtMs, j.Attempt, j.UpdatedAtMs, j.ClaimedAtMs, j.JobID); err != nil {
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
	// cross-tenant: bridge-driven job writes key on job_id (global) and are
	// authorized by lease_bridge_id (only the bridge holding the lease may
	// report), not by tenant: the driving bridge's session tenant may differ
	// from the job's (room's) tenant for a multi-tenant owner.
	upd = upd.normalize()
	if upd.requeues() {
		res, err := m.DB.ExecContext(ctx, `UPDATE agent_jobs SET status=?, failure_reason=?, diagnostics_json=?, updated_at_ms=?, lease_bridge_id='', lease_expires_at_ms=0
WHERE job_id=? AND lease_bridge_id=? AND status NOT IN (`+sqlTerminalJobStatuses+`) AND attempt < ?`,
			string(AgentJobQueued), string(upd.FailureReason), nullIfZero(string(upd.DiagnosticsJSON)), upd.NowMs, upd.JobID, upd.BridgeID, upd.MaxAttempts)
		if err != nil {
			return AgentJob{}, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return m.agentJobByID(ctx, upd.JobID)
		}
		// Attempt budget spent: fall through and record the terminal failure.
	}
	res, err := m.DB.ExecContext(ctx, `UPDATE agent_jobs
SET status=?,
    failure_reason=?,
    diagnostics_json=?,
    updated_at_ms=?,
    started_at_ms=IF(? AND started_at_ms=0, ?, started_at_ms),
    finished_at_ms=IF(?, ?, finished_at_ms),
    lease_expires_at_ms=?
WHERE job_id=? AND lease_bridge_id=? AND status NOT IN (`+sqlTerminalJobStatuses+`)`,
		string(upd.Status), string(upd.FailureReason), nullIfZero(string(upd.DiagnosticsJSON)), upd.NowMs,
		upd.Status == AgentJobRunning, upd.NowMs,
		upd.Status.Terminal(), upd.NowMs,
		upd.leaseExpiresAt(),
		upd.JobID, upd.BridgeID)
	if err != nil {
		return AgentJob{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return AgentJob{}, err
	} else if n == 0 {
		return AgentJob{}, m.jobMiss(ctx, `job_id=?`, upd.JobID)
	}
	return m.agentJobByID(ctx, upd.JobID)
}

// agentJobByID reads a job by its global id.
func (m *MySQLBridgeJobs) agentJobByID(ctx context.Context, jobID string) (AgentJob, error) {
	// cross-tenant: job_id is global; the bridge-driven writes that call it
	// are authorized by the lease, not the tenant.
	return m.scanAgentJob(ctx, `SELECT `+agentJobColumns+` FROM agent_jobs WHERE job_id=?`, jobID)
}

// jobMiss classifies a guarded job update that matched no row the way the
// memory store does: ErrAgentJobNotFound when no job matches where, else
// ErrAgentJobLeaseMismatch (another bridge holds the lease, or the job is
// already terminal).
func (m *MySQLBridgeJobs) jobMiss(ctx context.Context, where string, args ...any) error {
	// cross-tenant: where is the caller's own key, tenant-scoped for a
	// room action (cancel) and the global job id for a bridge report.
	var one int
	err := m.DB.QueryRowContext(ctx, `SELECT 1 FROM agent_jobs WHERE `+where, args...).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrAgentJobNotFound
	case err != nil:
		return err
	}
	return ErrAgentJobLeaseMismatch
}

func (m *MySQLBridgeJobs) RenewAgentJobLease(ctx context.Context, ren LeaseRenewal) error {
	// cross-tenant: keyed on job_id (global) + lease ownership, not tenant
	// (see UpdateAgentJobStatus).
	ren = ren.normalize()
	res, err := m.DB.ExecContext(ctx, `UPDATE agent_jobs SET lease_expires_at_ms=?, updated_at_ms=? WHERE job_id=? AND lease_bridge_id=? AND status NOT IN (`+sqlTerminalJobStatuses+`)`,
		ren.NowMs+ren.LeaseMs, ren.NowMs, ren.JobID, ren.BridgeID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return m.jobMiss(ctx, `job_id=?`, ren.JobID)
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
	key, keyArgs := `tenant_id=? AND job_id=?`, []any{tid, req.JobID}
	if req.RoomID != "" {
		key, keyArgs = key+` AND room_id=?`, append(keyArgs, req.RoomID)
	}
	res, err := m.DB.ExecContext(ctx, `UPDATE agent_jobs SET status=?, failure_reason=?, updated_at_ms=?, finished_at_ms=?, lease_expires_at_ms=0, lease_bridge_id=''
WHERE `+key+` AND status NOT IN (`+sqlTerminalJobStatuses+`)`,
		append([]any{string(AgentJobCancelled), string(FailureCancelled), req.NowMs, req.NowMs}, keyArgs...)...)
	if err != nil {
		return AgentJob{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return AgentJob{}, err
	} else if n == 0 {
		return AgentJob{}, m.jobMiss(ctx, key, keyArgs...)
	}
	return m.scanAgentJob(ctx, `
SELECT `+agentJobColumns+`
FROM agent_jobs WHERE tenant_id=? AND job_id=?`, tid, req.JobID)
}

// SweepAgentJobs fails jobs no worker can finish. It runs as cross-tenant
// maintenance, so it carries no tenant scope and returns the affected jobs for
// per-room notification.
func (m *MySQLBridgeJobs) SweepAgentJobs(ctx context.Context, req SweepRequest) ([]AgentJob, error) {
	// cross-tenant: the sweeper is maintenance over every tenant's queue.
	req = req.normalize()
	// Two indexed reads instead of one OR over the whole table: queued jobs
	// past the queue timeout (idx_agent_jobs_status_created) and live jobs
	// whose last lease lapsed on the final attempt
	// (idx_agent_jobs_status_lease). Oldest first, at most MaxSweptJobs;
	// SweepRequest.sweepReason decides each one.
	queued, err := queryAll(ctx, m.DB, `SELECT `+agentJobColumns+` FROM agent_jobs
WHERE status = ? AND created_at_ms <= ? ORDER BY created_at_ms, job_id LIMIT ?`, scanAgentJobRow,
		string(AgentJobQueued), req.queuedBefore(), MaxSweptJobs)
	if err != nil {
		return nil, err
	}
	lapsed, err := queryAll(ctx, m.DB, `SELECT `+agentJobColumns+` FROM agent_jobs
WHERE status IN (`+sqlLiveJobStatuses+`) AND lease_expires_at_ms <= ? AND attempt >= ? ORDER BY created_at_ms, job_id LIMIT ?`, scanAgentJobRow,
		req.NowMs, req.MaxAttempts, MaxSweptJobs)
	if err != nil {
		return nil, err
	}
	stuck := append(queued, lapsed...)
	sortJobsOldestFirst(stuck)
	if len(stuck) > MaxSweptJobs {
		stuck = stuck[:MaxSweptJobs]
	}
	out := []AgentJob{}
	for _, j := range stuck {
		if req.sweepReason(j) == "" {
			continue
		}
		f := req.failed(j)
		// Guarded on the row as read: a job claimed, renewed or finished
		// since is left alone.
		res, err := m.DB.ExecContext(ctx, `UPDATE agent_jobs SET status=?, failure_reason=?, updated_at_ms=?, finished_at_ms=?, lease_expires_at_ms=?
WHERE job_id=? AND status=? AND lease_expires_at_ms=? AND attempt=?`,
			string(f.Status), string(f.FailureReason), f.UpdatedAtMs, f.FinishedAtMs, f.LeaseExpiresAtMs,
			j.JobID, string(j.Status), j.LeaseExpiresAtMs, j.Attempt)
		if err != nil {
			return out, err
		}
		if n, err := res.RowsAffected(); err != nil {
			return out, err
		} else if n == 0 {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

func (m *MySQLBridgeJobs) PutBridgeHeartbeat(ctx context.Context, hb BridgeHeartbeat) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	if !hb.Status.valid() {
		return ErrBridgeStatusInvalid
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO bridge_heartbeats (bridge_id, tenant_id, owner_user_id, app_instance_id, version, status, serve_all_owners, last_seen_at_ms, metadata_json) VALUES (?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE tenant_id=VALUES(tenant_id), app_instance_id=VALUES(app_instance_id), version=VALUES(version), status=VALUES(status), serve_all_owners=VALUES(serve_all_owners), last_seen_at_ms=VALUES(last_seen_at_ms), metadata_json=VALUES(metadata_json)`, hb.BridgeID, tid, hb.OwnerUserID, hb.AppInstanceID, hb.Version, string(hb.Status), hb.ServeAllOwners, hb.LastSeenAtMs, nullIfZero(string(hb.MetadataJSON))); err != nil {
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
ON DUPLICATE KEY UPDATE owner_user_id=VALUES(owner_user_id), model_options_json=VALUES(model_options_json), disabled=VALUES(disabled), last_seen_at_ms=VALUES(last_seen_at_ms)`, bridgeID, tid, a.OwnerUserID, a.Core, a.Provider, a.TargetID, nullIfZero(string(a.ModelOptionsJSON)), a.Disabled, a.LastSeenAtMs); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// bridgeAgentColumns is the column list scanBridgeAgent reads, in its order
// (bridge_agents aliased a).
const bridgeAgentColumns = `a.bridge_id, a.tenant_id, a.owner_user_id, a.core, a.provider, a.target_id, a.model_options_json, a.disabled, a.last_seen_at_ms`

// bridgeAgentOrder is sortBridgeAgents in SQL.
const bridgeAgentOrder = `a.owner_user_id, a.core, a.provider, a.target_id, a.bridge_id`

// scanBridgeAgent reads one bridgeAgentColumns row.
func scanBridgeAgent(r rowScanner) (BridgeAgent, error) {
	var a BridgeAgent
	var raw []byte
	if err := r.Scan(&a.BridgeID, &a.TenantID, &a.OwnerUserID, &a.Core, &a.Provider, &a.TargetID, &raw, &a.Disabled, &a.LastSeenAtMs); err != nil {
		return BridgeAgent{}, err
	}
	a.ModelOptionsJSON = json.RawMessage(raw)
	return a, nil
}

func (m *MySQLBridgeJobs) scanAgentJob(ctx context.Context, q string, args ...any) (AgentJob, error) {
	return scanAgentJobRow(m.DB.QueryRowContext(ctx, q, args...))
}

// agentJobColumns is the one column list every agent_jobs read selects, in
// scanAgentJobRow's order.
const agentJobColumns = `job_id, tenant_id, room_id, channel_id, message_seq, client_msg_id, requester_user_id, owner_user_id, core, provider, target_id, message_text, attachment_ids, model, reasoning_effort, status, failure_reason, attempt, lease_bridge_id, lease_expires_at_ms, created_at_ms, updated_at_ms, started_at_ms, finished_at_ms, diagnostics_json, claimed_at_ms, dispatched_by_job_id, dispatch_depth, expert_id, expert_version, expert_name, room_expert_id, room_expert_version, room_expert_name`

func scanAgentJobRow(row rowScanner) (AgentJob, error) {
	var j AgentJob
	var status, reason string
	var messageText, model, reasoningEffort sql.NullString
	var attachmentIDs []byte
	var diag []byte
	err := row.Scan(&j.JobID, &j.TenantID, &j.RoomID, &j.ChannelID, &j.MessageSeq, &j.ClientMsgID, &j.RequesterUserID, &j.OwnerUserID, &j.Core, &j.Provider, &j.TargetID, &messageText, &attachmentIDs, &model, &reasoningEffort, &status, &reason, &j.Attempt, &j.LeaseBridgeID, &j.LeaseExpiresAtMs, &j.CreatedAtMs, &j.UpdatedAtMs, &j.StartedAtMs, &j.FinishedAtMs, &diag, &j.ClaimedAtMs, &j.DispatchedByJobID, &j.DispatchDepth, &j.ExpertID, &j.ExpertVersion, &j.ExpertName, &j.RoomExpertID, &j.RoomExpertVersion, &j.RoomExpertName)
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

// LatestBridgeForOwner returns the owner's latest heartbeat across tenants
// (see BridgeJobStore), on idx_bridge_heartbeats_owner_seen.
func (m *MySQLBridgeJobs) LatestBridgeForOwner(ctx context.Context, ownerUserID string) (BridgeHeartbeat, bool, error) {
	// cross-tenant: the owner identity is global (their desktop, wherever
	// it heartbeats).
	hb, err := scanBridgeHeartbeat(m.DB.QueryRowContext(ctx, `SELECT `+bridgeHeartbeatColumns+`
FROM bridge_heartbeats WHERE owner_user_id=? ORDER BY last_seen_at_ms DESC, bridge_id DESC LIMIT 1`, ownerUserID))
	if errors.Is(err, sql.ErrNoRows) {
		return BridgeHeartbeat{}, false, nil
	}
	return hb, err == nil, err
}

// ListBridgeAgentsForOwner returns the owner's advertised agents across all
// rooms and tenants, joined to a heartbeat live at nowMs on bridge_id (see
// BridgeJobStore). It reads idx_bridge_agents_owner.
func (m *MySQLBridgeJobs) ListBridgeAgentsForOwner(ctx context.Context, ownerUserID string, nowMs int64) ([]BridgeAgent, error) {
	// cross-tenant: the owner identity is global; a member's own agents are
	// their own agents wherever their bridge runs.
	out, err := queryAll(ctx, m.DB, `SELECT `+bridgeAgentColumns+`
FROM bridge_agents a
JOIN bridge_heartbeats h ON h.bridge_id=a.bridge_id AND `+liveBridgePred+`
WHERE a.owner_user_id=? AND a.disabled=0
ORDER BY `+bridgeAgentOrder, scanBridgeAgent, bridgeLiveSince(nowMs), ownerUserID)
	if out == nil && err == nil {
		out = []BridgeAgent{}
	}
	return out, err
}

// PendingAgentJobs counts the owner's not-yet-started jobs across tenants
// (see BridgeJobStore), on idx_agent_jobs_owner_status.
func (m *MySQLBridgeJobs) PendingAgentJobs(ctx context.Context, ownerUserID string) ([]AgentJobCount, error) {
	// cross-tenant: a host serves rooms of every organization; the queue
	// ahead of its agent spans them all.
	out, err := queryAll(ctx, m.DB, `SELECT core, provider, COUNT(*) FROM agent_jobs
WHERE owner_user_id=? AND status IN (`+sqlPendingJobStatuses+`)
GROUP BY core, provider ORDER BY core, provider`, func(r rowScanner) (AgentJobCount, error) {
		var c AgentJobCount
		err := r.Scan(&c.Core, &c.Provider, &c.Count)
		return c, err
	}, ownerUserID)
	if out == nil && err == nil {
		out = []AgentJobCount{}
	}
	return out, err
}
