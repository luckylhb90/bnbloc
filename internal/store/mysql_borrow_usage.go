package store

import (
	"context"
	"database/sql"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// BorrowTurns returns the ctx tenant's recorded borrow turns (see
// BorrowUsageStore). The tenant+time scan uses idx_audit_tenant_time (the
// all-tenants scan idx_audit_time). The actor<>owner comparison is exact (the
// columns are utf8mb4_bin), matching the memory store's ==. PlatformOnly joins
// the turn's grant (grant ids are globally unique: the tenant is part of their
// hash).
func (s *MySQLGrant) BorrowTurns(ctx context.Context, q BorrowTurnQuery) ([]AuditRecord, error) {
	where := "a.action = 'turn' AND a.decision = 'allow' AND a.actor_user_id <> '' AND a.actor_user_id <> a.owner_user_id AND a.at_ms >= ?"
	args := []any{q.FromMs}
	if !q.AllTenants {
		tnt, err := tenant.Require(ctx)
		if err != nil {
			return nil, err
		}
		where = "a.tenant_id = ? AND " + where
		args = append([]any{tnt}, args...)
	}
	if q.ToMs > 0 {
		where += " AND a.at_ms < ?"
		args = append(args, q.ToMs)
	}
	if q.OwnerUserID != "" {
		where += " AND a.owner_user_id = ?"
		args = append(args, q.OwnerUserID)
	}
	if q.BorrowerUserID != "" {
		where += " AND a.actor_user_id = ?"
		args = append(args, q.BorrowerUserID)
	}
	from := "shared_agent_audit a"
	if q.PlatformOnly {
		from += " JOIN shared_agent_grants g ON g.grant_id = a.grant_id AND g.source = '" + GrantSourcePlatform + "'"
	}
	args = append(args, MaxBorrowTurnRows)
	return queryAll(ctx, s.DB,
		`SELECT a.audit_id, a.tenant_id, a.room_id, COALESCE(a.grant_id,''), a.actor_user_id, a.owner_user_id, a.action, a.decision,
		        COALESCE(a.model,''), a.channel_id, a.at_ms, a.cost, a.input_tokens, a.output_tokens
		 FROM `+from+` WHERE `+where+` ORDER BY a.at_ms ASC, a.audit_id ASC LIMIT ?`, scanBorrowTurn, args...)
}

// scanBorrowTurn reads one BorrowTurns row.
func scanBorrowTurn(row rowScanner) (AuditRecord, error) {
	var r AuditRecord
	var cost sql.NullFloat64
	var inTok, outTok sql.NullInt64
	if err := row.Scan(&r.ID, &r.TenantID, &r.RoomID, &r.GrantID, &r.ActorUserID, &r.OwnerUserID, &r.Action,
		&r.Decision, &r.Model, &r.ChannelID, &r.AtMs, &cost, &inTok, &outTok); err != nil {
		return AuditRecord{}, err
	}
	r.Cost, r.InputTokens, r.OutputTokens = validFloat(cost), validInt64(inTok), validInt64(outTok)
	return r, nil
}

// RaiseRunQuota adds runs to a live grant's run quota in one conditional
// UPDATE that never touches runs_used/cost_used (see BorrowUsageStore). A miss
// is classified by reading the grant back.
func (s *MySQLGrant) RaiseRunQuota(ctx context.Context, grantID string, add int, atMs int64) (Grant, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return Grant{}, err
	}
	res, err := s.DB.ExecContext(ctx,
		`UPDATE shared_agent_grants SET run_quota = run_quota + ?
		 WHERE grant_id = ? AND tenant_id = ? AND run_quota IS NOT NULL AND `+sqlGrantLive,
		add, grantID, tnt, atMs)
	if err != nil {
		return Grant{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		g, err := s.GetGrant(ctx, grantID)
		if err != nil {
			return Grant{}, err
		}
		if !g.Live(atMs) {
			return Grant{}, ErrGrantNotFound
		}
		if g.RunQuota == nil {
			return Grant{}, ErrRunQuotaUnlimited
		}
	}
	return s.GetGrant(ctx, grantID)
}
