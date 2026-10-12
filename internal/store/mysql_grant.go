package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// MySQLGrant implements GrantStore over database/sql. Schema matches deploy/schema.sql.
type MySQLGrant struct{ DB *sql.DB }

// NewMySQLGrant constructs a MySQL-backed grant store.
func NewMySQLGrant(db *sql.DB) *MySQLGrant { return &MySQLGrant{DB: db} }

var _ GrantStore = (*MySQLGrant)(nil)

// PutGrant inserts or replaces a grant.
func (s *MySQLGrant) PutGrant(ctx context.Context, g Grant) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	models, _ := jsonArg(g.AllowedModels)
	caps, _ := jsonArg(g.Capabilities)
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO shared_agent_grants
		 (grant_id, tenant_id, room_id, target_id, owner_user_id, grantee_user_id, allowed_models_json,
		  capabilities_json, run_quota, cost_allowance, max_concurrent, reset_at_ms,
		  runs_used, cost_used, revoked_at_ms, expires_at_ms, created_at_ms, source, created_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE
		  source = VALUES(source), created_by = VALUES(created_by), created_at_ms = VALUES(created_at_ms),
		  allowed_models_json = VALUES(allowed_models_json),
		  capabilities_json = VALUES(capabilities_json),
		  run_quota = VALUES(run_quota), cost_allowance = VALUES(cost_allowance),
		  max_concurrent = VALUES(max_concurrent), reset_at_ms = VALUES(reset_at_ms),
		  expires_at_ms = VALUES(expires_at_ms),
		  -- A re-authorization is a fresh grant: clear any prior revocation and
		  -- reset usage counters so a re-enabled provider starts a clean window.
		  revoked_at_ms = VALUES(revoked_at_ms),
		  runs_used = VALUES(runs_used), cost_used = VALUES(cost_used)`,
		g.GrantID, tnt, g.RoomID, g.TargetID, g.OwnerUserID, g.GranteeUserID, models,
		caps, nullPtr(g.RunQuota), nullPtr(g.CostAllowance), g.MaxConcurrent, nullPtr(g.ResetAtMs),
		g.RunsUsed, g.CostUsed, nullPtr(g.RevokedAtMs), nullPtr(g.ExpiresAtMs), g.CreatedAtMs, g.Source, g.CreatedBy)
	return err
}

// GetGrant returns a grant or ErrGrantNotFound.
func (s *MySQLGrant) GetGrant(ctx context.Context, grantID string) (Grant, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return Grant{}, err
	}
	g, err := scanGrant(s.DB.QueryRowContext(ctx,
		`SELECT `+grantColumns+` FROM shared_agent_grants WHERE grant_id = ? AND tenant_id = ?`, grantID, tnt))
	if errors.Is(err, sql.ErrNoRows) {
		return Grant{}, ErrGrantNotFound
	}
	if err != nil {
		return Grant{}, err
	}
	g.TenantID = tnt
	return g, nil
}

// grantColumns is the column list every grant read selects, in scanGrant's
// order.
const grantColumns = `grant_id, room_id, target_id, owner_user_id, grantee_user_id, allowed_models_json,
		        capabilities_json, run_quota, cost_allowance, max_concurrent, reset_at_ms,
		        runs_used, cost_used, revoked_at_ms, expires_at_ms, created_at_ms, source, created_by`

// scanGrant reads one grantColumns row (TenantID is the caller's to set).
func scanGrant(row rowScanner) (Grant, error) {
	var g Grant
	var models, caps []byte
	var runQuota sql.NullInt64
	var costAllowance sql.NullFloat64
	var resetAt, revokedAt, expiresAt sql.NullInt64
	if err := row.Scan(
		&g.GrantID, &g.RoomID, &g.TargetID, &g.OwnerUserID, &g.GranteeUserID, &models,
		&caps, &runQuota, &costAllowance, &g.MaxConcurrent, &resetAt,
		&g.RunsUsed, &g.CostUsed, &revokedAt, &expiresAt, &g.CreatedAtMs, &g.Source, &g.CreatedBy); err != nil {
		return Grant{}, err
	}
	_ = json.Unmarshal(models, &g.AllowedModels)
	_ = json.Unmarshal(caps, &g.Capabilities)
	if runQuota.Valid {
		v := int(runQuota.Int64)
		g.RunQuota = &v
	}
	g.CostAllowance = validFloat(costAllowance)
	g.ResetAtMs, g.RevokedAtMs, g.ExpiresAtMs = validInt64(resetAt), validInt64(revokedAt), validInt64(expiresAt)
	return g, nil
}

// RevokeGrant marks a grant revoked.
func (s *MySQLGrant) RevokeGrant(ctx context.Context, grantID string, atMs int64) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE shared_agent_grants SET revoked_at_ms = ? WHERE grant_id = ? AND tenant_id = ?`, atMs, grantID, tnt)
	if err != nil {
		return err
	}
	return requireMatched(res, ErrGrantNotFound)
}

