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
func (s *MySQLGrant) BorrowTurns(ctx context.Context, q BorrowTurnQuery) []AuditRecord {
	where := "a.action = 'turn' AND a.decision = 'allow' AND a.actor_user_id <> '' AND a.actor_user_id <> a.owner_user_id AND a.at_ms >= ?"
	args := []any{q.FromMs}
	if !q.AllTenants {
		tnt, err := tenant.Require(ctx)
		if err != nil {
			return nil
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
	rows, err := s.DB.QueryContext(ctx,
		`SELECT a.tenant_id, a.room_id, COALESCE(a.grant_id,''), a.actor_user_id, a.owner_user_id, a.action, a.decision,
		        COALESCE(a.model,''), COALESCE(a.channel_id,''), a.at_ms, a.cost, a.input_tokens, a.output_tokens
		 FROM `+from+` WHERE `+where+` ORDER BY a.at_ms ASC LIMIT ?`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []AuditRecord
	for rows.Next() {
		var r AuditRecord
		var cost sql.NullFloat64
		var inTok, outTok sql.NullInt64
		if err := rows.Scan(&r.TenantID, &r.RoomID, &r.GrantID, &r.ActorUserID, &r.OwnerUserID, &r.Action,
			&r.Decision, &r.Model, &r.ChannelID, &r.AtMs, &cost, &inTok, &outTok); err != nil {
			continue
		}
		if cost.Valid {
			c := cost.Float64
			r.Cost = &c
		}
		if inTok.Valid {
			v := inTok.Int64
			r.InputTokens = &v
		}
		if outTok.Valid {
			v := outTok.Int64
			r.OutputTokens = &v
		}
		out = append(out, r)
	}
	return out
}

// RaiseRunQuota adds runs to a live grant's run quota in one conditional
// UPDATE that never touches runs_used/cost_used (see BorrowUsageStore). A miss
// is classified by reading the grant back.
func (s *MySQLGrant) RaiseRunQuota(ctx context.Context, grantID string, add int) (Grant, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return Grant{}, err
	}
	res, err := s.DB.ExecContext(ctx,
		`UPDATE shared_agent_grants SET run_quota = run_quota + ?
		 WHERE grant_id = ? AND tenant_id = ? AND run_quota IS NOT NULL AND revoked_at_ms IS NULL`,
		add, grantID, tnt)
	if err != nil {
		return Grant{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		g, err := s.GetGrant(ctx, grantID)
		if err != nil {
			return Grant{}, err
		}
		if g.RevokedAtMs != nil {
			return Grant{}, ErrGrantNotFound
		}
		if g.RunQuota == nil {
			return Grant{}, ErrRunQuotaUnlimited
		}
	}
	return s.GetGrant(ctx, grantID)
}
