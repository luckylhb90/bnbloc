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
		caps, nullInt(g.RunQuota), nullFloat(g.CostAllowance), g.MaxConcurrent, nullInt64(g.ResetAtMs),
		g.RunsUsed, g.CostUsed, nullInt64(g.RevokedAtMs), nullInt64(g.ExpiresAtMs), g.CreatedAtMs, g.Source, g.CreatedBy)
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

type grantScanner interface{ Scan(dest ...any) error }

// scanGrant reads one grantColumns row (TenantID is the caller's to set).
func scanGrant(row grantScanner) (Grant, error) {
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
	if costAllowance.Valid {
		g.CostAllowance = &costAllowance.Float64
	}
	if resetAt.Valid {
		g.ResetAtMs = &resetAt.Int64
	}
	if revokedAt.Valid {
		g.RevokedAtMs = &revokedAt.Int64
	}
	if expiresAt.Valid {
		g.ExpiresAtMs = &expiresAt.Int64
	}
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
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrGrantNotFound
	}
	return nil
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
		tnt, rec.RoomID, nullStr(rec.GrantID), rec.ActorUserID, rec.OwnerUserID, rec.Action,
		rec.Decision, nullStr(rec.DenyReason), nullStr(rec.Model), rec.ChannelID, rec.AtMs, nullFloat(rec.Cost),
		nullInt64(rec.InputTokens), nullInt64(rec.OutputTokens))
	return err
}

// AuditRecords returns a room's audit records newest-first. limit<=0 defaults
// to 100 (capped at 500); beforeAtMs>0 keeps only records strictly older than
// that timestamp for cursor pagination.
func (s *MySQLGrant) AuditRecords(ctx context.Context, roomID string, limit int, beforeAtMs int64) []AuditRecord {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	where := "tenant_id = ? AND room_id = ?"
	args := []any{tnt, roomID}
	if beforeAtMs > 0 {
		where += " AND at_ms < ?"
		args = append(args, beforeAtMs)
	}
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx,
		`SELECT room_id, COALESCE(grant_id,''), actor_user_id, owner_user_id, action, decision,
		        COALESCE(deny_reason,''), COALESCE(model,''), COALESCE(channel_id,''), at_ms
		 FROM shared_agent_audit WHERE `+where+` ORDER BY at_ms DESC LIMIT ?`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []AuditRecord
	for rows.Next() {
		var r AuditRecord
		if err := rows.Scan(&r.RoomID, &r.GrantID, &r.ActorUserID, &r.OwnerUserID, &r.Action,
			&r.Decision, &r.DenyReason, &r.Model, &r.ChannelID, &r.AtMs); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// TenantAuditRecords returns the ctx tenant's audit records across ALL rooms
// newest-first for an org-wide audit trail. limit<=0 defaults to 100 (capped at
// 500); beforeAtMs>0 keeps only records strictly older than that timestamp.
func (s *MySQLGrant) TenantAuditRecords(ctx context.Context, limit int, beforeAtMs int64) []AuditRecord {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	where := "tenant_id = ?"
	args := []any{tnt}
	if beforeAtMs > 0 {
		where += " AND at_ms < ?"
		args = append(args, beforeAtMs)
	}
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx,
		`SELECT room_id, COALESCE(grant_id,''), actor_user_id, owner_user_id, action, decision,
		        COALESCE(deny_reason,''), COALESCE(model,''), COALESCE(channel_id,''), at_ms
		 FROM shared_agent_audit WHERE `+where+` ORDER BY at_ms DESC LIMIT ?`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []AuditRecord
	for rows.Next() {
		var r AuditRecord
		if err := rows.Scan(&r.RoomID, &r.GrantID, &r.ActorUserID, &r.OwnerUserID, &r.Action,
			&r.Decision, &r.DenyReason, &r.Model, &r.ChannelID, &r.AtMs); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// RoomGrants returns all grants in a room newest-first (callers filter
// revoked/expired).
func (s *MySQLGrant) RoomGrants(ctx context.Context, roomID string) []Grant {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT `+grantColumns+` FROM shared_agent_grants WHERE tenant_id = ? AND room_id = ? ORDER BY created_at_ms DESC`, tnt, roomID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			continue
		}
		g.TenantID = tnt
		out = append(out, g)
	}
	return out
}

func nullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
func nullInt64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
func nullFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