// TenantUsage sums runs_used and cost_used across the ctx tenant's
// owner-issued grants (platform grants are outside the org's borrow
// ceilings).
func (s *MySQLGrant) TenantUsage(ctx context.Context) (int, float64, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return 0, 0, err
	}
	var runs int
	var cost float64
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(runs_used),0), COALESCE(SUM(cost_used),0) FROM shared_agent_grants WHERE tenant_id = ? AND source <> 'platform'`,
		tnt).Scan(&runs, &cost); err != nil {
		return 0, 0, err
	}
	return runs, cost, nil
}

// AppendAudit records a decision, returning any persistence error.
func (s *MySQLGrant) AppendAudit(ctx context.Context, rec AuditRecord) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO shared_agent_audit
		 (tenant_id, room_id, grant_id, actor_user_id, owner_user_id, action, decision, deny_reason, model, channel_id, at_ms, cost, input_tokens, output_tokens)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tnt, rec.RoomID, nullIfZero(rec.GrantID), rec.ActorUserID, rec.OwnerUserID, rec.Action,
		rec.Decision, nullIfZero(rec.DenyReason), nullIfZero(rec.Model), rec.ChannelID, rec.AtMs, nullPtr(rec.Cost),
		nullPtr(rec.InputTokens), nullPtr(rec.OutputTokens))
	return err
}

// auditRecordColumns is the column list scanAuditRecord reads, in its order:
// every column of a record.
const auditRecordColumns = `audit_id, tenant_id, room_id, COALESCE(grant_id,''), actor_user_id, owner_user_id, action, decision,
		        COALESCE(deny_reason,''), COALESCE(model,''), channel_id, at_ms, cost, input_tokens, output_tokens,
		        COALESCE(reservation_id,''), settled_at_ms`

// scanAuditRecord reads one auditRecordColumns row.
func scanAuditRecord(row rowScanner) (AuditRecord, error) {
	var r AuditRecord
	var cost sql.NullFloat64
	var inTok, outTok, settled sql.NullInt64
	if err := row.Scan(&r.ID, &r.TenantID, &r.RoomID, &r.GrantID, &r.ActorUserID, &r.OwnerUserID, &r.Action,
		&r.Decision, &r.DenyReason, &r.Model, &r.ChannelID, &r.AtMs, &cost, &inTok, &outTok,
		&r.ReservationID, &settled); err != nil {
		return AuditRecord{}, err
	}
	r.Cost = validFloat(cost)
	r.InputTokens, r.OutputTokens, r.SettledAtMs = validInt64(inTok), validInt64(outTok), validInt64(settled)
	return r, nil
}

// validFloat reads a nullable DECIMAL column (nil = NULL).
func validFloat(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	return &v.Float64
}

// validInt64 reads a nullable BIGINT column (nil = NULL).
func validInt64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

// AuditRecords returns one page of the ctx tenant's borrow audit trail (see
// GrantStore; idx_audit_room_time for a room, idx_audit_tenant_time for the
// tenant).
func (s *MySQLGrant) AuditRecords(ctx context.Context, q AuditRecordQuery) ([]AuditRecord, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	return s.borrowAuditPage(ctx, AuditLogQuery{TenantID: tnt, After: q.After, Limit: auditPage(q.Limit)}, q.RoomID)
}

// borrowAuditPage is the one read of shared_agent_audit pages (AuditRecords
// and the AuditLog reader): the rows q and roomID ("" = any) admit, in merge
// order (at_ms DESC, audit_id DESC), at most q.Limit.
func (s *MySQLGrant) borrowAuditPage(ctx context.Context, q AuditLogQuery, roomID string) ([]AuditRecord, error) {
	where, args := auditSQLFilter(q, auditSourceRank(AuditSourceBorrow), "tenant_id", "actor_user_id", "decision", "", "audit_id")
	if roomID != "" {
		where += " AND room_id = ?"
		args = append(args, roomID)
	}
	args = append(args, q.Limit)
	return queryAll(ctx, s.DB,
		`SELECT `+auditRecordColumns+` FROM shared_agent_audit WHERE `+where+` ORDER BY at_ms DESC, audit_id DESC LIMIT ?`, scanAuditRecord, args...)
}

// RoomGrants returns all grants in a room newest-first (callers filter
// revoked/expired).
func (s *MySQLGrant) RoomGrants(ctx context.Context, roomID string) ([]Grant, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	return queryAll(ctx, s.DB,
		`SELECT `+grantColumns+` FROM shared_agent_grants WHERE tenant_id = ? AND room_id = ? ORDER BY created_at_ms DESC, grant_id DESC`,
		func(r rowScanner) (Grant, error) {
			g, err := scanGrant(r)
			g.TenantID = tnt
			return g, err
		}, tnt, roomID)
}
